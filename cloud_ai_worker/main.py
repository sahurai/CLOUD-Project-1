import os
import socket
import time
from io import BytesIO

import numpy as np
from fastapi import FastAPI, File, Form, UploadFile
from PIL import Image
from tensorflow.keras.models import load_model
from tensorflow.keras.preprocessing import image

# --- 1. CLOUD INFRASTRUCTURE METADATA ---
# These constants identify the node's capacity within the cluster.
# NODE_TYPE is injected via environment variables (e.g., 'CPU' or 'GPU').
NODE_TYPE = os.getenv("NODE_TYPE", "Standard CPU")
# WORKER_ID identifies the specific Kubernetes Pod for scaling verification.
WORKER_ID = socket.gethostname()

# Initialize the FastAPI application.
# This acts as our independent AI inference microservice, decoupling the heavy
# deep learning computations from the main Django web server (the L7 Router).
app = FastAPI(
    title="Glaucoma Detection AI Worker",
    description="Microservice for processing medical fundus images and returning glaucoma probabilities.",
)

# In-memory dictionary to cache loaded Keras models.
# Why this is critical: Loading a deep learning model from disk takes time and consumes a lot of RAM.
# By caching it in memory after the first request, we ensure high throughput and low latency
# for subsequent requests, which is essential during mass screening campaigns.
MODEL_CACHE = {}

# Directory where the trained model files (.h5, .keras, or SavedModel format) are stored.
# In a production Kubernetes environment, this could be a mounted persistent volume.
MODEL_DIR = os.getenv("MODEL_DIR", "./models")

# Optional synthetic CPU work for local HPA demonstrations.
# Defaults to 0 and is intended only for controlled load tests in minikube.
LOAD_TEST_CPU_BURN_SECONDS = float(os.getenv("LOAD_TEST_CPU_BURN_SECONDS", "0"))

# Allowed extensions for model files
ALLOWED_MODEL_EXTENSIONS = {".h5", ".keras", ".pb"}


def get_model(model_filename: str):
    """
    Load and cache a Keras model by its filename (Lazy Loading).

    Args:
        model_filename (str): The name of the model file (e.g., 'glaucoma_v1.h5').

    Returns:
        The loaded TensorFlow/Keras model instance.
    """
    if model_filename not in MODEL_CACHE:
        path = os.path.join(MODEL_DIR, model_filename)

        if not os.path.exists(path):
            raise FileNotFoundError(
                f"Model file '{model_filename}' not found at {path}"
            )

        print(f"[AI WORKER] Loading model from disk: {path}")

        # compile=False is used because we only need the model for inference (prediction).
        # We are not training the model here. This saves memory and speeds up initialization.
        MODEL_CACHE[model_filename] = load_model(path, compile=False)

    return MODEL_CACHE[model_filename]


def preprocess_image_from_file(
    file_bytes: bytes, target_size: tuple = (224, 224)
) -> np.ndarray:
    """
    Convert raw image bytes into a normalized NumPy array suitable for the Convolutional Neural Network (CNN).

    Steps:
    1. Read the raw bytes into a PIL Image.
    2. Convert to RGB to ensure consistency (handles grayscale or RGBA inputs).
    3. Resize to the target dimensions expected by the CNN architecture (e.g., 224x224).
    4. Convert to a NumPy array and normalize pixel values to the [0, 1] range.
    5. Add a batch dimension, changing the shape from (224, 224, 3) to (1, 224, 224, 3).
    """
    # Load image directly from memory (BytesIO) without saving it to the disk
    img = Image.open(BytesIO(file_bytes)).convert("RGB")

    # Resize the image
    img = img.resize(target_size)

    # Convert to array and normalize
    arr = image.img_to_array(img) / 255.0

    # Expand dimensions to create a batch of size 1
    return np.expand_dims(arr, axis=0)


