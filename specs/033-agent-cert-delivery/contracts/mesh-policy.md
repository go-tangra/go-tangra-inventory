# Contract: mesh policy and stack configuration changes (033)

Every rule is method-specific; deny remains the default. Each repo's
`deploy/policy.yaml` (dev/test) and the production copies in
`go-tangra-docker/policies/` change together (go-tangra-docker is edited
by the user).

## lcm (`go-tangra-lcm-v4/deploy/policy.yaml`, `go-tangra-docker/policies/lcm.yaml`)

```yaml
  - id: inventory-download
    # inventory relays certificates to its agents (feature 033): it fetches
    # cert + chain (+ retained key) at the moment an agent pulls a delivery
    # the deployer created; nothing is persisted by inventory.
    from: ["spiffe://example.org/svc/inventory"]
    to: ["lcm"]
    operations: ["/lcm.v1.Certificates/Download"]
    effect: allow
```

## inventory (`deploy/policy.yaml`, `go-tangra-docker/policies/inventory.yaml`)

```yaml
  - id: deployer-cert-delivery
    # the deployer's inventory-agent provider requests deliveries and reads
    # their state (feature 033); references only, never material. The
    # handler additionally checks cert_delivery.sources.
    from: ["spiffe://example.org/svc/deployer"]
    to: ["inventory"]
    operations: ["/inventory.v1.CertificateDeliveryService/CreateCertificateDelivery",
                 "/inventory.v1.CertificateDeliveryService/GetCertificateDelivery",
                 "/inventory.v1.CertificateDeliveryService/PreviewCertificateTargets",
                 "/inventory.v1.CertificateDeliveryService/VerifyHostCertificates",
                 "/inventory.v1.CertificateDeliveryService/MarkCertificateRevoked",
                 "/grpc.health.v1.Health/Check"]
    effect: allow
```

The `gateway-forwards` rule (`operations: ["*"]`) also matches the new
service. The gateway only proxies HTTP routes declared by the module; the
module manifest declares no gRPC `Methods`, so browser traffic cannot
reach `CertificateDeliveryService`. A test asserts that the HTTP router
exposes no route to it.

## deployer (`go-tangra-deployer-v4/deploy/policy.yaml`)

Unchanged (no inbound change).

## Stack configuration (`go-tangra-docker/configs/*.yaml`, user-applied)

`inventory.yaml`:

```yaml
discovery:
  static:
    lcm: ["lcm:9945"]               # already present
cert_delivery:
  enabled: true
  sources: ["deployer"]
  lcm_service: lcm
  allow_plaintext_ingest: true      # dev stack only (ingest.insecure); NEVER in production
```

`deployer.yaml`:

```yaml
discovery:
  static:
    inventory: ["inventory:9975"]
inventory: { service: inventory }
```
