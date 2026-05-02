# Kubernetes Deployment

Bare-bones Kubernetes manifests to run the Glaucoma Detection ecosystem on a local minikube cluster.

## Prerequisites

- [minikube](https://minikube.sigs.k8s.io/docs/start/)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- Docker
- Model files (`.h5`/`.keras`) in `./models/`, or `models.zip` in the repository root

Model artifacts are intentionally not committed to Git. Place `models.zip` in the repository root before running `./k8s/deploy.sh`, or provide `.keras`/`.h5` files directly in `./models/`.

If `kubectl` is not installed, minikube can provide it:

```bash
alias kubectl="minikube kubectl --"
kubectl get nodes
```

## Quick Start

```bash
# 1. Start minikube
minikube start --driver=docker
minikube addons enable metrics-server

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
| `ai-worker-hpa.yaml` | HorizontalPodAutoscaler | Scales CPU/GPU workers based on CPU pressure |

## Useful Commands

```bash
# Check pod status
kubectl -n glaucoma get pods

# Check autoscaling status
kubectl -n glaucoma get hpa

# Verify Metrics Server is ready for HPA
kubectl top nodes

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

# Send multiple file sizes and print recent load-balancer logs
./test_routing_with_logs.sh http://localhost:8000 kubernetes
```

## Scalability Test

Keep a port-forward open:

```bash
kubectl -n glaucoma port-forward svc/load-balancer 8000:8080
```

Run sustained load in another terminal:

```bash
./test_scalability.sh http://localhost:8000 300 12 cpu
./test_scalability.sh http://localhost:8000 300 8 gpu
```

Or run the full CPU + GPU proof:

```bash
./test_scalability_e2e.sh http://localhost:8000 360 20 10
```

The script prints a final PASS/FAIL verdict for CPU routing, GPU routing, CPU HPA scaling, GPU HPA scaling, and unique worker pods.

For local minikube HPA demos, the worker manifests set `LOAD_TEST_CPU_BURN_SECONDS=0.8`. This adds controlled CPU work to inference requests so the CPU-based HPA has measurable load. Set it to `0` for normal inference-only runs.

Expected successful verdict:

```text
CPU routing:                 PASS
GPU routing:                 PASS
CPU HPA scaling:             PASS
GPU HPA scaling:             PASS
PASS: both CPU and GPU worker pools demonstrated HPA scaling.
```

Watch HPA and pods:

```bash
kubectl -n glaucoma get hpa -w
kubectl -n glaucoma get pods -w
```

## Troubleshooting

If minikube fails because `/var` is full:

```bash
df -h /var
docker system prune -a --volumes
docker builder prune -a
minikube delete
minikube start --driver=docker --cpus=2 --memory=6000
```

If Metrics Server is not ready:

```bash
kubectl -n kube-system get pods
kubectl top nodes
minikube addons disable metrics-server
minikube addons enable metrics-server
```

If Docker build cannot resolve PyPI during `pip install`, configure Docker DNS or build images manually with host networking:

```bash
eval $(minikube docker-env)
docker build --network=host -t glaucoma/ai-worker:latest ./cloud_ai_worker
docker build --network=host -t glaucoma/frontend:latest ./cloud_frontend_app
docker build --network=host -t glaucoma/load-balancer:latest ./load_balancer
./k8s/deploy.sh apply
```
