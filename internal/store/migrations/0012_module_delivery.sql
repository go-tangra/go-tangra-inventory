-- +goose Up
-- Feature 037: module bundle delivery to inventory agents. The inventory
-- keeps references only (source, delivery id, host, module, version, state);
-- the bundle is rendered by the source (the gateway) at the moment an agent
-- pulls its item, relayed and dropped: no column here holds bundle bytes, a
-- token or a password (asserted by the integration test).
CREATE TABLE inventory_module_delivery_items (
  id             uuid PRIMARY KEY,
  tenant_id      uuid NOT NULL,
  source         text NOT NULL CHECK (source ~ '^[a-z][a-z0-9-]{0,62}$'),
  delivery_id    uuid NOT NULL,
  host_id        uuid NOT NULL,
  agent_id       uuid,
  module         text NOT NULL CHECK (module ~ '^[a-z0-9][a-z0-9-]{0,62}$'),
  version        text NOT NULL CHECK (length(version) BETWEEN 1 AND 64),
  state          text NOT NULL CHECK (state IN ('pending','delivered','fetched','installed','failed','hook_failed',
                   'unsupported','superseded','expired')),
  reason         text NOT NULL DEFAULT '' CHECK (reason ~ '^[a-z_]{0,32}$'),
  hook_exit_code integer NOT NULL DEFAULT -1 CHECK (hook_exit_code BETWEEN -1 AND 256),
  attempt        integer NOT NULL DEFAULT 1 CHECK (attempt BETWEEN 1 AND 5),
  fetches        integer NOT NULL DEFAULT 0 CHECK (fetches BETWEEN 0 AND 5),
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now(),
  expires_at     timestamptz NOT NULL,
  delivered_at   timestamptz,
  fetched_at     timestamptz,
  reported_at    timestamptz,
  UNIQUE (tenant_id, source, delivery_id)
);
-- One active item per host and module (a new delivery supersedes the older).
CREATE UNIQUE INDEX inventory_module_items_active_uq ON inventory_module_delivery_items (tenant_id, host_id, module)
  WHERE state IN ('pending','delivered','fetched');
-- Replay on connect: an agent's active items, oldest first.
CREATE INDEX inventory_module_items_agent_active ON inventory_module_delivery_items (tenant_id, agent_id, created_at)
  WHERE state IN ('pending','delivered','fetched');
CREATE INDEX inventory_module_items_sweep ON inventory_module_delivery_items (state, updated_at, expires_at)
  WHERE state IN ('pending','delivered','fetched');
CREATE INDEX inventory_module_items_purge ON inventory_module_delivery_items (updated_at)
  WHERE state NOT IN ('pending','delivered','fetched');

-- RLS, same policy as 0004_rls.sql and 0010_cert_delivery.sql.
ALTER TABLE inventory_module_delivery_items ENABLE ROW LEVEL SECURITY;
ALTER TABLE inventory_module_delivery_items FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON inventory_module_delivery_items
  USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')
  WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on');
GRANT SELECT, INSERT, UPDATE, DELETE ON inventory_module_delivery_items TO inventory_app;

-- +goose Down
DROP TABLE IF EXISTS inventory_module_delivery_items;
