# Contract: mesh policy

Gateway inbound (`deploy/policy.yaml` of the portal, prod `policies/gateway.yaml`):

```yaml
  - id: inventory-module-bundle
    from: ["spiffe://<td>/svc/inventory"]
    to: ["gateway"]
    operations: ["/inventory.v1.ModuleBundleSource/RenderModuleBundle"]
    effect: allow
```

Inventory inbound: the existing `gateway-forwards` rule already lets the
gateway call every inventory method; the handler additionally requires
`gateway` in `module_delivery.sources`.

Inventory server config:

```yaml
module_delivery:
  enabled: true
  sources: [gateway]
  source_service: gateway          # mesh service serving ModuleBundleSource
  report_timeout_minutes: 30       # 5..120
  max_concurrent_fetches: 20       # 1..500
  source_timeout_seconds: 30       # 2..120
  allow_plaintext_ingest: false    # refused in production
```
