# 👁️ Glaucoma Detection Cloud Ecosystem

**Project Authors:** Ilia Sukhina, Yevhenii Severin & Adam Partl  

## 📌 Project Overview
The primary objective of this project is to provide a scalable, cloud-native solution for the early diagnosis of glaucoma. By leveraging Deep Learning (CNN) and high-performance cloud infrastructure, the system enables medical professionals to perform rapid screenings using retinal fundus photographs.

## 🏗️ System Architecture
The application is built using a **microservices architecture**, designed for high availability and elastic scaling within a **Kubernetes** environment.

### Core Microservices:
| Component | Technology | Role |
| :--- | :--- | :--- |
| **Frontend** | Streamlit | Web-based diagnostic dashboard for medical personnel. |
| **L7 Load Balancer** | Nginx | "Resource-Aware" gateway that routes traffic based on image complexity. |
| **AI Worker (CPU)** | FastAPI / TF | Processes standard images on high-throughput, cost-effective nodes. |
| **AI Worker (GPU)** | FastAPI / TF | Handles high-resolution images on nodes with hardware acceleration. |

## 📁 Repository Structure
```text
.
├── docker-compose.yml         # Local orchestration for all services
├── nginx.conf                 # L7 Smart Routing configuration
│
├── frontend_app/              # Streamlit Web Application
│   ├── app.py                 # UI Logic & API client
│   ├── Dockerfile             # Frontend container build
│   └── requirements.txt
│
└── ai_worker/                 # AI Inference Microservice
    ├── models/                # (Local) Directory for AI weights (.h5/.keras)
    ├── main.py                # FastAPI endpoints & CNN logic
    ├── Dockerfile             # Inference container build
    └── requirements.txt
```

## 🧠 Resource-Aware Routing Logic
To ensure system stability and prevent **Out-of-Memory (OOM)** errors, we implemented a custom L7 routing logic:
* **Small/Standard Files:** Directed to standard CPU nodes.
* **Large/High-Res Files:** Automatically routed to High-Performance GPU nodes.
* **Mechanism:** The Nginx Load Balancer analyzes the `Content-Length` header of incoming requests to determine the appropriate processing target.

## 📈 Cloud Features & Scalability
* **Kubernetes Orchestration:** Manages container lifecycles and provides service discovery.
* **Horizontal Pod Autoscaler (HPA):** The system is configured to launch additional AI Worker instances automatically when CPU utilization exceeds **80%**.
* **Infrastructure Telemetry:** Each diagnostic report includes metadata (Node Type, Worker ID) to verify real-time resource allocation and cluster state.

## 🛠️ Getting Started (Local Development)

### 1. Model Preparation
Place your trained Keras/TensorFlow models in the `./models/` directory. Ensure the file extensions are `.h5` or `.keras`.

### 2. Launching the Ecosystem
Run the following command from the root directory to build and start all microservices:
```bash
docker-compose up --build
```

### 3. Usage
Access the diagnostic dashboard at `http://localhost:8501`. All API calls are internally routed through the Load Balancer at `http://localhost:8000`.

## ⚠️ Model Storage Policy
To maintain a lightweight repository, large model weight files are excluded from Git. In production, these are delivered to the AI Workers via **Persistent Volumes (PV)** or cloud-based object storage during the container initialization phase.