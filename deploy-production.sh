#!/bin/bash
# Production deployment. Run by .github/workflows/deploy.yml on the self-hosted
# runner, from the checked-out repo root, after .env has been written.
set -euo pipefail

echo "Stopping existing container..."
docker-compose down

echo "Building and starting..."
docker-compose up --build -d

echo "Container status:"
docker-compose ps
