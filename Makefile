.PHONY: init up down check integration smoke manifests
init:
	go run ./cmd/bootstrap
up: init
	docker compose up --build -d --wait --wait-timeout 240
down:
	docker compose down
check:
	go vet ./...
	go test ./...
	go build ./...
integration:
	docker compose -f compose.yaml -f compose.test.yaml run --build --rm tests
smoke:
	go run ./cmd/smoke
manifests:
	go run ./cmd/manifests
	kubectl kustomize deploy
