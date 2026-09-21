-- +goose Up
-- Core inventory tables. Every table carries tenant_id and is RLS-protected
-- (policies + grants applied in 0004_rls.sql). Hypertables live in 0002.

CREATE TABLE inventory_hosts (
  id                uuid PRIMARY KEY,
  tenant_id         uuid NOT NULL,
  hostname          text NOT NULL DEFAULT '',
  machine_id        text NOT NULL DEFAULT '',
  hardware_uuid     text NOT NULL DEFAULT '',
  system_serial     text NOT NULL DEFAULT '',
  identity_key      text NOT NULL DEFAULT '' CHECK (identity_key IN ('', 'hardware_uuid', 'machine_id', 'hostname')),
  manufacturer      text NOT NULL DEFAULT '',
  model             text NOT NULL DEFAULT '',
  os_name           text NOT NULL DEFAULT '',
  os_version        text NOT NULL DEFAULT '',
  os_arch           text NOT NULL DEFAULT '',
  agent_version     text NOT NULL DEFAULT '',
  assigned_user     text NOT NULL DEFAULT '',
  status            text NOT NULL DEFAULT 'active' CHECK (status IN ('active','stale','retired')),
  tags              jsonb NOT NULL DEFAULT '{}'::jsonb,
  first_seen        timestamptz NOT NULL DEFAULT now(),
  last_seen         timestamptz NOT NULL DEFAULT now(),
  last_snapshot_id  text NOT NULL DEFAULT '',
  created_at        timestamptz NOT NULL DEFAULT now(),
  updated_at        timestamptz NOT NULL DEFAULT now()
);
-- Identity uniqueness with precedence: hardware_uuid wins; else machine_id; else
-- hostname. Partial unique indexes enforce exactly one row per stable identity.
CREATE UNIQUE INDEX hosts_uniq_hwuuid   ON inventory_hosts (tenant_id, hardware_uuid) WHERE hardware_uuid <> '';
CREATE UNIQUE INDEX hosts_uniq_machine  ON inventory_hosts (tenant_id, machine_id)    WHERE hardware_uuid = '' AND machine_id <> '';
CREATE UNIQUE INDEX hosts_uniq_hostname ON inventory_hosts (tenant_id, hostname)      WHERE hardware_uuid = '' AND machine_id = '' AND hostname <> '';
CREATE INDEX hosts_hostname     ON inventory_hosts (tenant_id, hostname);
CREATE INDEX hosts_os_name      ON inventory_hosts (tenant_id, os_name);
CREATE INDEX hosts_manufacturer ON inventory_hosts (tenant_id, manufacturer);
CREATE INDEX hosts_status       ON inventory_hosts (tenant_id, status);
CREATE INDEX hosts_last_seen    ON inventory_hosts (tenant_id, last_seen);
CREATE INDEX hosts_tags         ON inventory_hosts USING gin (tags);

CREATE TABLE inventory_agents (
  id                uuid PRIMARY KEY,
  tenant_id         uuid NOT NULL,
  host_id           uuid REFERENCES inventory_hosts(id) ON DELETE SET NULL,
  credential_sealed bytea NOT NULL DEFAULT ''::bytea,
  enrolled_at       timestamptz NOT NULL DEFAULT now(),
  last_seen         timestamptz NOT NULL DEFAULT now(),
  agent_version     text NOT NULL DEFAULT '',
  revoked           boolean NOT NULL DEFAULT false,
  identity_hint     text NOT NULL DEFAULT ''
);
CREATE INDEX agents_tenant ON inventory_agents (tenant_id);
CREATE INDEX agents_host   ON inventory_agents (tenant_id, host_id);

CREATE TABLE inventory_enrollment_tokens (
  id          uuid PRIMARY KEY,
  tenant_id   uuid NOT NULL,
  token_hash  text NOT NULL,
  expires_at  timestamptz NOT NULL,
  used_at     timestamptz,
  revoked     boolean NOT NULL DEFAULT false,
  created_by  text NOT NULL DEFAULT '',
  created_at  timestamptz NOT NULL DEFAULT now(),
  label       text NOT NULL DEFAULT '',
  UNIQUE (token_hash)
);
CREATE INDEX tokens_tenant ON inventory_enrollment_tokens (tenant_id);

CREATE TABLE inventory_changes (
  id                uuid PRIMARY KEY,
  tenant_id         uuid NOT NULL,
  host_id           uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  snapshot_id       uuid NOT NULL,
  prev_snapshot_id  uuid,
  detected_at       timestamptz NOT NULL DEFAULT now(),
  category          text NOT NULL DEFAULT '',
  change_type       text NOT NULL CHECK (change_type IN ('added','removed','modified')),
  component_key     text NOT NULL DEFAULT '',
  before            jsonb,
  after             jsonb
);
CREATE INDEX changes_host     ON inventory_changes (tenant_id, host_id, detected_at DESC);
CREATE INDEX changes_snapshot ON inventory_changes (tenant_id, snapshot_id);

-- +goose Down
DROP TABLE IF EXISTS inventory_changes;
DROP TABLE IF EXISTS inventory_enrollment_tokens;
DROP TABLE IF EXISTS inventory_agents;
DROP TABLE IF EXISTS inventory_hosts;
