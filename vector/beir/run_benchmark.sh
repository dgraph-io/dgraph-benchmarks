#!/bin/bash

# BEIR Benchmark Runner Script
# This script helps run benchmarks across different Dgraph versions

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Function to print colored output
print_info() {
    echo -e "${GREEN}[INFO]${NC} $1"
}

print_error() {
    echo -e "${RED}[ERROR]${NC} $1"
}

print_warning() {
    echo -e "${YELLOW}[WARNING]${NC} $1"
}

# Check if .env file exists
if [ ! -f .env ]; then
    print_warning ".env file not found. Copying from .env.example..."
    cp .env.example .env
    print_info "Please edit .env file with your desired configuration"
fi

# Source .env without overriding existing environment variables
# This allows command-line overrides like: DGRAPH_VERSION=v25.1.0 ./run_benchmark.sh
while IFS='=' read -r key value; do
    # Skip comments and empty lines
    [[ -z "$key" || "$key" =~ ^# ]] && continue
    # Remove leading/trailing whitespace from key
    key=$(echo "$key" | xargs)
    # Strip inline comments and trailing whitespace from value
    value=$(echo "$value" | sed 's/#.*$//' | xargs)
    # Only set if not already defined in environment
    if [ -z "${!key+x}" ]; then
        export "$key=$value"
    fi
done < .env

# Default Dgraph version
DGRAPH_VERSION=${DGRAPH_VERSION:-"local"}

# Function to run benchmark
run_benchmark() {
    print_info "=========================================="
    print_info "Testing Dgraph Version: $DGRAPH_VERSION"
    print_info "=========================================="
    
    # Stop any running containers
    print_info "Stopping any running Dgraph containers..."
    docker-compose down -v 2>/dev/null || true
    
    # Start Dgraph with the specified version
    print_info "Starting Dgraph $DGRAPH_VERSION..."
    docker-compose up -d
    
    # Wait for Dgraph to be ready
    print_info "Waiting for Dgraph to be ready..."
    sleep 10
    
    # Check if Dgraph is healthy
    max_retries=30
    retry_count=0
    while [ $retry_count -lt $max_retries ]; do
        if curl -s http://localhost:8080/health > /dev/null 2>&1; then
            print_info "Dgraph is ready!"
            break
        fi
        retry_count=$((retry_count + 1))
        echo -n "."
        sleep 2
    done
    echo ""
    
    if [ $retry_count -eq $max_retries ]; then
        print_error "Dgraph failed to start after $max_retries attempts"
        return 1
    fi
    
    # Run the benchmark
    print_info "Running benchmark..."
    uv run python evaluate.py
    
    # Stop Dgraph (unless KEEP_RUNNING is set)
    if [ "${KEEP_RUNNING:-false}" != "true" ]; then
        print_info "Stopping Dgraph..."
        docker-compose down -v
    else
        print_info "Keeping Dgraph running for manual testing..."
        print_info "To stop later, run: docker-compose down -v"
    fi
    
    print_info "Benchmark completed!"
}

# Main execution
print_info "BEIR Dgraph Benchmark Suite"
print_info "=========================================="

# Check if uv is installed
if ! command -v uv &> /dev/null; then
    print_error "uv is not installed. Please install it first:"
    echo "curl -LsSf https://astral.sh/uv/install.sh | sh"
    exit 1
fi

# Check if Docker is running
if ! docker info > /dev/null 2>&1; then
    print_error "Docker is not running. Please start Docker first."
    exit 1
fi

# Install dependencies
print_info "Installing dependencies..."
uv sync

# Run benchmark
run_benchmark || print_error "Benchmark failed for $DGRAPH_VERSION"

print_info "=========================================="
print_info "All benchmarks completed!"
print_info "Results are saved in the ./results directory"
print_info "=========================================="
