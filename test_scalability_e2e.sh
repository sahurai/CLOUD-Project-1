#!/usr/bin/env bash
#
# End-to-end Kubernetes scalability proof for both CPU and GPU worker pools.
#
# The script:
#   1. Opens a port-forward to the load balancer if needed.
#   2. Sends sustained CPU-routed traffic using small images.
#   3. Verifies ai-worker-cpu HPA increases desired/current replicas.
#   4. Sends sustained GPU-routed traffic using large images.
#   5. Verifies ai-worker-gpu HPA increases desired/current replicas.
#
# Usage:
#   ./test_scalability_e2e.sh [BASE_URL] [DURATION_SECONDS] [CPU_CONCURRENCY] [GPU_CONCURRENCY]
#
# Example:
#   ./test_scalability_e2e.sh http://localhost:8000 360 20 10
#
# Notes:
#   - Requires Metrics Server: kubectl top nodes must work.
#   - If kubectl is not installed, the script uses: minikube kubectl --
#   - DURATION_SECONDS is a max duration per pool; each pool finishes early
#     once routing and HPA scale-up have been proven.
set -euo pipefail

BASE_URL="${1:-http://localhost:8000}"
DURATION_SECONDS="${2:-360}"
CPU_CONCURRENCY="${3:-20}"
GPU_CONCURRENCY="${4:-10}"
NAMESPACE="${NAMESPACE:-glaucoma}"
PORT_FORWARD_PID=""
TMP_DIR="$(mktemp -d)"
SMALL_IMAGE="$TMP_DIR/cpu_small.png"
LARGE_IMAGE="$TMP_DIR/gpu_large.png"
MODEL_NAME=""
CPU_ROUTING_RESULT="FAIL"
GPU_ROUTING_RESULT="FAIL"
CPU_SCALING_RESULT="FAIL"
GPU_SCALING_RESULT="FAIL"
CPU_UNIQUE_WORKERS=0
GPU_UNIQUE_WORKERS=0

cleanup() {
    if [ -n "$PORT_FORWARD_PID" ]; then
        kill "$PORT_FORWARD_PID" 2>/dev/null || true
    fi
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

if command -v kubectl >/dev/null 2>&1; then
    KUBECTL=(kubectl)
elif command -v minikube >/dev/null 2>&1; then
    KUBECTL=(minikube kubectl --)
else
    echo "ERROR: neither kubectl nor minikube was found." >&2
    exit 1
fi

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "ERROR: required command not found: $1" >&2
        exit 1
    fi
}

require_command curl
require_command python3

models_url() {
    printf "%s/models/" "$BASE_URL"
}

predict_url() {
    printf "%s/predict/" "$BASE_URL"
}

wait_for_url() {
    url="$1"
    tries=60
    while [ "$tries" -gt 0 ]; do
        if curl -sf "$url" >/dev/null 2>&1; then
            return 0
        fi
        tries=$((tries - 1))
        sleep 1
    done
    return 1
}

ensure_port_forward() {
    if curl -sf "$(models_url)" >/dev/null 2>&1; then
        return 0
    fi

    if [ "$BASE_URL" != "http://localhost:8000" ] && [ "$BASE_URL" != "http://127.0.0.1:8000" ]; then
        echo "ERROR: Cannot reach $(models_url), and automatic port-forward only supports localhost:8000." >&2
        exit 1
    fi

    echo "Starting port-forward: svc/load-balancer 8000:8080"
    "${KUBECTL[@]}" -n "$NAMESPACE" port-forward svc/load-balancer 8000:8080 >/tmp/glaucoma-port-forward.log 2>&1 &
    PORT_FORWARD_PID="$!"

    if ! wait_for_url "$(models_url)"; then
        echo "ERROR: port-forward started but backend is still unreachable." >&2
        echo "Port-forward log:" >&2
        cat /tmp/glaucoma-port-forward.log >&2 || true
        exit 1
    fi
}

metric_value() {
    # Prints currentReplicas or desiredReplicas from HPA status.
    hpa="$1"
    field="$2"
    "${KUBECTL[@]}" -n "$NAMESPACE" get hpa "$hpa" -o "jsonpath={.status.${field}}" 2>/dev/null || true
}

