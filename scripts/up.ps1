$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
  if (-not (Test-Path -LiteralPath '.env')) {
    docker build -f Dockerfile.bootstrap -t flashcart-bootstrap .
    if ($LASTEXITCODE -ne 0) { throw 'bootstrap build failed' }
    $projectPath = (Get-Location).Path
    docker run --rm --mount "type=bind,source=$projectPath,target=/work" flashcart-bootstrap
    if ($LASTEXITCODE -ne 0) { throw 'bootstrap failed' }
  }
  docker compose up --build -d --wait --wait-timeout 240
  if ($LASTEXITCODE -ne 0) { throw 'compose startup failed' }
  Write-Host 'FlashCart: https://localhost:8443; admin credentials: .env; local CA: .secrets/ca.pem'
} finally { Pop-Location }
