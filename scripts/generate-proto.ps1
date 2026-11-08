$ErrorActionPreference = 'Stop'
Push-Location (Split-Path -Parent $PSScriptRoot)
try {
  New-Item -ItemType Directory -Force .tools | Out-Null
  $env:GOBIN = Join-Path (Get-Location) '.tools'
  go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.9
  if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-go install failed' }
  go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
  if ($LASTEXITCODE -ne 0) { throw 'protoc-gen-go-grpc install failed' }
  $env:PATH = "$env:GOBIN;$env:PATH"
  protoc --go_out=. --go_opt=module=flashcart --go-grpc_out=. --go-grpc_opt=module=flashcart api/proto/inventory/v1/inventory.proto
  if ($LASTEXITCODE -ne 0) { throw 'protoc failed; install official protoc v32.1 first' }
} finally { Pop-Location }
