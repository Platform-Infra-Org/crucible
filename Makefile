.PHONY: test build web local-check cluster-check
test:
	go test ./...
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -o bin/ ./cmd/...
web:
	cd web && npm ci && npm run build
local-check:
	./scripts/local-check.sh
cluster-check:
	./scripts/cluster-check.sh
