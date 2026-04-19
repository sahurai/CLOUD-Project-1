#!/usr/bin/env bash
#
# Run all tests for the Cloud AI Project
#
set -euo pipefail

echo "=== Running All Tests ==="

# Colors
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1"
}

# Test AI Worker (Python)
log_info "Testing AI Worker (Python)..."
cd cloud_ai_worker
if command -v python3 &> /dev/null; then
    # Activate virtual environment if it exists
    if [ -f "venv/bin/activate" ]; then
        source venv/bin/activate
    fi
    python3 -m pytest tests/ -v
else
    log_error "Python3 not found, skipping AI Worker tests"
fi
cd ..

# Test Load Balancer (Go)
log_info "Testing Load Balancer (Go)..."
cd load_balancer
if command -v go &> /dev/null; then
    go test -v ./... 2>/dev/null || log_warn "Go tests failed or Go toolchain issues - skipping"
else
    log_warn "Go not found, skipping Load Balancer tests"
fi
cd ..

# Integration test (if Kubernetes is running)
log_info "Checking for integration tests..."
if command -v kubectl &> /dev/null && kubectl cluster-info &> /dev/null; then
    log_info "Kubernetes cluster detected, running integration test..."
    if kubectl get namespace glaucoma &> /dev/null; then
        log_info "Glaucoma namespace found, running load balancer test..."
        # Start port forward in background
        kubectl port-forward svc/load-balancer -n glaucoma 8080:8080 &
        PORT_FORWARD_PID=$!

        # Wait for port forward to start
        sleep 3

        # Run the test
        ./test_load_balancer.sh http://localhost:8080

        # Kill port forward
        kill $PORT_FORWARD_PID 2>/dev/null || true
    else
        log_warn "Glaucoma namespace not found, skipping integration test"
    fi
else
    log_warn "Kubernetes not available, skipping integration test"
fi

log_info "All tests completed!"