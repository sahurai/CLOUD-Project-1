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
├── test_load_balancer.sh      # Automated routing verification script
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
- Model files (`.h5`/`.keras`) placed in `./models/`

### Option 1: Docker Compose (local development)

```bash
docker-compose up --build
```

- Frontend: `http://localhost:8501`
- Load Balancer API: `http://localhost:8000`

### Option 2: Kubernetes (minikube)

```bash
minikube start --driver=docker
./k8s/deploy.sh
minikube service frontend -n glaucoma --url
```

See [`k8s/README.md`](k8s/README.md) for detailed instructions and useful commands.

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
docker-compose up --build -d
./test_load_balancer.sh
```

### Against Kubernetes

```bash
# Forward the load balancer service to localhost:8000
kubectl -n glaucoma port-forward svc/load-balancer 8000:8080 &

# Run the test script
./test_load_balancer.sh http://localhost:8000
```

### Manual Verification via Logs

While using the frontend, open a separate terminal and tail the logs to observe routing decisions in real time:

```bash
# Docker Compose
docker-compose logs -f load-balancer

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
To maintain a lightweight repository, large model weight files are excluded from Git. In production, these are delivered to the AI Workers via **Persistent Volumes (PV)** or cloud-based object storage during the container initialization phase.
