.PHONY: test build web local-check cluster-check toolbox
test:
	go test ./...
build:
	mkdir -p bin
	CGO_ENABLED=0 go build -ldflags "-X crucible/internal/docs.Version=$$(git describe --always --dirty 2>/dev/null || echo dev)" -o bin/ ./cmd/...
web:
	cd web && npm ci && npm run build
local-check:
	./scripts/local-check.sh
cluster-check:
	./scripts/cluster-check.sh

TOOLBOX_TAG ?= dev
toolbox:
	docker build -f deploy/toolbox/Dockerfile -t crucible-toolbox:$(TOOLBOX_TAG) .
