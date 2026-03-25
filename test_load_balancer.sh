#!/usr/bin/env bash
#
# Test script for the Content-Length-based load balancer.
# Generates a small (<2.5MB) and large (>2.5MB) test image,
# sends each to the /predict/ endpoint, and checks which worker handled it.
#
# Usage: ./test_load_balancer.sh [BASE_URL]
#   BASE_URL defaults to http://localhost:8000

set -euo pipefail

BASE_URL="${1:-http://localhost:8000}"
PREDICT_URL="${BASE_URL}/predict/"
MODELS_URL="${BASE_URL}/models/"
PASS=0
FAIL=0

# --- Helpers ---

cleanup() {
    rm -f /tmp/test_small.png /tmp/test_large.png
}
trap cleanup EXIT

print_result() {
    local label="$1" expected="$2" actual="$3"
    if [[ "$actual" == *"$expected"* ]]; then
        echo "  PASS - Routed to: $actual"
        PASS=$((PASS + 1))
    else
        echo "  FAIL - Expected '$expected', got: $actual"
        FAIL=$((FAIL + 1))
    fi
}

# --- Pre-flight: check that the backend is reachable ---

echo "=== Load Balancer Routing Test ==="
echo ""
echo "Target: $PREDICT_URL"
echo ""

echo "[0] Checking backend connectivity..."
if ! curl -sf "$MODELS_URL" > /dev/null 2>&1; then
    echo "  ERROR: Cannot reach $MODELS_URL. Are the services running?"
    echo "  Run: docker-compose up --build"
    exit 1
fi

# Pick the first available model
MODEL_NAME=$(curl -sf "$MODELS_URL" | python3 -c "import sys,json; m=json.load(sys.stdin).get('models',[]); print(m[0] if m else '')" 2>/dev/null)
if [[ -z "$MODEL_NAME" ]]; then
    echo "  ERROR: No models available on the backend."
    exit 1
fi
echo "  OK - Using model: $MODEL_NAME"
echo ""

# --- Generate test files ---

# Small file: 500KB (well under 2.5MB threshold) -> should go to CPU
echo "[1] Creating small test image (~500KB)..."
python3 -c "
from PIL import Image
img = Image.new('RGB', (400, 400), color=(128, 200, 50))
img.save('/tmp/test_small.png')
"
SMALL_SIZE=$(stat --format=%s /tmp/test_small.png 2>/dev/null || stat -f%z /tmp/test_small.png)
echo "  File size: $SMALL_SIZE bytes ($(( SMALL_SIZE / 1024 )) KB)"

# Large file: ~3.5MB (over 2.5MB threshold) -> should go to GPU
echo "[2] Creating large test image (~3.5MB)..."
python3 -c "
from PIL import Image
import numpy as np
# Random pixel data produces a large PNG that doesn't compress well
arr = np.random.randint(0, 256, (1200, 1200, 3), dtype=np.uint8)
img = Image.fromarray(arr)
img.save('/tmp/test_large.png')
"
LARGE_SIZE=$(stat --format=%s /tmp/test_large.png 2>/dev/null || stat -f%z /tmp/test_large.png)
echo "  File size: $LARGE_SIZE bytes ($(( LARGE_SIZE / 1024 )) KB)"
echo ""

# --- Test 1: Small file -> CPU ---

echo "[Test 1] Sending small image ($SMALL_SIZE bytes) - expecting CPU worker..."
RESPONSE_SMALL=$(curl -s --max-time 120 -X POST "$PREDICT_URL" \
    -F "file=@/tmp/test_small.png" \
    -F "model_name=$MODEL_NAME" || true)

if [[ -z "$RESPONSE_SMALL" ]]; then
    echo "  FAIL - No response from server (timeout or connection error)"
    FAIL=$((FAIL + 1))
else
    NODE_SMALL=$(echo "$RESPONSE_SMALL" | python3 -c "import sys,json; print(json.load(sys.stdin).get('node_type','UNKNOWN'))" 2>/dev/null)
    print_result "Small file" "CPU" "$NODE_SMALL"
fi
echo ""

# --- Test 2: Large file -> GPU ---

echo "[Test 2] Sending large image ($LARGE_SIZE bytes) - expecting GPU worker..."
RESPONSE_LARGE=$(curl -s --max-time 120 -X POST "$PREDICT_URL" \
    -F "file=@/tmp/test_large.png" \
    -F "model_name=$MODEL_NAME" || true)

if [[ -z "$RESPONSE_LARGE" ]]; then
    echo "  FAIL - No response from server (timeout or connection error)"
    FAIL=$((FAIL + 1))
else
    NODE_LARGE=$(echo "$RESPONSE_LARGE" | python3 -c "import sys,json; print(json.load(sys.stdin).get('node_type','UNKNOWN'))" 2>/dev/null)
    print_result "Large file" "GPU" "$NODE_LARGE"
fi
echo ""

# --- Summary ---

echo "=== Results: $PASS passed, $FAIL failed ==="
[[ $FAIL -eq 0 ]] && exit 0 || exit 1
