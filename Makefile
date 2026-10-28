.PHONY: lint test build run docs

lint:
	golangci-lint run ./...

test:
	go test -race -cover ./...

build:
	go build -o bin/entitlements-api ./cmd/entitlements-api

run:
	go run ./cmd/entitlements-api

docs:
	swag init -d cmd/entitlements-api/,server/ --parseDependency --parseInternal -o docs
