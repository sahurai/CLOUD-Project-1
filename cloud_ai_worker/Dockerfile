# Use the official lightweight Python image to keep the container size small.
# This is crucial for fast scaling in Kubernetes (faster pod startup times).
FROM python:3.10-slim

# Set environment variables:
# PYTHONDONTWRITEBYTECODE - Prevents Python from writing .pyc files to disk.
# PYTHONUNBUFFERED - Ensures that Python output is logged directly to the terminal (useful for real-time debugging).
# TF_CPP_MIN_LOG_LEVEL - Suppresses verbose TensorFlow logs (hides INFO and WARNING messages).
ENV PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1 \
    TF_CPP_MIN_LOG_LEVEL=2

# Set the working directory inside the container
WORKDIR /app

# Copy the dependencies file to the working directory
COPY requirements.txt /app/

# Install Python dependencies.
# The '--no-cache-dir' flag is used to reduce the final Docker image size 
# by not storing the downloaded pip archives.
RUN pip install --no-cache-dir -r requirements.txt

# Copy the rest of the microservice code into the container.
# (Make sure large model files are either copied here or mounted as volumes later).
COPY . /app/

# Expose port 8001 so the Django router (Load Balancer) can communicate with this AI worker
EXPOSE 8001

# Command to run the Uvicorn server when the container starts
CMD ["uvicorn", "main:app", "--host", "0.0.0.0", "--port", "8001"]