def burn_cpu_for_load_test(seconds: float) -> None:
    if seconds <= 0:
        return

    end_at = time.perf_counter() + seconds
    value = 0.0
    while time.perf_counter() < end_at:
        # Keep one CPU core busy with deterministic floating point work.
        value = (value * 1.000001 + 3.14159) % 97.0


@app.get("/models/")
async def list_available_models():
    """
    API endpoint to list all available trained models in the MODEL_DIR.
    This allows the Django frontend to dynamically populate a dropdown menu.

    Returns:
        dict: A JSON response containing a list of model filenames.
    """
    try:
        # Check if directory exists
        if not os.path.exists(MODEL_DIR):
            return {
                "status": "error",
                "message": f"Model directory '{MODEL_DIR}' does not exist.",
                "models": [],
            }

        available_models = []
        for filename in os.listdir(MODEL_DIR):
            # Check if it's a file and has a valid model extension (or is a SavedModel directory)
            path = os.path.join(MODEL_DIR, filename)
            if os.path.isfile(path):
                _, ext = os.path.splitext(filename)
                if ext in ALLOWED_MODEL_EXTENSIONS:
                    available_models.append(filename)
            elif os.path.isdir(path):
                # Handle TensorFlow SavedModel format (which is a directory containing a saved_model.pb)
                if os.path.exists(os.path.join(path, "saved_model.pb")):
                    available_models.append(filename)

        return {"status": "success", "models": sorted(available_models)}

    except Exception as e:
        print(f"[AI WORKER ERROR] Failed to list models: {str(e)}")
        return {"status": "error", "message": str(e), "models": []}


@app.get("/health")
async def health():
    """
    Lightweight health endpoint for Docker/Kubernetes probes.

    It intentionally avoids loading TensorFlow models, because readiness checks
    must stay cheap while pods are scaling up.
    """
    return {
        "status": "ok",
        "node_type": NODE_TYPE,
        "worker_id": WORKER_ID,
        "model_dir": MODEL_DIR,
    }


@app.post("/predict/")
async def predict(file: UploadFile = File(...), model_name: str = Form(...)):
    """
    API endpoint to receive a medical image and return the glaucoma prediction.
    This endpoint is called by the Django backend (the Resource-Aware Load Balancer).

    Args:
        file (UploadFile): The image file sent via multipart/form-data.
        model_name (str): The specific model version to use for inference.

    Returns:
        dict: A JSON response containing the calculated probability of glaucoma.
    """
    try:
        # Start timer to measure inference latency for infrastructure monitoring
        start_mark = time.perf_counter()

        # 1. Read the raw bytes of the uploaded file asynchronously
        file_bytes = await file.read()

        # 2. Preprocess the image data for the neural network
        # Target size depends on how you trained your model (e.g., ResNet/VGG usually take 224x224)
        x = preprocess_image_from_file(file_bytes, target_size=(224, 224))

        # 3. Retrieve the requested model from the cache
        model = get_model(model_name)

        # 4. Perform the prediction.
        # model.predict returns a 2D array (e.g., [[0.85]]).
        # We extract the single scalar value and multiply by 100 to get a percentage.
        prediction_value = float(model.predict(x)[0][0])
        burn_cpu_for_load_test(LOAD_TEST_CPU_BURN_SECONDS)
        probability_percentage = prediction_value * 100

        # Calculate performance metric for load balancing analysis
        latency = round(time.perf_counter() - start_mark, 3)

        # 5. Return the result as a structured JSON response
        # Including metadata to verify CPU/GPU routing and HPA scaling pod IDs
        return {
            "status": "success",
            "probability": probability_percentage,
            "node_type": NODE_TYPE,
            "worker_id": WORKER_ID,
            "execution_time": latency,
        }

    except Exception as e:
        # Catch any errors (e.g., corrupted image, missing model) and return a clean error message
        print(f"[AI WORKER ERROR] {str(e)}")
        return {"status": "error", "message": str(e)}
