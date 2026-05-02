#!/usr/bin/env bash
#
# Send multiple image sizes through the load balancer and print routing logs.
#
# Usage:
#   ./test_routing_with_logs.sh [BASE_URL] [LOG_MODE]
#
# Examples:
#   ./test_routing_with_logs.sh http://localhost:8000 kubernetes
#   ./test_routing_with_logs.sh http://localhost:8000 compose
#
# LOG_MODE:
#   kubernetes - read logs from deployment/load-balancer in namespace glaucoma
#   compose    - read logs from docker compose service load-balancer
#   none       - skip log collection
set -euo pipefail

BASE_URL="${1:-http://localhost:8000}"
LOG_MODE="${2:-kubernetes}"
MODELS_URL="${BASE_URL}/models/"
PREDICT_URL="${BASE_URL}/predict/"
TMP_DIR="$(mktemp -d)"
PASS=0
FAIL=0

cleanup() {
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

echo "=== Routing Test With Logs ==="
echo "Backend:  $BASE_URL"
echo "Log mode: $LOG_MODE"
echo ""

echo "[0] Checking backend and selecting model..."
if ! curl -sf "$MODELS_URL" >/dev/null; then
    echo "ERROR: Cannot reach $MODELS_URL"
    echo "For Kubernetes, run first:"
    echo "  kubectl -n glaucoma port-forward svc/load-balancer 8000:8080"
    exit 1
fi

MODEL_NAME="$(curl -sf "$MODELS_URL" | python3 -c "import sys,json; m=json.load(sys.stdin).get('models',[]); print(m[0] if m else '')")"
if [ -z "$MODEL_NAME" ]; then
    echo "ERROR: Backend returned no models." >&2
    exit 1
fi
echo "Using model: $MODEL_NAME"
echo ""

echo "[1] Generating test images..."
python3 - "$TMP_DIR" <<'PY'
import sys
from pathlib import Path

import numpy as np
from PIL import Image

out = Path(sys.argv[1])

cases = [
    ("tiny", (224, 224), "solid"),
    ("small", (500, 500), "solid"),
    ("medium", (900, 900), "random"),
    ("large", (1400, 1400), "random"),
]

for name, size, mode in cases:
    if mode == "solid":
        img = Image.new("RGB", size, color=(88, 160, 90))
    else:
        arr = np.random.randint(0, 256, (size[1], size[0], 3), dtype=np.uint8)
        img = Image.fromarray(arr)
    img.save(out / f"{name}.png")
PY

printf "%-10s %-12s %-10s %-26s %-20s\n" "file" "bytes" "expected" "node_type" "worker_id"
printf "%-10s %-12s %-10s %-26s %-20s\n" "----" "-----" "--------" "---------" "---------"

send_case() {
    label="$1"
    image_path="$TMP_DIR/$label.png"
    size="$(stat --format=%s "$image_path" 2>/dev/null || stat -f%z "$image_path")"

    expected="CPU"
    if [ "$size" -ge 2500000 ]; then
        expected="GPU"
    fi

    response="$(curl -s --max-time 180 -X POST "$PREDICT_URL" \
        -F "file=@${image_path}" \
        -F "model_name=${MODEL_NAME}" || true)"

    parsed="$(python3 - "$response" <<'PY'
import json
import sys

try:
    payload = json.loads(sys.argv[1])
    print("\t".join([
        payload.get("status", "unknown"),
        payload.get("node_type", "unknown"),
        payload.get("worker_id", "unknown"),
    ]))
except Exception:
    print("parse-error\tunknown\tunknown")
PY
)"

    status="$(printf "%s" "$parsed" | cut -f1)"
    node_type="$(printf "%s" "$parsed" | cut -f2)"
    worker_id="$(printf "%s" "$parsed" | cut -f3)"

    printf "%-10s %-12s %-10s %-26s %-20s\n" "$label" "$size" "$expected" "$node_type" "$worker_id"

    if [ "$status" = "success" ] && printf "%s" "$node_type" | grep -q "$expected"; then
        PASS=$((PASS + 1))
    else
        FAIL=$((FAIL + 1))
    fi
}

send_case tiny
send_case small
send_case medium
send_case large

echo ""
echo "[2] Recent load balancer logs"
case "$LOG_MODE" in
    kubernetes)
        if command -v kubectl >/dev/null 2>&1; then
            kubectl -n glaucoma logs deployment/load-balancer --tail=40 || true
        else
            echo "kubectl not found. If needed, run: alias kubectl=\"minikube kubectl --\""
        fi
        ;;
    compose)
        if command -v docker >/dev/null 2>&1; then
            docker compose logs --tail=40 load-balancer || true
        else
            echo "docker not found, skipping compose logs."
        fi
        ;;
    none)
        echo "Skipped."
        ;;
    *)
        echo "Unknown LOG_MODE '$LOG_MODE'. Use: kubernetes, compose, none."
        ;;
esac

echo ""
echo "=== Results: $PASS passed, $FAIL failed ==="
[ "$FAIL" -eq 0 ]
