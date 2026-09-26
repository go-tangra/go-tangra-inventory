GO        ?= go
PKGS      := $(shell $(GO) list ./... | grep -v /ui/)
COVER_OUT := coverage.out

.PHONY: lint vuln test test-integration cover fuzz proto-check generate ui-build build build-ui image agent-windows agent-linux agent

lint:
	$(GO) vet ./...
	staticcheck ./...
	gosec -quiet -exclude-generated -exclude-dir=ui ./...

vuln:
	./scripts/vulncheck.sh
	cd sdk && ../scripts/vulncheck.sh

test:
	$(GO) test -race -count=1 ./...

# Docker-backed suites (testcontainers) carry the integration build tag next to
# the code they exercise.
test-integration:
	$(GO) test -race -count=1 -tags integration ./...

# Generated protobuf, SQL bindings (internal/store, */*db), wiring (internal/app,
# cmd) and test packages are exercised by the tagged integration suite and are
# excluded from the unit gate on purpose.
COVERPKG := $(shell $(GO) list ./... | grep -v -E '/api/|/internal/store$$|db$$|/internal/app$$|/valkeykv$$|/cmd/|/tests/|/ui|/internal/collector|/internal/sender|/internal/daemon|/internal/winsvc|/internal/stream' | paste -sd, -)

cover:
	$(GO) test -count=1 -coverprofile=$(COVER_OUT) -coverpkg=$(COVERPKG) $(PKGS)
	./scripts/coverage-gate.sh $(COVER_OUT)

# Run every Fuzz* target of the module for FUZZTIME each (parsers of agent
# facts, the ingest mapper, the host report projection, enrollment tokens, diff).
FUZZTIME ?= 10s
fuzz:
	@set -e; for pkg in $$($(GO) list ./... | grep -v /ui/); do \
	  for f in $$($(GO) test -list '^Fuzz' $$pkg | grep '^Fuzz' || true); do \
	    echo "fuzz $$pkg $$f"; \
	    $(GO) test -run='^$$' -fuzz="^$$f$$" -fuzztime=$(FUZZTIME) $$pkg; \
	  done; \
	done

generate:
	cd sdk && buf generate

# Proto contract: lint and stay wire-compatible with the released SDK.
proto-check:
	cd sdk && buf lint && buf breaking --against '../.git#tag=sdk/v4.0.0,subdir=sdk'

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

# Cross-compile the endpoint agent for the platforms it ships to.
agent: agent-windows agent-linux

agent-windows:
	GOOS=windows GOARCH=amd64 $(GO) build -o bin/inventory-agent-windows-amd64.exe ./cmd/inventory-agent
	GOOS=windows GOARCH=arm64 $(GO) build -o bin/inventory-agent-windows-arm64.exe ./cmd/inventory-agent

agent-linux:
	GOOS=linux GOARCH=amd64 $(GO) build -o bin/inventory-agent-linux-amd64 ./cmd/inventory-agent
	GOOS=linux GOARCH=arm64 $(GO) build -o bin/inventory-agent-linux-arm64 ./cmd/inventory-agent
