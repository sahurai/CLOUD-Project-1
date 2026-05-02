#!/usr/bin/env bash
#
# Kubernetes scalability test for the Glaucoma Detection stack.
#
# Usage:
#   ./test_scalability.sh [BASE_URL] [DURATION_SECONDS] [CONCURRENCY] [MODE]
#
# Examples:
#   ./test_scalability.sh http://localhost:8000 300 12 cpu
#   ./test_scalability.sh http://localhost:8000 300 8 gpu
#   ./test_scalability.sh http://localhost:8000 300 16 both
#
# MODE:
#   cpu  - sends small images, should load ai-worker-cpu
#   gpu  - sends large images, should load ai-worker-gpu
#   both - alternates small/large requests
set -euo pipefail

BASE_URL="${1:-http://localhost:8000}"
DURATION_SECONDS="${2:-300}"
CONCURRENCY="${3:-12}"
MODE="${4:-cpu}"

PREDICT_URL="${BASE_URL}/predict/"
MODELS_URL="${BASE_URL}/models/"
TMP_DIR="$(mktemp -d)"
SMALL_IMAGE="$TMP_DIR/scale_small.png"
LARGE_IMAGE="$TMP_DIR/scale_large.png"
RESULTS_FILE="$TMP_DIR/results.tsv"
MONITOR_PID=""

cleanup() {
    if [ -n "$MONITOR_PID" ]; then
        kill "$MONITOR_PID" 2>/dev/null || true
    fi
    rm -rf "$TMP_DIR"
}
trap cleanup EXIT

require_command() {
    if ! command -v "$1" >/dev/null 2>&1; then
        echo "ERROR: required command not found: $1" >&2
        exit 1
    fi
}

require_command curl
require_command python3

case "$MODE" in
    cpu|gpu|both) ;;
    *)
        echo "ERROR: MODE must be one of: cpu, gpu, both" >&2
        exit 1
        ;;
esac

echo "=== Kubernetes Scalability Test ==="
echo "Target:      $PREDICT_URL"
echo "Duration:    ${DURATION_SECONDS}s"
echo "Concurrency: $CONCURRENCY"
echo "Mode:        $MODE"
echo ""

echo "[0] Checking backend and selecting model..."
if ! curl -sf "$MODELS_URL" >/dev/null; then
    echo "ERROR: Cannot reach $MODELS_URL"
    echo "For Kubernetes, run this in another terminal first:"
    echo "  kubectl -n glaucoma port-forward svc/load-balancer 8000:8080"
    exit 1
fi

MODEL_NAME="$(curl -sf "$MODELS_URL" | python3 -c "import sys,json; m=json.load(sys.stdin).get('models',[]); print(m[0] if m else '')")"
if [ -z "$MODEL_NAME" ]; then
    echo "ERROR: No models returned by $MODELS_URL" >&2
    exit 1
fi
echo "Using model: $MODEL_NAME"
echo ""

echo "[1] Creating test images..."
python3 - "$SMALL_IMAGE" "$LARGE_IMAGE" <<'PY'
import sys
from PIL import Image
import numpy as np

small_path, large_path = sys.argv[1], sys.argv[2]
Image.new("RGB", (400, 400), color=(128, 200, 50)).save(small_path)

arr = np.random.randint(0, 256, (1200, 1200, 3), dtype=np.uint8)
Image.fromarray(arr).save(large_path)
PY

SMALL_SIZE="$(stat --format=%s "$SMALL_IMAGE" 2>/dev/null || stat -f%z "$SMALL_IMAGE")"
LARGE_SIZE="$(stat --format=%s "$LARGE_IMAGE" 2>/dev/null || stat -f%z "$LARGE_IMAGE")"
echo "Small image: $SMALL_SIZE bytes -> CPU worker"
echo "Large image: $LARGE_SIZE bytes -> GPU worker"
echo ""

