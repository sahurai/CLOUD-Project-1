# Glaucoma Detection AI: Frontend Application

**Project Authors:** Ilia Sukhina, Yevhenii Severin & Adam Partl

## 📌 Overview
This repository contains the **Streamlit-based frontend** for the Cloud-based Early Glaucoma Detection system. The application serves as a diagnostic dashboard where medical professionals can upload retinal fundus images and receive instant AI-driven analysis.

The frontend is designed to be completely decoupled from the AI inference logic, acting as a client to a centralized **L7 Resource-Aware Load Balancer**. This ensures that the user interface remains responsive while the heavy computations are handled by specialized cloud nodes.

## 🚀 Key Features
* **Wide-Layout Dashboard:** Optimized for side-by-side comparison of the original medical image and the AI diagnostic report.
* **Infrastructure Transparency:** The UI displays real-time telemetry from the cloud, including which node handled the request (CPU vs. GPU) and the specific Worker/Pod ID.
* **Asynchronous Processing:** Uses Streamlit's state management to handle image uploads and API requests without freezing the interface.
* **Clinical Verdicts:** Automatically categorizes AI probability scores into "Low Risk," "Suspicious," or "High Risk" based on medical thresholds.

## 🏗️ Architecture Role
In the context of our cloud infrastructure, this application performs the following:
1. **Data Ingestion:** Collects raw fundus photographs and model preferences from the user.
2. **Unified Entry Point:** Sends all requests to a single Backend URL. It does not perform routing itself; it relies on the **Resource-Aware Load Balancer** to direct traffic based on image complexity.
3. **Observability:** Parses enhanced JSON responses from the AI Workers to show the user exactly how the cloud resources were allocated for their specific task.

## 📁 Project Structure
```text
frontend_app/
├── app.py                  # Main Streamlit application
├── requirements.txt        # Frontend dependencies (Streamlit, Requests, etc.)
├── .env                    # Environment configuration (Backend URLs)
└── README.md               # Project documentation
```

## ⚙️ Environment Configuration (.env)
The frontend requires the URL of the backend API (or Load Balancer) to function.

**Example `.env` file:**
```env
# The URL of the centralized L7 Load Balancer or Backend API
BACKEND_URL=http://127.0.0.1:8001
```

## 🛠️ Setup & Installation

### Prerequisites
* Python 3.10+
* Access to a running AI Worker microservice

### Running Locally
```bash
# Navigate to the frontend directory
cd frontend_app

# Install dependencies
pip install -r requirements.txt

# Launch the Streamlit server
streamlit run app.py
```

## 📡 API Interaction
The frontend expects the backend to support the following interface:
* `GET /models/`: To populate the model selection dropdown.
* `POST /predict/`: To send image data and receive a JSON response containing `probability`, `node_type`, and `worker_id`.