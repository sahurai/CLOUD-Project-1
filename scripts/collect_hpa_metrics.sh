#!/usr/bin/env bash
# Polls ai-worker-cpu and ai-worker-gpu HPA state at a fixed interval and
# appends one row per pool per sample to a CSV. Stops on SIGTERM/SIGINT.
#
# Usage: collect_hpa_metrics.sh <out.csv> [interval_seconds] [namespace]
set -euo pipefail

OUT_CSV="${1:?usage: $0 out.csv [interval] [namespace]}"
INTERVAL="${2:-5}"
NAMESPACE="${3:-glaucoma}"

if command -v kubectl >/dev/null 2>&1; then
    KUBECTL=(kubectl)
elif command -v minikube >/dev/null 2>&1; then
    KUBECTL=(minikube kubectl --)
else
    echo "ERROR: neither kubectl nor minikube was found." >&2
    exit 1
fi

if ! command -v jq >/dev/null 2>&1; then
    echo "ERROR: jq is required." >&2
    exit 1
fi

echo "t_seconds,pool,desired_replicas,current_replicas,cpu_pct_current,cpu_pct_target" > "$OUT_CSV"

START_EPOCH="$(date +%s)"

# Read the target utilization once — it doesn't change during a run unless
# the orchestrator patches the HPA mid-test, in which case we'll re-read it
# inside the loop anyway.
sample_once() {
    local now_epoch t_rel
    now_epoch="$(date +%s)"
    t_rel=$((now_epoch - START_EPOCH))

    local json
    if ! json="$("${KUBECTL[@]}" -n "$NAMESPACE" get hpa ai-worker-cpu ai-worker-gpu -o json 2>/dev/null)"; then
        return 0
    fi

    printf '%s' "$json" | jq -r --argjson t "$t_rel" '
        .items[] |
        [
            $t,
            (.metadata.name | sub("^ai-worker-"; "")),
            (.status.desiredReplicas // 0),
            (.status.currentReplicas // 0),
            (.status.currentMetrics[0].resource.current.averageUtilization // ""),
            (.spec.metrics[0].resource.target.averageUtilization // "")
        ] | @csv
    ' >> "$OUT_CSV"
}

trap 'exit 0' INT TERM

while true; do
    sample_once
    sleep "$INTERVAL"
done
