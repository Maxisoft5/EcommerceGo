param([string]$KindCluster = '')
$ErrorActionPreference = 'Stop'
function Check-Exit { if ($LASTEXITCODE -ne 0) { throw "Command failed: exit $LASTEXITCODE" } }
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
  go run ./cmd/bootstrap; Check-Exit
  foreach ($service in @('gateway','identity','catalog','inventory','orders','projection')) {
    docker build --build-arg "SERVICE=$service" -t "flashcart/${service}:dev" .; Check-Exit
    if ($KindCluster) { kind load docker-image "flashcart/${service}:dev" --name $KindCluster; Check-Exit }
  }
  kubectl create namespace flashcart --dry-run=client -o yaml | kubectl apply -f -; Check-Exit
  kubectl -n flashcart create secret generic app-secrets --from-env-file=.env --dry-run=client -o yaml | kubectl apply -f -; Check-Exit
  $secrets = @{
    'jwt-signing' = @('jwt-private.pem')
    'jwt-verifier' = @('jwt-public.pem')
    'gateway-tls' = @('gateway.pem','gateway-key.pem')
    'grpc-inventory' = @('ca.pem','inventory.pem','inventory-key.pem')
    'grpc-orders' = @('ca.pem','orders.pem','orders-key.pem')
  }
  foreach ($name in $secrets.Keys) {
    $fileArgs = @($secrets[$name] | ForEach-Object { "--from-file=$_=.secrets/$_" })
    kubectl -n flashcart create secret generic $name @fileArgs --dry-run=client -o yaml | kubectl apply -f -; Check-Exit
  }
  kubectl apply -k deploy; Check-Exit
  kubectl -n flashcart wait --for=condition=complete job/kafka-init --timeout=300s; Check-Exit
  foreach($service in @('identity','catalog','inventory','orders','projection','gateway')) {
    kubectl -n flashcart rollout status "deployment/$service" --timeout=300s; Check-Exit
  }
  Write-Host 'Ready: https://localhost:8443 (kind port mapping), or kubectl -n flashcart port-forward svc/gateway 8443:443'
} finally { Pop-Location }
