#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
if [ ! -f .env ]; then
  docker build -f Dockerfile.bootstrap -t flashcart-bootstrap .
  docker run --rm --user "$(id -u):$(id -g)" --mount "type=bind,source=$(pwd),target=/work" flashcart-bootstrap
fi
docker compose up --build -d --wait --wait-timeout 240
echo 'FlashCart: https://localhost:8443; admin credentials: .env; local CA: .secrets/ca.pem'
