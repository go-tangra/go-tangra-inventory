---
title: "Example: enroll a rack of servers with an enrollment key"
summary: Let 40 new Linux servers on one subnet enroll themselves with a single tenant key, limited to that subnet, that number and one month.
kind: example
order: 50
tags: [inventory, example, enrollment, auto-enroll, agents]
requires: agents:manage
---
**Goal.** Forty new Linux servers in the subnet `10.20.30.0/24` are installed
by configuration management. Each should appear in inventory without anyone
issuing forty tokens by hand. Nothing outside that subnet, and nothing after
the rollout, should be able to enroll with the same secret.

**Plan.** One **enrollment key** restricted to the subnet, limited to 40
enrollments and expiring at the end of the month. Configuration management
puts the key on every server together with the agent package.

## 1. Switch on automatic enrollment

In **Agents → Automatic enrollment**, turn on **Allow agents to enroll with
a key**. While it is off, no key works.

## 2. Create the key

Choose **New key**:

| Field | Value |
|---|---|
| Name | `rack-b-rollout` |
| Allowed networks | `10.20.30.0/24` |
| Expires on | the last day of the month |
| Enrollment limit | `40` |

Choose **Create key**. The drawer shows the **Key ID** (`ak_…`) and the
**Secret** (`aks_…`). Copy the secret now: it is shown once. The drawer also
offers the matching `agent.yaml` lines.

## 3. Configure every server

Configuration management installs the package and writes two files on each
server. The secret, mode `0600`:

```sh
install -m 0600 /dev/stdin /etc/inventory-agent/auto-enroll.key <<< '<the aks_ secret>'
```

and `/etc/inventory-agent/agent.yaml`:

```yaml
ingest_endpoint: inventory.example.com:9977
credential_file: /var/lib/inventory-agent/credential
state_file: /var/lib/inventory-agent/state.json
auto_enroll:
  key_id: ak_0123456789abcdef01234567   # the Key ID from step 2
  key_file: /etc/inventory-agent/auto-enroll.key
```

Then `systemctl start inventory-agent`. The agent uses the key only when it
has no credential yet and there is no token file. It never sends the secret:
it signs its request with it, and the request is accepted only from an
address in the key's networks, with a clock within a few minutes of the
platform's, and only once.

## 4. Watch the rollout

On the **Agents** page the new agents appear with **Enrolled with** showing
`key ak_…`. The key's row counts **Enrolled** as `n / 40` and shows when and
from which address it was last used.

Refused enrollments all look the same to the server being installed
(permission denied). Common causes:

- the server reaches the ingest port through NAT or a proxy, so its address
  is not in `10.20.30.0/24`;
- the server's clock is off;
- the key has expired, reached its limit, or automatic enrollment is off.

Your operator can see the exact reason in the audit trail.

## 5. After the rollout

**Delete** the key. Agents that enrolled with it keep working; nothing can
enroll with it any more. If the secret may have leaked during the rollout,
**Rotate secret** instead: enrolled agents keep working, and new enrollments
need the new secret.

Related: [manage agents](../guides/agents.md), [get started](../getting-started.md).
