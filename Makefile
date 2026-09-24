GO        ?= go
PKGS      := $(shell $(GO) list ./... | grep -v /ui/)
COVER_OUT := coverage.out

.PHONY: lint vuln test test-integration cover generate ui-build build build-ui image compose-up compose-down agent-windows agent-linux agent

lint:
	$(GO) vet ./...
	staticcheck ./...
	gosec -quiet -exclude-generated -exclude-dir=ui ./...

vuln:
	./scripts/vulncheck.sh
	cd sdk && ../scripts/vulncheck.sh

test:
	$(GO) test -race -count=1 ./...

test-integration:
	$(GO) test -race -count=1 -tags integration ./tests/integration/...

# Generated protobuf, SQL bindings (internal/store, */*db), wiring (internal/app,
# cmd) and test packages are exercised by the tagged integration suite and are
# excluded from the unit gate on purpose.
COVERPKG := $(shell $(GO) list ./... | grep -v -E '/api/|/internal/store$$|db$$|/internal/app$$|/valkeykv$$|/cmd/|/tests/|/ui|/internal/collector|/internal/sender|/internal/daemon|/internal/winsvc|/internal/stream' | paste -sd, -)

cover:
	$(GO) test -count=1 -coverprofile=$(COVER_OUT) -coverpkg=$(COVERPKG) $(PKGS)
	./scripts/coverage-gate.sh $(COVER_OUT)

generate:
	cd sdk && buf generate

# Build the federated UI remote (produces ui/dist consumed by the -tags ui build).
ui-build:
	cd ui && npm ci && npm run build

# Build the service binary without the embedded UI.
build:
	$(GO) build -o bin/inventorysvc ./cmd/inventorysvc

# Build the service binary with the embedded UI remote (requires ui-build first).
build-ui: ui-build
	$(GO) build -tags "ui" -o bin/inventorysvc ./cmd/inventorysvc

# Build the container image (NODE_AUTH_TOKEN: GitHub token with read:packages for @go-tangra/ui).
image:
	DOCKER_BUILDKIT=1 docker buildx build --secret id=npm_token,env=NODE_AUTH_TOKEN -t go-tangra-inventory:dev .

compose-up:
	docker compose -p inventory -f deploy/compose.yaml up -d

compose-down:
	docker compose -p inventory -f deploy/compose.yaml down -v

# Cross-compile the endpoint agent for the platforms it ships to.
agent: agent-windows agent-linux

agent-windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o bin/inventory-agent-windows-amd64.exe ./cmd/inventory-agent
	GOOS=windows GOARCH=arm64 $(GO) build -o bin/inventory-agent-windows-arm64.exe ./cmd/inventory-agent

agent-linux:
	GOOS=linux GOARCH=amd64 $(GO) build -o bin/inventory-agent-linux-amd64 ./cmd/inventory-agent
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/inventory-agent-linux-arm64 ./cmd/inventory-agent
