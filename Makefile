GO        ?= go
PKGS      := $(shell $(GO) list ./... | grep -v /ui/)
COVER_OUT := coverage.out

.PHONY: lint vuln test test-integration cover fuzz proto-check generate ui-build build build-ui image agent-windows agent-linux agent packages agent-release agent-release-dev release-check e2e-upgrade test-agent-certs

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
# excluded from the unit gate on purpose. The OS glue of the agent certificate
# store (internal/agentcerts/*_linux.go) is skipped by scripts/coverage-gate.sh
# for the 100 % gate of internal/agentcerts and covered by test-agent-certs.
COVERPKG := $(shell $(GO) list ./... | grep -v -E '/api/|/internal/store$$|db$$|/internal/app$$|/valkeykv$$|/cmd/|/tests/|/ui|/internal/collector|/internal/upgrader|/internal/sender|/internal/daemon|/internal/winsvc|/internal/stream' | paste -sd, -)

cover:
	$(GO) test -count=1 -coverprofile=$(COVER_OUT) -coverpkg=$(COVERPKG) $(PKGS)
	./scripts/coverage-gate.sh $(COVER_OUT)

# Run every Fuzz* target of the module for FUZZTIME each (parsers of agent
# facts, the ingest mapper, the host report projection, enrollment tokens, diff).
# Feature 023 adds FuzzSMBIOSStructures, FuzzSysBlock, FuzzWindowsDisks
# (internal/agentfacts), FuzzManifest, FuzzVersion (internal/agentrelease) and
# extends FuzzSubmitMapper, FuzzHostReport and FuzzDiff. Feature 033 adds
# FuzzValidName, FuzzParseBundle, FuzzHostTag (internal/certmaterial),
# FuzzReportCertificate (internal/ingest) and FuzzAgentCertsConfig
# (internal/config); the loop below picks up every target automatically.
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
	cd sdk && buf lint && buf breaking --against '../.git#tag=sdk/v4.3.0,subdir=sdk'

# Build the federated UI remote (produces ui/dist consumed by the -tags ui build).
ui-build:
	cd ui && npm ci && npm run build

# Release signing public keyring ("<id>:<base64 Ed25519 public key>[,...]")
# compiled into the agent and the service (feature 023). Release builds get
# it from the AGENT_RELEASE_PUBLIC_KEYS setting of the CI; without it the
# binaries refuse every agent upgrade. `make agent-release-dev` uses a
# locally generated development key instead.
AGENT_RELEASE_KEYS ?=
KEYS_LDFLAG := $(if $(AGENT_RELEASE_KEYS),-X github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease.productionKeys=$(AGENT_RELEASE_KEYS))

# Build the service binary without the embedded UI.
build:
	$(GO) build -ldflags "$(KEYS_LDFLAG)" -o bin/inventorysvc ./cmd/inventorysvc

# Build the service binary with the embedded UI remote (requires ui-build first).
build-ui: ui-build
	$(GO) build -tags "ui" -ldflags "$(KEYS_LDFLAG)" -o bin/inventorysvc ./cmd/inventorysvc

# Build the container image (NODE_AUTH_TOKEN: GitHub token with read:packages for @go-tangra/ui).
image:
	DOCKER_BUILDKIT=1 docker buildx build --secret id=npm_token,env=NODE_AUTH_TOKEN -t go-tangra-inventory:dev .

# Cross-compile the endpoint agent for the platforms it ships to.
agent: agent-windows agent-linux

# Agents are static and stamped with the version the packages carry and the
# release signing keyring.
AGENT_VERSION ?= $(shell git describe --tags --match 'v*' --always 2>/dev/null | sed 's/^v//')
AGENT_LDFLAGS := -s -w -X main.version=$(AGENT_VERSION) $(KEYS_LDFLAG)

agent-windows:
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=windows GOARCH=$$arch $(GO) build -trimpath -ldflags "$(AGENT_LDFLAGS)" -o bin/inventory-agent-windows-$$arch.exe ./cmd/inventory-agent || exit 1; \
	done

agent-linux:
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(AGENT_LDFLAGS)" -o bin/inventory-agent-linux-$$arch ./cmd/inventory-agent || exit 1; \
	done

# .deb and .rpm packages of the Linux agent (binary, systemd unit, sample
# /etc/inventory-agent/agent.yaml) in dist/. nfpm is run at a pinned version.
NFPM ?= $(GO) run github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0

packages: agent-linux
	mkdir -p dist
	for arch in amd64 arm64; do \
		mkdir -p bin/pkg && cp bin/inventory-agent-linux-$$arch bin/pkg/inventory-agent || exit 1; \
		for fmt in deb rpm; do \
			VERSION=$(AGENT_VERSION) ARCH=$$arch $(NFPM) package -f packaging/nfpm.yaml -p $$fmt -t dist/ || exit 1; \
		done; \
	done

