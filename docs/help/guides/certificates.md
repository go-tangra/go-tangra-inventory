---
title: Certificates on hosts
summary: How Linux agents receive certificates from the deployer, where the files go, how to reload a service with a deploy hook, and how to follow deliveries.
kind: guide
order: 40
tags: [inventory, certificates, deployer, agent, deploy-hook]
requires: inventory:read
---
The deployer's **Inventory agent** provider installs lcm certificates on
Linux hosts through their inventory agent. You choose the hosts and the
certificate in the deployer; inventory hands the certificate to the agent,
which writes it on the host. Inventory itself never stores certificate files
or keys.

## What the host needs

- A Linux agent of version 4.7.0 or later, connected over TLS. Windows
  agents never receive certificates.
- `certificates.enabled` left on in `agent.yaml` (the default).
- Certificate delivery switched on by your operator on the server.

The **Certificates** column on the **Agents** page tells you per agent:
*enabled*, *disabled on host*, *upgrade required*, *not supported*
(Windows) or *off* (switched off on the server).

## Where the files go

The agent writes only below `certificates.directory` (default
`/etc/inventory-agent/certs`), in the layout certbot uses:

```text
live/<name>/cert.pem          the certificate
live/<name>/chain.pem         the issuer chain
live/<name>/fullchain.pem     certificate and chain
live/<name>/privkey.pem       the private key, when the deployer sends one
archive/<name>/<generation>/  the files of each version
```

`live/<name>` is a symbolic link switched in one step to the new version, so
a service never reads a certificate with the wrong key. Point your service at
`live/<name>/fullchain.pem` and `live/<name>/privkey.pem`. The previous
version stays in `archive/` (`keep_previous`, default 1).

File owner, group and modes come from `agent.yaml`:

```yaml
certificates:
  directory: /etc/inventory-agent/certs
  owner: root
  group: www-data       # lets a web server read its key through the group
  key_mode: "0640"      # 0600 (default) or 0640
```

## Reload the service: the deploy hook

Nothing runs on the host unless you set `certificates.deploy_hook` in
`agent.yaml`; the platform cannot send a command. The hook runs after a new
or renewed certificate is installed. It must be an absolute path to a
root-owned executable that group and others cannot write, in directories
that are root-owned and not writable by group or others. It runs without a
shell or arguments, with variables such as `LCM_CERT_NAME`,
`LCM_FULLCHAIN_PATH`, `LCM_KEY_PATH`, `LCM_DNS_NAMES` and `LCM_IS_RENEWAL`.

```sh
#!/bin/sh
# /usr/local/sbin/reload-nginx.sh  (root:root 0755)
nginx -t && systemctl reload nginx
```

```yaml
certificates:
  deploy_hook: /usr/local/sbin/reload-nginx.sh
  hook_timeout_seconds: 300   # 30-1800
```

Quote the certificate variables in a hook (`"$LCM_DNS_NAMES"`); they come
from the certificate. A hook that exits non-zero leaves the files installed
and the delivery shows *hook failed*.

## Follow deliveries

Open the host and choose **Certificates**.

- **Certificates** lists what is installed: name, common name, serial,
  fingerprint, expiry, state, hook result and the last delivery, with a link
  to the deployer configuration.
- **Delivery history** lists every delivery with its state, trigger and
  attempts.

| State | Meaning |
|---|---|
| queued | waiting for the agent (offline agents get it when they reconnect) |
| sent to agent | the agent was told to fetch it |
| installing | the agent fetched it |
| installed | written on the host |
| unchanged | the same certificate was already installed |
| failed | not installed; the reason is shown |
| hook failed | installed, but the deploy hook failed |
| unsupported | the agent cannot receive certificates, or several agents claim the host |
| superseded | a newer delivery replaced it, or the host already has a later-expiring certificate |
| expired | not picked up or not reported in time |
| cancelled | cancelled by a user, or the certificate, agent or host was revoked or deleted |

With `agents:manage` you can **Cancel** a delivery that has not finished.
When several non-revoked agents claim the same host, nothing is delivered:
revoke the stale agent on the **Agents** page and deploy again.

Related: [manage agents](agents.md), [reference](../reference.md#certificate-delivery-states).
