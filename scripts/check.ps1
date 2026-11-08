$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
  New-Item -ItemType Directory -Force artifacts | Out-Null
  $unformatted = @(gofmt -l cmd internal tests api/gen)
  if ($unformatted.Count -gt 0) { throw "Run gofmt: $unformatted" }
  go vet ./...; if ($LASTEXITCODE -ne 0) { throw 'go vet failed' }
  go test -count=1 ./...; if ($LASTEXITCODE -ne 0) { throw 'go test failed' }
  go build ./...; if ($LASTEXITCODE -ne 0) { throw 'go build failed' }
  go run ./cmd/manifests; if ($LASTEXITCODE -ne 0) { throw 'manifest generation failed' }
  docker compose config --quiet; if ($LASTEXITCODE -ne 0) { throw 'compose config failed' }
  kubectl kustomize deploy -o artifacts/kubernetes.yaml; if ($LASTEXITCODE -ne 0) { throw 'kustomize failed' }
} finally { Pop-Location }
