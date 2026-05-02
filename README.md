# Glaucoma Detection Cloud Ecosystem

**Project Authors:** Ilia Sukhina, Yevhenii Severin & Adam Partl

## Project Overview
The primary objective of this project is to provide a scalable, cloud-native solution for the early diagnosis of glaucoma. By leveraging Deep Learning (CNN) and high-performance cloud infrastructure, the system enables medical professionals to perform rapid screenings using retinal fundus photographs.

## System Architecture
The application is built using a **microservices architecture**, designed for high availability and elastic scaling within a **Kubernetes** environment.

### Core Microservices
| Component | Technology | Role |
| :--- | :--- | :--- |
| **Frontend** | Streamlit | Web-based diagnostic dashboard for medical personnel. |
| **L7 Load Balancer** | Go | Resource-aware reverse proxy that routes traffic based on image size. See [`load_balancer/README.md`](load_balancer/README.md). |
| **AI Worker (CPU)** | FastAPI / TF | Processes standard images on high-throughput, cost-effective nodes. |
| **AI Worker (GPU)** | FastAPI / TF | Handles high-resolution images on nodes with hardware acceleration. |

## Repository Structure
```text
.
├── docker-compose.yml         # Local orchestration for all services
├── prepare_models.sh          # Extracts models.zip into ./models
├── test_load_balancer.sh      # Automated routing verification script
├── test_routing_with_logs.sh  # Sends several file sizes and prints routing logs
├── test_scalability.sh        # Single-pool HPA load test
├── test_scalability_e2e.sh    # CPU + GPU routing and HPA proof
│
├── load_balancer/             # Go L7 Resource-Aware Load Balancer
│   ├── main.go                # Reverse proxy with Content-Length routing
│   ├── Dockerfile             # Multi-stage Alpine build
│   └── README.md              # Load balancer documentation
│
├── k8s/                       # Kubernetes manifests (minikube)
│   ├── deploy.sh              # Build & deploy helper script
│   ├── namespace.yaml
│   ├── models-pv.yaml         # PersistentVolume for model files
│   ├── ai-worker-cpu.yaml
│   ├── ai-worker-gpu.yaml
│   ├── load-balancer.yaml
│   ├── frontend.yaml
│   ├── ai-worker-hpa.yaml      # Horizontal autoscaling for CPU/GPU workers
│   └── README.md              # K8s deployment guide
│
├── cloud_frontend_app/        # Streamlit Web Application
│   ├── app.py                 # UI logic & API client
│   ├── Dockerfile
│   └── requirements.txt
│
├── cloud_ai_worker/           # AI Inference Microservice
│   ├── main.py                # FastAPI endpoints & CNN logic
│   ├── Dockerfile
│   └── requirements.txt
│
└── models/                    # (git-ignored) AI model weights (.h5/.keras)
```

## Resource-Aware Routing Logic
To ensure system stability and prevent **Out-of-Memory (OOM)** errors, the Go load balancer inspects the `Content-Length` header of each request:

* **Small/Standard Files** (< 2.5 MB): Routed to the CPU worker.
* **Large/High-Res Files** (>= 2.5 MB): Routed to the GPU worker.
* **Model listing** (`/models/`): Always routed to the CPU worker.

The threshold is configurable via the `SIZE_THRESHOLD` environment variable. See [`load_balancer/README.md`](load_balancer/README.md) for full configuration details.

## Getting Started

### Prerequisites
- Docker & Docker Compose
- minikube and kubectl for Kubernetes deployment
- Model files (`.h5`/`.keras`) placed in `./models/`, or `models.zip` in the project root

### Option 1: Docker Compose (local development)

```bash
./prepare_models.sh
docker compose up --build
```

- Frontend: `http://localhost:8501`
- Load Balancer API: `http://localhost:8000`

### Option 2: Kubernetes (minikube)

```bash
minikube start --driver=docker
minikube addons enable metrics-server
./k8s/deploy.sh
minikube service frontend -n glaucoma --url
```

See [`k8s/README.md`](k8s/README.md) for detailed instructions and useful commands.

If `kubectl` is not installed, use minikube's bundled kubectl:

```bash
alias kubectl="minikube kubectl --"
kubectl get nodes
```

## Testing

### Unit Tests

The project includes comprehensive unit tests for all components:

#### AI Worker (Python/FastAPI)
```bash
cd cloud_ai_worker
pip install -r requirements.txt
python -m pytest tests/ -v
```

Tests cover:
- Model listing endpoint (`/models/`)
- Prediction endpoint (`/predict/`)
- Image preprocessing
- Error handling

#### Load Balancer (Go)
```bash
cd load_balancer
go test -v ./...
```

Tests cover:
- Routing threshold logic
- Health endpoint
- Environment variable handling
- Performance benchmarks

#### Run All Tests
```bash
./test_all.sh
```

This script runs unit tests for all components and integration tests if Kubernetes is available.

### Integration Testing

## Testing the Load Balancer

The included test script generates a small (~500 KB) and large (~3.5 MB) image and sends them to `/predict/`, verifying that each is routed to the correct worker node.

