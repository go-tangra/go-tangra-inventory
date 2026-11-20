-- +goose Up
-- Feature 038: Teleport SSH-node enrollment of inventory hosts. The inventory
-- keeps references only (source, enrollment id, host, cluster, state); the
-- join material (a single-use provision token) is rendered by the source
-- (the teleport module) at the moment an agent pulls its item, relayed and
-- dropped: no column here holds a token, a CA pin set or labels (asserted by
-- the integration test).
CREATE TABLE inventory_teleport_join_items (
  id             uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  source         text NOT NULL CHECK (source ~ '^[a-z][a-z0-9-]{0,62}$'),
  enrollment_id  uuid NOT NULL,
  host_id        uuid NOT NULL,
  agent_id       uuid,
  cluster_id     uuid NOT NULL,
  state          text NOT NULL CHECK (state IN ('pending','delivered','fetched','installing','joined','failed',
                   'unsupported','superseded','expired','cancelled')),
  reason         text NOT NULL DEFAULT '' CHECK (reason ~ '^[a-z_]{0,32}$'),
  node_host_uuid text NOT NULL DEFAULT '' CHECK (node_host_uuid ~ '^([0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})?$'),
  attempt        integer NOT NULL DEFAULT 1 CHECK (attempt BETWEEN 1 AND 5),
  fetches        integer NOT NULL DEFAULT 0 CHECK (fetches BETWEEN 0 AND 5),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  expires_at     timestamptz NOT NULL,
  delivered_at   timestamptz,
  fetched_at     timestamptz,
  reported_at    timestamptz,
  UNIQUE (tenant_id, source, enrollment_id)
);
-- One active item per host and cluster (a newer enrollment supersedes the older).
CREATE UNIQUE INDEX inventory_teleport_items_active_uq ON inventory_teleport_join_items (tenant_id, host_id, cluster_id)
  WHERE state IN ('pending','delivered','fetched','installing');
-- Replay on connect: an agent's active items, oldest first.
CREATE INDEX inventory_teleport_items_agent_active ON inventory_teleport_join_items (tenant_id, agent_id, created_at)
  WHERE state IN ('pending','delivered','fetched','installing');
CREATE INDEX inventory_teleport_items_sweep ON inventory_teleport_join_items (state, updated_at, expires_at)
  WHERE state IN ('pending','delivered','fetched','installing');
CREATE INDEX inventory_teleport_items_purge ON inventory_teleport_join_items (updated_at)
  WHERE state NOT IN ('pending','delivered','fetched','installing');

-- RLS, same policy as 0004_rls.sql, 0010_cert_delivery.sql and 0012_module_delivery.sql.
ALTER TABLE inventory_teleport_join_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_teleport_join_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON inventory_teleport_join_items
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on');
GRANT SELECT, INSERT, UPDATE, DELETE ON inventory_teleport_join_items TO inventory_app;

-- +goose Down
DROP TABLE IF EXISTS inventory_teleport_join_items;
