VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE    ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -X main.version=$(VERSION) -X main.commitHash=$(COMMIT) -X main.buildDate=$(DATE)

KRATOS_THIRD_PARTY := $(shell go list -m -f '{{.Dir}}' github.com/go-kratos/kratos/v2 2>/dev/null)/third_party

.PHONY: build build-collector build-inventory proto openapi gen clean tidy

build: build-collector build-inventory

build-collector:
	go build -ldflags "$(LDFLAGS)" -o inventory-collector ./cmd/collector

build-inventory:
	go build -ldflags "$(LDFLAGS)" -o inventory ./cmd/inventory

proto:
	protoc \
		--go_out=gen/go --go_opt=paths=source_relative \
		--go-grpc_out=gen/go --go-grpc_opt=paths=source_relative \
		--go-http_out=gen/go --go-http_opt=paths=source_relative \
		--proto_path=proto \
		--proto_path=/usr/include \
		--proto_path=$(KRATOS_THIRD_PARTY) \
		proto/inventory/collector/v1/collector.proto

openapi:
	buf generate --template buf.openapi.gen.yaml

gen: proto openapi

clean:
	rm -f inventory-collector inventory

tidy:
	go mod tidy
