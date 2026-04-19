#!/usr/bin/env bash
#
# Complete Kubernetes setup script for the Cloud AI Project.
# This script handles the full setup: Docker, minikube, image building, and deployment.
#
# Usage:
#   ./setup_k8s.sh          # Full setup (build + deploy)
#   ./setup_k8s.sh apply    # Deploy only (skip image build)
#
set -euo pipefail

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

# --- Check prerequisites ---

log_info "Checking prerequisites..."

# Check if Docker is installed
if ! command -v docker &> /dev/null; then
    log_error "Docker is not installed. Please install Docker first."
    exit 1
fi

# Check if Docker daemon is running
if ! docker info &> /dev/null; then
    log_warn "Docker daemon is not running. Starting Docker..."
    sudo systemctl start docker
    # Wait a bit for Docker to start
    sleep 5
    if ! docker info &> /dev/null; then
        log_error "Failed to start Docker daemon."
        exit 1
    fi
fi

log_info "Docker is running."

# Check if minikube is installed
if ! command -v minikube &> /dev/null; then
    log_error "minikube is not installed. Please install minikube first."
    exit 1
fi

# Check if kubectl is installed
if ! command -v kubectl &> /dev/null; then
    log_error "kubectl is not installed. Please install kubectl first."
    exit 1
fi

# --- Start minikube if not running ---

log_info "Checking minikube status..."
if ! minikube status &> /dev/null; then
    log_info "Starting minikube cluster..."
    export DOCKER_HOST=unix:///var/run/docker.sock
    minikube start --driver=docker
else
    log_info "minikube is already running."
fi

# --- Run the deployment ---

log_info "Running deployment script..."
if [[ "${1:-}" == "apply" ]]; then
    ./k8s/deploy.sh apply
else
    ./k8s/deploy.sh
fi

# --- Post-deployment checks ---

log_info "Performing post-deployment checks..."

# Wait a bit for services to be ready
sleep 10

# Check pods
log_info "Checking pod status..."
kubectl get pods -n glaucoma

# Check services
log_info "Checking service status..."
kubectl get services -n glaucoma

# Get minikube IP
MINIKUBE_IP=$(minikube ip)
FRONTEND_PORT=$(kubectl get service frontend -n glaucoma -o jsonpath='{.spec.ports[0].nodePort}')

log_info "Setup complete!"
echo ""
echo "Access the application at: http://${MINIKUBE_IP}:${FRONTEND_PORT}"
echo ""
echo "To test the load balancer, run: ./test_load_balancer.sh http://${MINIKUBE_IP}:30501"
echo ""
echo "To stop the cluster: minikube stop"
echo "To delete the cluster: minikube delete"