available_replicas() {
    deployment="$1"
    "${KUBECTL[@]}" -n "$NAMESPACE" get deployment "$deployment" -o jsonpath='{.status.availableReplicas}' 2>/dev/null || true
}

hpa_cpu_utilization() {
    hpa="$1"
    "${KUBECTL[@]}" -n "$NAMESPACE" get hpa "$hpa" -o jsonpath='{.status.currentMetrics[0].resource.current.averageUtilization}' 2>/dev/null || true
}

wait_for_hpa_metrics() {
    hpa="$1"
    echo "Waiting for $hpa metrics..."

    tries=48
    while [ "$tries" -gt 0 ]; do
        utilization="$(hpa_cpu_utilization "$hpa")"
        if [ -n "$utilization" ]; then
            echo "$hpa metrics ready: cpu ${utilization}%"
            return 0
        fi
        sleep 5
        tries=$((tries - 1))
    done

    echo "WARNING: $hpa metrics are still not ready; continuing anyway."
    return 0
}

print_scaling_state() {
    echo ""
    echo "----- $(date '+%H:%M:%S') scaling state -----"
    "${KUBECTL[@]}" -n "$NAMESPACE" get hpa ai-worker-cpu ai-worker-gpu || true
    "${KUBECTL[@]}" -n "$NAMESPACE" get pods -l 'app in (ai-worker-cpu,ai-worker-gpu)' || true
}

generate_images() {
    python3 - "$SMALL_IMAGE" "$LARGE_IMAGE" <<'PY'
import sys
import numpy as np
from PIL import Image

small_path, large_path = sys.argv[1], sys.argv[2]

Image.new("RGB", (420, 420), color=(120, 190, 80)).save(small_path)

# Random pixels create a PNG over the 2.5 MB load-balancer threshold.
arr = np.random.randint(0, 256, (1400, 1400, 3), dtype=np.uint8)
Image.fromarray(arr).save(large_path)
PY
}

send_request() {
    image_path="$1"
    response="$(curl -s --max-time 180 -X POST "$(predict_url)" \
        -F "file=@${image_path}" \
        -F "model_name=${MODEL_NAME}" || true)"

    python3 - "$response" <<'PY'
import json
import sys

try:
    payload = json.loads(sys.argv[1])
    print("\t".join([
        payload.get("status", "unknown"),
        payload.get("node_type", "unknown"),
        payload.get("worker_id", "unknown"),
        str(payload.get("execution_time", "0")),
    ]))
except Exception:
    print("parse-error\tunknown\tunknown\t0")
PY
}

run_load_batch() {
    image_path="$1"
    concurrency="$2"
    expected="$3"
    results="$4"

    pids=""
    i=0
    while [ "$i" -lt "$concurrency" ]; do
        bash -c 'send_request "$1"' _ "$image_path" >> "$results" &
        pids="$pids $!"
        i=$((i + 1))
    done

    for pid in $pids; do
        wait "$pid" || true
    done

    if ! tail -n "$concurrency" "$results" | cut -f2 | grep -q "$expected"; then
        echo "WARNING: latest batch did not show expected node type '$expected'." >&2
    fi
}

export -f send_request predict_url
export BASE_URL MODEL_NAME