### Against Docker Compose

```bash
docker compose up --build -d
./test_load_balancer.sh
```

### Against Kubernetes

```bash
# Forward the load balancer service to localhost:8000
kubectl -n glaucoma port-forward svc/load-balancer 8000:8080 &

# Run the test script
./test_load_balancer.sh http://localhost:8000
```

To send several different image sizes and print the matching load-balancer logs:

```bash
./test_routing_with_logs.sh http://localhost:8000 kubernetes
```

For Docker Compose:

```bash
./test_routing_with_logs.sh http://localhost:8000 compose
```

### Scalability / HPA Test

After deploying to Kubernetes, keep a port-forward open:

```bash
kubectl -n glaucoma port-forward svc/load-balancer 8000:8080
```

In another terminal, run sustained load against one worker pool:

```bash
# CPU worker scaling test: 5 minutes, 12 parallel requests
./test_scalability.sh http://localhost:8000 300 12 cpu

# GPU-profile worker scaling test
./test_scalability.sh http://localhost:8000 300 8 gpu
```

Watch scaling in a third terminal:

```bash
kubectl -n glaucoma get hpa -w
kubectl -n glaucoma get pods -w
```

To prove both worker pools scale in one run:

```bash
./test_scalability_e2e.sh http://localhost:8000 360 20 10
```

This test sends CPU-routed and GPU-routed traffic, monitors both HPAs, and prints an automatic verdict for CPU routing, GPU routing, CPU HPA scaling, GPU HPA scaling, and unique worker pods.

In local minikube, HPA is measured with CPU metrics. The CPU and GPU-profile deployments therefore set `LOAD_TEST_CPU_BURN_SECONDS=0.8` to create measurable CPU pressure during load tests. Set it to `0` in the worker manifests for normal inference-only runs.

Expected successful verdict:

```text
CPU routing:                 PASS
GPU routing:                 PASS
CPU HPA scaling:             PASS
GPU HPA scaling:             PASS
CPU unique worker pods:      2
GPU unique worker pods:      2
PASS: both CPU and GPU worker pools demonstrated HPA scaling.
```

This is a cloud infrastructure test, not a medical accuracy test. It proves that the load balancer routes light and heavy inference traffic to different worker pools and that Kubernetes HPA adds replicas under sustained load.

### Manual Verification via Logs

While using the frontend, open a separate terminal and tail the logs to observe routing decisions in real time:

```bash
# Docker Compose
docker compose logs -f load-balancer

# Kubernetes
kubectl -n glaucoma logs -f deployment/load-balancer
```

Each request logs the method, path, byte size, and routing target:
```
[route] POST /predict/ (124987 bytes) -> CPU
[route] POST /predict/ (3500000 bytes) -> GPU
[route] GET /models/ -> CPU
```

## Model Storage Policy
To maintain a lightweight repository, large model weight files are excluded from Git. This includes `models/` and `models.zip`, because trained model artifacts are too large for normal Git hosting. For local demos, place `models.zip` in the project root and run `./prepare_models.sh`; for production, deliver the model artifacts via **Persistent Volumes (PV)**, object storage, or Git LFS.

## Kubernetes Scalability
The cluster manifests include HorizontalPodAutoscalers for both AI worker pools.

```bash
kubectl -n glaucoma get hpa
kubectl -n glaucoma describe hpa ai-worker-cpu
```

The CPU worker scales from 1 to 5 pods at 70% CPU utilization. The GPU-profile worker scales from 1 to 3 pods at 75% CPU utilization. Metrics require Kubernetes Metrics Server; in minikube, enable it with `minikube addons enable metrics-server`.

## Troubleshooting

If minikube fails with Docker storage errors:

```bash
df -h /var
docker system df
docker system prune -a --volumes
docker builder prune -a
minikube delete
minikube start --driver=docker --cpus=2 --memory=6000
```

If `metrics-server` is `0/1` or `kubectl top nodes` says `Metrics API not available`, wait 1-2 minutes and check again:

```bash
kubectl -n kube-system get pods
kubectl top nodes
```

If it still fails:

```bash
minikube addons disable metrics-server
minikube addons enable metrics-server
kubectl -n kube-system logs -l k8s-app=metrics-server
```

If Docker build fails during `pip install` with `Temporary failure in name resolution`, fix Docker DNS or build with host networking:

```bash
sudo mkdir -p /etc/docker
sudo tee /etc/docker/daemon.json >/dev/null <<'EOF'
{
  "dns": ["8.8.8.8", "1.1.1.1"]
}
EOF
sudo systemctl restart docker
docker run --rm busybox nslookup pypi.org
```

Then rebuild:

```bash
eval $(minikube docker-env)
docker build --network=host -t glaucoma/ai-worker:latest ./cloud_ai_worker
docker build --network=host -t glaucoma/frontend:latest ./cloud_frontend_app
docker build --network=host -t glaucoma/load-balancer:latest ./load_balancer
./k8s/deploy.sh apply
```
