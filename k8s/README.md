# Kubernetes Deployment

Bare-bones Kubernetes manifests to run the Glaucoma Detection ecosystem on a local minikube cluster.

## Prerequisites

- [minikube](https://minikube.sigs.k8s.io/docs/start/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- Docker
- Model files (`.h5`/`.keras`) in `./models/`

## Quick Start

```bash
# 1. Start minikube
minikube start --driver=docker

# 2. Build images & deploy (runs everything)
./k8s/deploy.sh

# 3. Get the frontend URL
minikube service frontend -n glaucoma --url
```

To redeploy after manifest changes (skips image rebuild):

```bash
./k8s/deploy.sh apply
```

## What Gets Created

All resources are in the `glaucoma` namespace:

| Manifest | Resources | Purpose |
|---|---|---|
| `namespace.yaml` | Namespace | Isolates all project resources |
| `models-pv.yaml` | PersistentVolume + PVC | Mounts model files from host into worker pods |
| `ai-worker-cpu.yaml` | Deployment + Service | CPU inference worker (port 8001) |
| `ai-worker-gpu.yaml` | Deployment + Service | GPU inference worker (port 8001) |
| `load-balancer.yaml` | Deployment + Service | Go reverse proxy (port 8080) |
| `frontend.yaml` | Deployment + Service (NodePort) | Streamlit dashboard (NodePort 30501) |

## Useful Commands

```bash
# Check pod status
kubectl -n glaucoma get pods

# View load balancer routing logs
kubectl -n glaucoma logs -f deployment/load-balancer

# View worker logs side-by-side
kubectl -n glaucoma logs -f --prefix deployment/ai-worker-cpu &
kubectl -n glaucoma logs -f --prefix deployment/ai-worker-gpu &
kubectl -n glaucoma logs -f --prefix deployment/load-balancer

# Restart a single service after image rebuild
kubectl -n glaucoma rollout restart deployment/load-balancer

# Tear down everything
kubectl delete namespace glaucoma
kubectl delete pv models-pv
```

## Testing

See [Testing the Load Balancer](#) in the root README, or run:

```bash
# Get the load balancer URL via port-forward or NodePort
kubectl -n glaucoma port-forward svc/load-balancer 8000:8080 &

# Run the test script against the forwarded port
./test_load_balancer.sh http://localhost:8000
```
