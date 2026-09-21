-- +goose Up
-- Time-series tables. Snapshots are immutable inventory reports partitioned by
-- collected_at; audit events are append-only partitioned by their timestamp.
-- Hypertables carry no unique constraint that excludes the partition column, so
-- neither table has a PRIMARY KEY (ids are app-generated uuid v7, unique in
-- practice) and lookups use plain indexes.

CREATE TABLE inventory_snapshots (
  id            uuid NOT NULL,
  tenant_id     uuid NOT NULL,
  host_id       uuid NOT NULL REFERENCES inventory_hosts(id) ON DELETE CASCADE,
  collected_at  timestamptz NOT NULL,
  received_at   timestamptz NOT NULL DEFAULT now(),
  agent_version text NOT NULL DEFAULT '',
  source        text NOT NULL DEFAULT 'agent' CHECK (source IN ('agent','manual','import')),
  os_name       text NOT NULL DEFAULT '',
  os_version    text NOT NULL DEFAULT '',
  manufacturer  text NOT NULL DEFAULT '',
  model         text NOT NULL DEFAULT '',
  payload       jsonb NOT NULL DEFAULT '{}'::jsonb
);
SELECT create_hypertable('inventory_snapshots', 'collected_at', if_not_exists => TRUE, migrate_data => TRUE);
CREATE INDEX snapshots_host    ON inventory_snapshots (tenant_id, host_id, collected_at DESC);
CREATE INDEX snapshots_id      ON inventory_snapshots (tenant_id, id);
CREATE INDEX snapshots_purge   ON inventory_snapshots (collected_at);

CREATE TABLE inventory_audit_events (
  id            uuid NOT NULL,
  tenant_id     uuid NOT NULL,
  at            timestamptz NOT NULL DEFAULT now(),
  actor_kind    text NOT NULL DEFAULT '',
  actor_id      text NOT NULL DEFAULT '',
  action        text NOT NULL DEFAULT '',
  subject_kind  text NOT NULL DEFAULT '',
  subject_id    text NOT NULL DEFAULT '',
  outcome       text NOT NULL DEFAULT '',
  reason        text NOT NULL DEFAULT '',
  detail        jsonb
);
SELECT create_hypertable('inventory_audit_events', 'at', if_not_exists => TRUE, migrate_data => TRUE);
CREATE INDEX audit_tenant ON inventory_audit_events (tenant_id, at DESC);

-- +goose Down
DROP TABLE IF EXISTS inventory_audit_events;
DROP TABLE IF EXISTS inventory_snapshots;
