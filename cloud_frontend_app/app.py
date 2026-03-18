import os

import requests
import streamlit as st
from dotenv import load_dotenv

# --- 1. CONFIGURATION AND CLOUD ENVIRONMENT ---
# Load environment variables from a .env file for security and flexibility.
load_dotenv()

# The frontend is decoupled from specific worker nodes.
# It communicates with a single entry point, representing the Load Balancer or API Gateway.
BACKEND_URL = os.getenv("BACKEND_URL", "http://127.0.0.1:8001")
MODELS_ENDPOINT = f"{BACKEND_URL}/models/"
PREDICT_ENDPOINT = f"{BACKEND_URL}/predict/"

# --- 2. GLOBAL PAGE SETTINGS ---
st.set_page_config(
    page_title="Glaucoma Vision AI",
    page_icon="👁️",
    layout="wide",  # Optimized for side-by-side comparison of fundus images and AI results.
)


# --- 3. API COMMUNICATION UTILITIES ---
@st.cache_data(ttl=60)
def fetch_models():
    """
    Fetch the list of available CNN models from the backend microservice.
    Caching prevents redundant network requests during Streamlit session refreshes.
    """
    try:
        response = requests.get(MODELS_ENDPOINT, timeout=3)
        if response.status_code == 200:
            return response.json().get("models", [])
        return []
    except Exception:
        return []


# --- 4. SIDEBAR: CONTROL AND CONFIGURATION ---
with st.sidebar:
    st.title("Diagnostic Settings")
    st.markdown("Configure analysis parameters and upload retinal photographs.")

    # The uploader accepts raw image data for processing by the cloud infrastructure.
    uploaded_file = st.file_uploader(
        "Upload Retina Image",
        type=["jpg", "jpeg", "png"],
        help="Please provide a high-quality fundus photograph.",
    )

    available_models = fetch_models()
    if not available_models:
        st.error("Infrastructure Offline: Backend API is currently unreachable.")
        selected_model = None
    else:
        selected_model = st.selectbox("Select Inference Model", available_models)

    # Analysis trigger; disabled if no file is present to ensure data integrity.
    analyze_btn = st.button(
        "Start Analysis", type="primary", disabled=not uploaded_file
    )

    st.caption("**CLOUD Project 1** by Ilia Sukhina, Yevhenii Severin & Adam Partl")

# --- 5. MAIN INTERFACE: DIAGNOSTIC REPORT ---
st.header("👁️ Glaucoma Analysis Report")

if not uploaded_file:
    st.info(
        "System Ready. Please upload a retinal image in the sidebar to begin diagnostics."
    )
else:
    # A split-view layout: The subject image on the left, AI metrics on the right.
    col_img, col_info = st.columns([1, 1], gap="large")

    with col_img:
        st.subheader("Subject: Fundus Image")
        # Legacy-compatible parameter 'use_column_width' ensures stability on older Streamlit versions.
        st.image(uploaded_file, use_column_width=True)

    with col_info:
        st.subheader("Analysis Results")

        if analyze_btn:
            # Prepare the raw image bytes for transmission over the network[cite: 8].
            file_bytes = uploaded_file.getvalue()

            with st.spinner("Processing through the AI Pipeline..."):
                try:
                    # Request is sent to a single endpoint.
                    # The external Resource-Aware Load Balancer performs routing based on data complexity[cite: 23, 26].
                    files = {"file": (uploaded_file.name, file_bytes, "image/jpeg")}
                    data = {"model_name": selected_model}

                    response = requests.post(
                        PREDICT_ENDPOINT, files=files, data=data, timeout=30
                    )
                    response.raise_for_status()
                    res = response.json()

                    if res.get("status") == "success":
                        prob = res["probability"]

                        # Technical metadata provided by the worker node for infrastructure transparency[cite: 22].
                        node_info = res.get("node_type", "Unknown")
                        worker_id = res.get("worker_id", "N/A")
                        latency = res.get("execution_time", 0.0)

                        # Primary probability result and progress indicator.
                        st.metric(label="Glaucoma Probability", value=f"{prob:.2f}%")
                        st.progress(int(prob))

                        st.markdown("---")

                        # Clinical verdict based on CNN classification thresholds[cite: 9, 10].
                        if prob < 40:
                            st.success("### Verdict: Low Risk ✅")
                            st.write("No significant glaucoma indicators detected.")
                        elif prob < 60:
                            st.warning("### Verdict: Suspicious ⚠️")
                            st.write(
                                "Results are borderline. Clinical correlation is advised."
                            )
                        else:
                            st.error("### Verdict: High Risk 🚨")
                            st.write(
                                "Significant indicators of glaucoma detected by the model."
                            )

                        # Infrastructure log verifying the load balancer's routing performance[cite: 29].
                        st.caption(
                            f"Model: {selected_model} | Node: {node_info} | Latency: {latency}s | Worker ID: {worker_id}"
                        )
                    else:
                        st.error(f"Inference Error: {res.get('message')}")

                except Exception as e:
                    st.error(
                        f"Network Error: Connection to Inference Server failed ({e})"
                    )
        else:
            st.write("Image verified. Use the sidebar button to initiate analysis.")
