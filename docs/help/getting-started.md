---
title: Get started
summary: Issue an enrollment token, install the agent on a Linux server and see the host's inventory appear.
kind: guide
order: 10
tags: [inventory, getting-started, enrollment, agent]
requires: agents:manage
---
This walk-through enrolls one Debian or Ubuntu server. You need
`agents:manage` to issue the token (the *Inventory administrator* role has
it), root on the server, and from your administrator:

- the agent package `tangra-inventory-agent` (`.deb` or `.rpm`) of the
  current release;
- the **ingest endpoint**, the `host:port` agents connect to (port 9977 by
  default).

## 1. Issue an enrollment token

1. Open **Agents** in the menu and choose **Issue enrollment token**.
2. Optionally give it a label, for example the host name, and choose
   **Mint token**.
3. Copy the token. It is shown once, is valid for 24 hours and can enroll one
   agent.

## 2. Install the agent

On the server, as root:

```sh
apt install ./tangra-inventory-agent_<version>_amd64.deb
```

The package installs the agent, a systemd unit and a sample configuration in
`/etc/inventory-agent/agent.yaml`. Set the ingest endpoint there:

```yaml
ingest_endpoint: inventory.example.com:9977
```

If the ingest endpoint uses a certificate from a private CA, also set
`ca_file` to that CA's PEM file.

## 3. Hand over the token and start

```sh
install -m 0600 /dev/stdin /etc/inventory-agent/enrollment.token <<< '<the token>'
systemctl start inventory-agent
```

The agent exchanges the token for its own credential (kept in
`/var/lib/inventory-agent/credential`), collects the inventory and sends the
first snapshot. The token cannot be used again.

## 4. See the host

Open **Hosts**. The server appears with status `active` and agent `online`.
Open it to see the **Hardware**, **Software** and **Network** tabs. The
**Agents** page lists the agent with its version.

From now on the agent reports every hour (`interval_seconds` in its
configuration). **Refresh** on the host page asks it to report now.

## If the host does not appear

- `journalctl -u inventory-agent` shows the agent's errors.
- A refused token (expired, already used or mistyped) needs a new token.
- A TLS error means the agent does not trust the ingest certificate: set
  `ca_file`, or `server_name` when the certificate carries another name.

## Next steps

- [Explore hosts and their history](guides/hosts.md)
- Enroll many hosts without tokens:
  [the enrollment-key example](examples/auto-enroll-servers.md).
