# Contract: agent.yaml `modules` section

```yaml
modules:
  enabled: true                       # false: items are reported disabled_locally
  directory: /opt/tangra/modules      # absolute; not /proc /sys /dev /home /root /tmp; not a symlink
  owner: root                         # name or uid of written files
  group: root
  deploy_hook: ""                     # optional absolute path; same checks as certificates.deploy_hook
  hook_timeout_seconds: 600           # 30..1800
  allow_insecure_transport: false     # announce module.v1 over a plaintext edge (lab only)
```

`module.v1` is announced only when `enabled`, on Linux, and the edge uses TLS
(or `allow_insecure_transport`).

Layout written: `<directory>/<module>/…` exactly as the archive's
`<module>/` tree. Staged in `<directory>/.staging-<module>-<random>` then
renamed into place; `<directory>/<module>` existing → `already_installed`.

Hook environment: `PATH`, `LANG`, `MODULE_NAME`, `MODULE_VERSION`,
`MODULE_DIR`; working directory `MODULE_DIR`; no arguments, no shell.
Example hook shipped as `packaging/examples/module-compose-up.sh`.
