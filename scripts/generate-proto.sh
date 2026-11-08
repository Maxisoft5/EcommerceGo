#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
mkdir -p .tools
export GOBIN="$(pwd)/.tools"
export PATH="$GOBIN:$PATH"
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.9
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.5.1
protoc --go_out=. --go_opt=module=flashcart --go-grpc_out=. --go-grpc_opt=module=flashcart api/proto/inventory/v1/inventory.proto
