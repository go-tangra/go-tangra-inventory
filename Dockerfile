# syntax=docker/dockerfile:1
# Inventory service image: builds the Vue remote, embeds it (-tags ui), and
# produces a slim runtime carrying inventorysvc. Build context is the repo root
# so the module's replace directives (../.. and sibling services) resolve.

FROM node:22-alpine AS ui
# The front-ends form one npm workspace (root package-lock.json) with the shared
# kit at ui/kit; install the workspace, build the kit, then this front-end.
WORKDIR /w
COPY package.json package-lock.json .npmrc ./
COPY ui/kit/package.json ui/kit/
COPY services/gateway/shell/package.json services/gateway/shell/
COPY services/auth/console/package.json services/auth/console/
COPY services/asset/ui/package.json services/asset/ui/
COPY services/inventory/ui/package.json services/inventory/ui/
COPY services/ipam/ui/package.json services/ipam/ui/
COPY services/paperless/ui/package.json services/paperless/ui/
COPY services/deployer/ui/package.json services/deployer/ui/
COPY services/lcm/ui/package.json services/lcm/ui/
COPY services/notification/ui/package.json services/notification/ui/
COPY services/warden/ui/package.json services/warden/ui/
COPY services/ticket/ui/package.json services/ticket/ui/
COPY services/dns/ui/package.json services/dns/ui/
RUN npm ci --no-audit --no-fund
COPY ui/ ./ui/
RUN npm run -w ui/kit build
COPY services/inventory/ui/ ./services/inventory/ui/
RUN npm run -w services/inventory/ui build

FROM golang:1.26-alpine AS build
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY . .
COPY --from=ui /w/services/inventory/ui/dist ./services/inventory/ui/dist
WORKDIR /src/services/inventory
ENV CGO_ENABLED=0 GOFLAGS=-buildvcs=false
RUN go build -tags "ui" -o /out/inventorysvc ./cmd/inventorysvc

FROM alpine:3.20
RUN apk add --no-cache ca-certificates postgresql-client && adduser -D -u 10001 app
COPY --from=build /out/inventorysvc /usr/local/bin/
COPY services/inventory/deploy /app/deploy
WORKDIR /app
USER app
ENTRYPOINT ["inventorysvc"]
CMD ["-config", "deploy/container.yaml"]