test_pool() {
    label="$1"
    hpa="$2"
    deployment="$3"
    image_path="$4"
    concurrency="$5"
    expected_node="$6"
    results="$TMP_DIR/${label}.tsv"

    : > "$results"

    echo ""
    echo "=== Testing $label scalability ==="
    echo "HPA:         $hpa"
    echo "Deployment:  $deployment"
    echo "Duration:    ${DURATION_SECONDS}s"
    echo "Concurrency: $concurrency"

    initial_desired="$(metric_value "$hpa" desiredReplicas)"
    initial_current="$(metric_value "$hpa" currentReplicas)"
    initial_available="$(available_replicas "$deployment")"
    initial_desired="${initial_desired:-1}"
    initial_current="${initial_current:-1}"
    initial_available="${initial_available:-1}"

    echo "Initial HPA desired/current: ${initial_desired}/${initial_current}"
    echo "Initial deployment available: ${initial_available}"

    wait_for_hpa_metrics "$hpa"

    start_time="$(date +%s)"
    max_desired="$initial_desired"
    max_current="$initial_current"
    max_available="$initial_available"
    last_print=0
    routed_seen=0
    unique_workers_seen=0
    scaled_seen=0
    success_batches_after_scale=0

    while [ $(( $(date +%s) - start_time )) -lt "$DURATION_SECONDS" ]; do
        run_load_batch "$image_path" "$concurrency" "$expected_node" "$results"

        live_summary="$(python3 - "$results" "$expected_node" <<'PY'
import collections
import sys

expected = sys.argv[2]
rows = []
with open(sys.argv[1], "r", encoding="utf-8") as f:
    for line in f:
        parts = line.rstrip("\n").split("\t")
        if len(parts) == 4:
            rows.append(parts)

nodes = collections.Counter(row[1] for row in rows)
workers = collections.Counter(row[2] for row in rows)
valid_workers = [w for w in workers if w != "unknown"]
routed = any(expected in node for node in nodes)

print(f"{1 if routed else 0} {len(valid_workers)}")
PY
)"
        routed_seen="$(printf "%s" "$live_summary" | awk '{print $1}')"
        unique_workers_seen="$(printf "%s" "$live_summary" | awk '{print $2}')"

        desired="$(metric_value "$hpa" desiredReplicas)"
        current="$(metric_value "$hpa" currentReplicas)"
        available="$(available_replicas "$deployment")"
        desired="${desired:-0}"
        current="${current:-0}"
        available="${available:-0}"

        [ "$desired" -gt "$max_desired" ] && max_desired="$desired"
        [ "$current" -gt "$max_current" ] && max_current="$current"
        [ "$available" -gt "$max_available" ] && max_available="$available"

        if [ "$max_current" -gt 1 ] || [ "$max_available" -gt 1 ]; then
            scaled_seen=1
        fi

        now="$(date +%s)"
        if [ $((now - last_print)) -ge 20 ]; then
            print_scaling_state
            echo "Progress: routed=${routed_seen}, unique_workers=${unique_workers_seen}, ready_scaled=${scaled_seen}, max_desired=${max_desired}, max_current=${max_current}, max_available=${max_available}"
            last_print="$now"
        fi

        if [ "$routed_seen" = "1" ] && [ "$scaled_seen" = "1" ]; then
            success_batches_after_scale=$((success_batches_after_scale + 1))
        else
            success_batches_after_scale=0
        fi

        if [ "$success_batches_after_scale" -ge 2 ]; then
            echo ""
            echo "Early success: $label routed correctly and has more than one current/available replica."
            break
        fi
    done

    echo ""
    echo "Summary for $label:"
    summary="$(python3 - "$results" "$expected_node" <<'PY'
import collections
import sys

expected = sys.argv[2]
rows = []
with open(sys.argv[1], "r", encoding="utf-8") as f:
    for line in f:
        parts = line.rstrip("\n").split("\t")
        if len(parts) == 4:
            rows.append(parts)

statuses = collections.Counter(row[0] for row in rows)
nodes = collections.Counter(row[1] for row in rows)
workers = collections.Counter(row[2] for row in rows)
valid_workers = [w for w in workers if w != "unknown"]
routed = any(expected in node for node in nodes)

print(f"Requests: {len(rows)}")
print(f"Statuses: {dict(statuses)}")
print(f"Node types: {dict(nodes)}")
print(f"Unique workers: {len(valid_workers)}")
for worker, count in workers.most_common():
    print(f"  {worker}: {count}")