# The eight artifacts of an agent release (feature 023) in dist/agent/:
# deb and rpm packages and raw binaries for Linux amd64/arm64, exes for
# Windows amd64/arm64, all version-stamped and carrying AGENT_RELEASE_KEYS.
# CI signs them in the protected release environment (agent-release sign).
AGENT_DIST := dist/agent
agent-release:
	@test -n "$(AGENT_VERSION)" || { echo "AGENT_VERSION is empty" >&2; exit 1; }
	mkdir -p $(AGENT_DIST) bin/pkg
	find $(AGENT_DIST) -mindepth 1 -maxdepth 1 -type f -delete
	for arch in amd64 arm64; do \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch $(GO) build -trimpath -ldflags "$(AGENT_LDFLAGS)" -o $(AGENT_DIST)/inventory-agent-linux-$$arch ./cmd/inventory-agent || exit 1; \
		CGO_ENABLED=0 GOOS=windows GOARCH=$$arch $(GO) build -trimpath -ldflags "$(AGENT_LDFLAGS)" -o $(AGENT_DIST)/inventory-agent-windows-$$arch.exe ./cmd/inventory-agent || exit 1; \
		cp $(AGENT_DIST)/inventory-agent-linux-$$arch bin/pkg/inventory-agent || exit 1; \
		for fmt in deb rpm; do \
			VERSION=$(AGENT_VERSION) ARCH=$$arch $(NFPM) package -f packaging/nfpm.yaml -p $$fmt -t $(AGENT_DIST)/ || exit 1; \
		done; \
	done

# Development release for local stacks (freya-stack): a development key pair
# is generated once into $(DEV_KEY_DIR) (git-ignored, never committed; key id
# "dev-local", refused by release-check), the agents and the service are
# built with its public key and the signed bundle is copied to
# agent-releases/<version>/ for `make image`. Use a release-like version, e.g.
#   make agent-release-dev AGENT_VERSION=4.4.1
DEV_KEY_DIR ?= .dev/agent-release
agent-release-dev:
	mkdir -p $(DEV_KEY_DIR)
	test -f $(DEV_KEY_DIR)/dev.key || $(GO) run ./cmd/agent-release keygen -out-private $(DEV_KEY_DIR)/dev.key -key-id dev-local > $(DEV_KEY_DIR)/dev.pub
	$(MAKE) agent-release AGENT_VERSION=$(AGENT_VERSION) AGENT_RELEASE_KEYS="$$(cat $(DEV_KEY_DIR)/dev.pub)"
	AGENT_RELEASE_SIGNING_KEY="$$(cat $(DEV_KEY_DIR)/dev.key)" $(GO) run -ldflags "-X github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease.productionKeys=$$(cat $(DEV_KEY_DIR)/dev.pub)" ./cmd/agent-release sign -key-env AGENT_RELEASE_SIGNING_KEY -key-id dev-local -version $(AGENT_VERSION) -dir $(AGENT_DIST)
	mkdir -p agent-releases/$(AGENT_VERSION)
	cp $(AGENT_DIST)/* agent-releases/$(AGENT_VERSION)/
	@echo "dev release $(AGENT_VERSION) in agent-releases/$(AGENT_VERSION); build the image with AGENT_RELEASE_KEYS=\"$$(cat $(DEV_KEY_DIR)/dev.pub)\""

# Release artifacts carry the production keyring, no development key and a
# release version (scripts/check-release-binary.sh).
release-check:
	./scripts/check-release-binary.sh -keys "$(AGENT_RELEASE_KEYS)" -version "$(AGENT_VERSION)" \
		$(AGENT_DIST)/inventory-agent-linux-amd64 $(AGENT_DIST)/inventory-agent-linux-arm64 \
		$(AGENT_DIST)/inventory-agent-windows-amd64.exe $(AGENT_DIST)/inventory-agent-windows-arm64.exe

# Agent certificate store on a real filesystem (feature 033): file ownership
# and modes, the live/<name> generation swap and deploy hook execution as root
# in a privileged container (Docker; //go:build agentcerts_e2e).
test-agent-certs:
	$(GO) test -tags agentcerts_e2e -count=1 -v ./internal/agentcerts/...

# Package upgrade and rollback in Debian 12 and Rocky 9 containers with
# systemd (Docker, privileged containers; CI job e2e-upgrade).
e2e-upgrade:
	$(GO) test -tags e2e -count=1 -timeout 40m -v ./tests/e2e/...