monitor_k8s() {
    if ! command -v kubectl >/dev/null 2>&1; then
        return 0
    fi

    while true; do
        echo ""
        echo "----- $(date '+%H:%M:%S') Kubernetes scaling status -----"
        kubectl -n glaucoma get hpa 2>/dev/null || true
        kubectl -n glaucoma get pods -l 'app in (ai-worker-cpu,ai-worker-gpu)' 2>/dev/null || true
        sleep 15
    done
}

send_one() {
    request_id="$1"
    image_path="$SMALL_IMAGE"

    if [ "$MODE" = "gpu" ]; then
        image_path="$LARGE_IMAGE"
    elif [ "$MODE" = "both" ]; then
        if [ $((request_id % 2)) -eq 0 ]; then
            image_path="$LARGE_IMAGE"
        fi
    fi

    response="$(curl -s --max-time 180 -w '\t%{http_code}\t%{time_total}' \
        -X POST "$PREDICT_URL" \
        -F "file=@${image_path}" \
        -F "model_name=${MODEL_NAME}" || true)"

    python3 - "$response" <<'PY'
import json
import sys

raw = sys.argv[1]
try:
    body, status, elapsed = raw.rsplit("\t", 2)
    payload = json.loads(body) if body else {}
    print("\t".join([
        status,
        elapsed,
        payload.get("status", "no-json"),
        payload.get("node_type", "unknown"),
        payload.get("worker_id", "unknown"),
    ]))
except Exception:
    print("000\t0\tparse-error\tunknown\tunknown")
PY
}

export BASE_URL PREDICT_URL MODEL_NAME MODE SMALL_IMAGE LARGE_IMAGE
export -f send_one

echo "[2] Starting load. Watch for HPA to increase pod replicas..."
monitor_k8s &
MONITOR_PID="$!"

START_TIME="$(date +%s)"
REQUEST_ID=0
: > "$RESULTS_FILE"

while [ $(( $(date +%s) - START_TIME )) -lt "$DURATION_SECONDS" ]; do
    batch_pids=""
    i=0
    while [ "$i" -lt "$CONCURRENCY" ]; do
        REQUEST_ID=$((REQUEST_ID + 1))
        bash -c "send_one '$REQUEST_ID'" >> "$RESULTS_FILE" &
        batch_pids="$batch_pids $!"
        i=$((i + 1))
    done

    for pid in $batch_pids; do
        wait "$pid" || true
    done
done

kill "$MONITOR_PID" 2>/dev/null || true
MONITOR_PID=""

echo ""
echo "[3] Results summary"
python3 - "$RESULTS_FILE" <<'PY'
import collections
import statistics
import sys

rows = []
with open(sys.argv[1], "r", encoding="utf-8") as f:
    for line in f:
        parts = line.rstrip("\n").split("\t")
        if len(parts) == 5:
            rows.append(parts)

total = len(rows)
statuses = collections.Counter(row[0] for row in rows)
nodes = collections.Counter(row[3] for row in rows)
workers = collections.Counter(row[4] for row in rows)
latencies = [float(row[1]) for row in rows if row[0] == "200"]

print(f"Total requests: {total}")
print("HTTP statuses:", dict(statuses))
print("Node types:", dict(nodes))
print(f"Unique workers hit: {len([w for w in workers if w != 'unknown'])}")
for worker, count in workers.most_common():
    print(f"  {worker}: {count}")

if latencies:
    latencies.sort()
    p50 = statistics.median(latencies)
    p95 = latencies[int((len(latencies) - 1) * 0.95)]
    print(f"Latency p50: {p50:.3f}s")
    print(f"Latency p95: {p95:.3f}s")
PY

echo ""
echo "Final Kubernetes state:"
if command -v kubectl >/dev/null 2>&1; then
    kubectl -n glaucoma get hpa 2>/dev/null || true
    kubectl -n glaucoma get pods -l 'app in (ai-worker-cpu,ai-worker-gpu)' 2>/dev/null || true
else
    echo "kubectl not found, skipped Kubernetes status."
fi
