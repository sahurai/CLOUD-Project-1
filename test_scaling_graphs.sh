#!/usr/bin/env bash
#
# Runs the existing CPU+GPU scalability E2E test with a metrics poller in the
# background, then renders two PNGs into docs/:
#   docs/scaling_replicas.png — pod replica count over time (CPU + GPU)
#   docs/scaling_cpu.png      — HPA-reported CPU% vs target (CPU + GPU)
#
# Usage:
#   ./test_scaling_graphs.sh [BASE_URL] [DURATION_SECONDS] [CPU_CONCURRENCY] [GPU_CONCURRENCY] [COOLDOWN_SECONDS]
#
# Example (default cooldown 60s after the test finishes so the graph captures
# the start of HPA scale-down too):
#   ./test_scaling_graphs.sh http://localhost:8000 360 20 10 60
#
# Prereqs:
#   - The cluster is up and a port-forward to svc/load-balancer is active on 8000
#     (test_scalability_e2e.sh starts one automatically if not).
#   - jq, python3 with the scripts/plot-venv venv set up (matplotlib).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "$0")" && pwd)"
BASE_URL="${1:-http://localhost:8000}"
DURATION_SECONDS="${2:-360}"
CPU_CONCURRENCY="${3:-20}"
GPU_CONCURRENCY="${4:-10}"
COOLDOWN_SECONDS="${5:-60}"
SAMPLE_INTERVAL="${SAMPLE_INTERVAL:-5}"

OUT_DIR="$ROOT_DIR/docs"
DATA_DIR="$ROOT_DIR/docs/data"
mkdir -p "$DATA_DIR"
CSV="$DATA_DIR/scaling_metrics.csv"

POLLER_PID=""
cleanup() {
    if [ -n "$POLLER_PID" ] && kill -0 "$POLLER_PID" 2>/dev/null; then
        kill "$POLLER_PID" 2>/dev/null || true
        wait "$POLLER_PID" 2>/dev/null || true
    fi
}
trap cleanup EXIT

PLOT_VENV="$ROOT_DIR/scripts/plot-venv"
if [ ! -x "$PLOT_VENV/bin/python" ]; then
    echo "Creating plot venv at $PLOT_VENV"
    python3 -m venv "$PLOT_VENV"
    "$PLOT_VENV/bin/pip" install --quiet --disable-pip-version-check matplotlib
fi

echo "=== Scaling graph run ==="
echo "Base URL:      $BASE_URL"
echo "Duration:      ${DURATION_SECONDS}s"
echo "Concurrency:   cpu=$CPU_CONCURRENCY gpu=$GPU_CONCURRENCY"
echo "Sample every:  ${SAMPLE_INTERVAL}s"
echo "Cooldown:      ${COOLDOWN_SECONDS}s"
echo "CSV:           $CSV"
echo "Plots out dir: $OUT_DIR"
echo ""

echo "[1/3] Starting HPA poller in background..."
"$ROOT_DIR/scripts/collect_hpa_metrics.sh" "$CSV" "$SAMPLE_INTERVAL" glaucoma &
POLLER_PID="$!"
sleep 2

echo "[2/3] Running test_scalability_e2e.sh..."
set +e
"$ROOT_DIR/test_scalability_e2e.sh" "$BASE_URL" "$DURATION_SECONDS" "$CPU_CONCURRENCY" "$GPU_CONCURRENCY"
E2E_RC=$?
set -e
echo ""
echo "test_scalability_e2e.sh exit code: $E2E_RC"

if [ "$COOLDOWN_SECONDS" -gt 0 ]; then
    echo "Continuing to sample for ${COOLDOWN_SECONDS}s of cooldown..."
    sleep "$COOLDOWN_SECONDS"
fi

echo "[3/3] Stopping poller and rendering plots..."
cleanup
POLLER_PID=""

ROW_COUNT="$(wc -l < "$CSV")"
echo "Rows collected (incl. header): $ROW_COUNT"

"$PLOT_VENV/bin/python" "$ROOT_DIR/scripts/plot_scaling.py" "$CSV" --out-dir "$OUT_DIR" --prefix scaling

echo ""
echo "Done. Open:"
echo "  $OUT_DIR/scaling_replicas.png"
echo "  $OUT_DIR/scaling_cpu.png"
exit "$E2E_RC"
