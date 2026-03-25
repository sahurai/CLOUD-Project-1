# Go L7 Resource-Aware Load Balancer

A lightweight reverse proxy written in Go that routes incoming requests to CPU or GPU AI worker nodes based on the `Content-Length` of the request.

## Routing Logic

| Endpoint | Rule | Target |
|---|---|---|
| `POST /predict/` | `Content-Length >= SIZE_THRESHOLD` | GPU worker |
| `POST /predict/` | `Content-Length < SIZE_THRESHOLD` | CPU worker |
| `GET /models/` | Always | CPU worker |
| `GET /health` | Health check | Returns `{"status":"ok"}` |

The default threshold is **2,500,000 bytes (~2.5 MB)**. Requests with large, high-resolution fundus images are routed to the GPU-accelerated worker to prevent OOM errors on CPU nodes.

## Configuration

All settings are controlled via environment variables:

| Variable | Default | Description |
|---|---|---|
| `CPU_BACKEND` | `http://ai-worker-cpu:8001` | URL of the CPU worker |
| `GPU_BACKEND` | `http://ai-worker-gpu:8001` | URL of the GPU worker |
| `SIZE_THRESHOLD` | `2500000` | Byte threshold for GPU routing |
| `PORT` | `8080` | Port the load balancer listens on |

## Running

### Via Docker Compose

The load balancer is started automatically as part of the full stack:

```bash
docker-compose up --build
```

It is exposed on **port 8000** externally (mapped to 8080 internally).

### Standalone (for development)

```bash
cd load_balancer
go run main.go
```

### Via Kubernetes

See [`k8s/README.md`](../k8s/README.md) for deployment instructions.

## Docker Image

Multi-stage Alpine build for a minimal final image:

```bash
docker build -t glaucoma/load-balancer:latest .
```
