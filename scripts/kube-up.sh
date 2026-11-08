#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
go run ./cmd/bootstrap
for service in gateway identity catalog inventory orders projection; do
  docker build --build-arg "SERVICE=$service" -t "flashcart/$service:dev" .
  if [ -n "${KIND_CLUSTER:-}" ]; then kind load docker-image "flashcart/$service:dev" --name "$KIND_CLUSTER"; fi
done
kubectl create namespace flashcart --dry-run=client -o yaml | kubectl apply -f -
kubectl -n flashcart create secret generic app-secrets --from-env-file=.env --dry-run=client -o yaml | kubectl apply -f -
kubectl -n flashcart create secret generic jwt-signing --from-file=.secrets/jwt-private.pem --dry-run=client -o yaml | kubectl apply -f -
kubectl -n flashcart create secret generic jwt-verifier --from-file=.secrets/jwt-public.pem --dry-run=client -o yaml | kubectl apply -f -
kubectl -n flashcart create secret generic gateway-tls --from-file=.secrets/gateway.pem --from-file=.secrets/gateway-key.pem --dry-run=client -o yaml | kubectl apply -f -
kubectl -n flashcart create secret generic grpc-inventory --from-file=.secrets/ca.pem --from-file=.secrets/inventory.pem --from-file=.secrets/inventory-key.pem --dry-run=client -o yaml | kubectl apply -f -
kubectl -n flashcart create secret generic grpc-orders --from-file=.secrets/ca.pem --from-file=.secrets/orders.pem --from-file=.secrets/orders-key.pem --dry-run=client -o yaml | kubectl apply -f -
kubectl apply -k deploy
kubectl -n flashcart wait --for=condition=complete job/kafka-init --timeout=300s
for service in identity catalog inventory orders projection gateway; do
  kubectl -n flashcart rollout status "deployment/$service" --timeout=300s
done
echo 'Ready: https://localhost:8443 (kind), or kubectl -n flashcart port-forward svc/gateway 8443:443'
