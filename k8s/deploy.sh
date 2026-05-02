#!/usr/bin/env bash
#
# Build images and deploy to a local Kubernetes cluster (minikube).
#
# Usage:
#   ./k8s/deploy.sh          # build + deploy
#   ./k8s/deploy.sh apply    # deploy only (skip image build)
#
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
K8S="$ROOT/k8s"

"$ROOT/prepare_models.sh"

# --- Build images inside minikube's Docker daemon ---
if [[ "${1:-}" != "apply" ]]; then
    echo "==> Configuring shell for minikube Docker daemon..."
    eval $(minikube docker-env)

    echo "==> Building images..."
    docker build -t glaucoma/ai-worker:latest  "$ROOT/cloud_ai_worker"
    docker build -t glaucoma/frontend:latest   "$ROOT/cloud_frontend_app"
    docker build -t glaucoma/load-balancer:latest "$ROOT/load_balancer"
    echo "==> Images built."
fi

# --- Copy model files into minikube node ---
echo "==> Copying models to minikube node at /data/models..."
minikube ssh -- "sudo mkdir -p /data/models"
for f in "$ROOT"/models/*; do
    [ -e "$f" ] || continue
    minikube cp "$f" "/data/models/$(basename "$f")"
done

# --- Apply manifests ---
echo "==> Applying Kubernetes manifests..."
kubectl apply -f "$K8S/namespace.yaml"
kubectl apply -f "$K8S/models-pv.yaml"
kubectl apply -f "$K8S/ai-worker-cpu.yaml"
kubectl apply -f "$K8S/ai-worker-gpu.yaml"
kubectl apply -f "$K8S/load-balancer.yaml"
kubectl apply -f "$K8S/frontend.yaml"
kubectl apply -f "$K8S/ai-worker-hpa.yaml"

echo ""
echo "==> Waiting for pods to be ready..."
kubectl -n glaucoma rollout status deployment/ai-worker-cpu  --timeout=120s
kubectl -n glaucoma rollout status deployment/ai-worker-gpu  --timeout=120s
kubectl -n glaucoma rollout status deployment/load-balancer   --timeout=120s
kubectl -n glaucoma rollout status deployment/frontend        --timeout=120s

echo ""
echo "==> Autoscalers:"
kubectl -n glaucoma get hpa

echo ""
echo "==> All deployments ready. Access the frontend:"
minikube service frontend -n glaucoma --url
