# Cloud AI Worker: Glaucoma Detection Microservice

**Authors:** Ilia Sukhina, Yevhenii Severin, Adam Partl

## 📌 Overview
This repository contains the standalone AI inference microservice for the **Cloud Application for Early Glaucoma Detection**. 

By isolating the deep learning inference logic into this dedicated FastAPI microservice, we achieve a robust cloud-native architecture. This shift serves several critical purposes:
1. **OOM Prevention:** Isolating heavy TensorFlow/Keras models from the web server prevents Out-Of-Memory errors during mass screening.
2. **Infrastructure Observability:** The service now reports which specific node type and pod ID processed the request, allowing for real-time monitoring of our cloud resources.
3. **Independent Scalability:** Optimized for Kubernetes Horizontal Pod Autoscaler (HPA) to scale dynamically based on CPU/GPU demand.
4. **Smart Routing Target:** Acts as the backend for an L7 Resource-Aware Load Balancer, which routes high-resolution images to specialized GPU pods while keeping standard requests on CPU pods.

## 🚀 Features
* **FastAPI Framework:** High-performance ASGI framework for asynchronous request handling.
* **Infrastructure Metadata:** Returns `NODE_TYPE` and `WORKER_ID` in every prediction response to verify cloud routing logic.
* **Performance Tracking:** Includes `execution_time` metrics to monitor inference latency across different hardware configurations.
* **Lazy Loading & Caching:** Models are loaded into RAM only upon the first request and cached, drastically reducing subsequent latency.
* **TensorFlow/Keras Integration:** Supports `.h5`, `.keras`, and SavedModel formats.
* **Dynamic Model Discovery:** Automatically scans the environment to expose available models to the frontend.

## 📁 Project Structure
```text
cloud_ai_worker/
├── models/                  # Directory containing trained Keras/TF models
├── main.py                  # FastAPI application with infrastructure metadata logic
├── requirements.txt         # Python dependencies (TensorFlow, FastAPI, Pillow, etc.)
├── Dockerfile               # Container build instructions
├── .env                     # Environment variables
└── README.md                # Project documentation
```

## ⚙️ Environment Configuration (.env)
Create a `.env` file in the root directory to configure the worker's identity within the cluster.

**Example `.env` file:**
```env
# Port for the microservice
PORT=8001

# Directory for model storage
MODEL_DIR=./models

# Cloud Metadata: Identifies if this node is 'Standard CPU' or 'High-Performance GPU'
NODE_TYPE="Standard CPU"

# TensorFlow logging (2 = Errors only)
TF_CPP_MIN_LOG_LEVEL=2
```

## 🛠️ Local Development Setup

### 1. Standard Setup
```bash
python -m venv venv
source venv/bin/activate  # Windows: venv\Scripts\activate
pip install -r requirements.txt
uvicorn main:app --host 0.0.0.0 --port 8001 --reload
```

### 2. Docker Setup (Recommended)
```bash
# Build the image
docker build -t glaucoma-ai-worker .

# Run with environment variables
docker run -d -p 8001:8001 --env NODE_TYPE="Local-Dev" --name ai_worker glaucoma-ai-worker
```

## 📡 API Documentation

### `GET /models/`
Returns a list of available trained models. Used by the frontend to populate selection menus.

**Response:**
```json
{
  "status": "success",
  "models": ["resnet_v1.h5", "vgg16_final.keras"]
}
```

### `POST /predict/`
Processes a fundus image and returns the diagnostic result along with infrastructure telemetry.

**Request:** `multipart/form-data`
* `file`: Image file (JPEG/PNG).
* `model_name`: Filename of the target model (e.g., `"resnet_v1.h5"`).

**Example Response (Success):**
```json
{
  "status": "success",
  "probability": 85.43,
  "node_type": "High-Performance GPU",
  "worker_id": "ai-worker-gpu-7f9db8",
  "execution_time": 0.452
}
```

## ☁️ Cloud Deployment (Kubernetes)
This microservice is designed for stateless deployment in a K8s cluster. 

* **Horizontal Scaling:** Use HPA to scale pods when CPU/GPU usage exceeds 80%.
* **Resource-Aware Routing:** The `NODE_TYPE` variable should be set via the Kubernetes Deployment manifest using `env` fields to distinguish between CPU and GPU node pools. 
* **Worker Identification:** `worker_id` automatically captures the Pod hostname, providing a clear audit trail of which specific instance handled a medical request.