print(f"__ROUTED__={1 if routed else 0}")
print(f"__UNIQUE_WORKERS__={len(valid_workers)}")
PY
)"
    printf "%s\n" "$summary" | grep -v '^__'

    routed="$(printf "%s\n" "$summary" | awk -F= '/^__ROUTED__=/{print $2}')"
    unique_workers="$(printf "%s\n" "$summary" | awk -F= '/^__UNIQUE_WORKERS__=/{print $2}')"
    unique_workers="${unique_workers:-0}"

    echo "Max HPA desired/current: ${max_desired}/${max_current}"
    echo "Max deployment available: ${max_available}"

    scaling_result="FAIL"
    if [ "$max_current" -gt 1 ] || [ "$max_available" -gt 1 ]; then
        scaling_result="PASS"
    fi

    routing_result="FAIL"
    if [ "$routed" = "1" ]; then
        routing_result="PASS"
    fi

    if [ "$deployment" = "ai-worker-cpu" ]; then
        CPU_ROUTING_RESULT="$routing_result"
        CPU_SCALING_RESULT="$scaling_result"
        CPU_UNIQUE_WORKERS="$unique_workers"
    else
        GPU_ROUTING_RESULT="$routing_result"
        GPU_SCALING_RESULT="$scaling_result"
        GPU_UNIQUE_WORKERS="$unique_workers"
    fi

    echo "Routing: $routing_result"
    echo "Scaling: $scaling_result"

    if [ "$routing_result" != "PASS" ]; then
        echo "FAIL: $label traffic was not routed to expected node type '$expected_node'." >&2
        return 1
    fi

    if [ "$scaling_result" != "PASS" ]; then
        echo "FAIL: $label did not scale up." >&2
        return 1
    fi

    echo "PASS: $label routing and scaling are working."
}

echo "=== CPU and GPU HPA Scalability E2E Test ==="
echo "Namespace: $NAMESPACE"
echo "Base URL:  $BASE_URL"
echo ""

echo "[0] Checking Kubernetes access..."
"${KUBECTL[@]}" get namespace "$NAMESPACE" >/dev/null
"${KUBECTL[@]}" -n "$NAMESPACE" get hpa ai-worker-cpu ai-worker-gpu >/dev/null

echo "[1] Checking Metrics Server..."
if ! "${KUBECTL[@]}" top nodes >/dev/null 2>&1; then
    echo "ERROR: Metrics API is not ready. Run: minikube addons enable metrics-server" >&2
    exit 1
fi

echo "[2] Connecting to load balancer..."
ensure_port_forward

MODEL_NAME="$(curl -sf "$(models_url)" | python3 -c "import sys,json; m=json.load(sys.stdin).get('models',[]); print(m[0] if m else '')")"
if [ -z "$MODEL_NAME" ]; then
    echo "ERROR: backend returned no models." >&2
    exit 1
fi
echo "Using model: $MODEL_NAME"

echo "[3] Generating CPU/GPU test images..."
generate_images

SMALL_SIZE="$(stat --format=%s "$SMALL_IMAGE" 2>/dev/null || stat -f%z "$SMALL_IMAGE")"
LARGE_SIZE="$(stat --format=%s "$LARGE_IMAGE" 2>/dev/null || stat -f%z "$LARGE_IMAGE")"
echo "CPU image size: $SMALL_SIZE bytes"
echo "GPU image size: $LARGE_SIZE bytes"

FAIL=0
test_pool "CPU worker" "ai-worker-cpu" "ai-worker-cpu" "$SMALL_IMAGE" "$CPU_CONCURRENCY" "CPU" || FAIL=1
test_pool "GPU worker" "ai-worker-gpu" "ai-worker-gpu" "$LARGE_IMAGE" "$GPU_CONCURRENCY" "GPU" || FAIL=1

echo ""
echo "=== Final state ==="
print_scaling_state

echo ""
echo "=== Automatic verdict ==="
printf "%-28s %s\n" "CPU routing:" "$CPU_ROUTING_RESULT"
printf "%-28s %s\n" "GPU routing:" "$GPU_ROUTING_RESULT"
printf "%-28s %s\n" "CPU HPA scaling:" "$CPU_SCALING_RESULT"
printf "%-28s %s\n" "GPU HPA scaling:" "$GPU_SCALING_RESULT"
printf "%-28s %s\n" "CPU unique worker pods:" "$CPU_UNIQUE_WORKERS"
printf "%-28s %s\n" "GPU unique worker pods:" "$GPU_UNIQUE_WORKERS"

if [ "$FAIL" -eq 0 ]; then
    echo "PASS: both CPU and GPU worker pools demonstrated HPA scaling."
else
    echo "FAIL: one or more worker pools did not scale. Increase duration/concurrency and confirm CPU targets are above thresholds." >&2
    exit 1
fi
