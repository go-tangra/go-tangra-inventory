-- +goose Up
-- Feature 033: certificate delivery to inventory agents. The inventory keeps
-- references and identity only (certificate id, serial, fingerprint, CN,
-- expiry); certificate, chain and key material are fetched from lcm at the
-- moment an agent pulls its item and never stored: no table here has a
-- material or bytea column (asserted by the integration test).

-- One delivery request per (tenant, source, idempotency key = deployer job).
CREATE TABLE inventory_cert_deliveries (
  id               uuid PRIMARY KEY,
  tenant_id        uuid NOT NULL,
  source           text NOT NULL CHECK (source ~ '^[a-z][a-z0-9-]{0,62}$'),
  idempotency_key  text NOT NULL CHECK (length(idempotency_key) BETWEEN 1 AND 128),
  configuration_id text NOT NULL DEFAULT '' CHECK (length(configuration_id) <= 128),
  target_id        text NOT NULL DEFAULT '' CHECK (length(target_id) <= 128),
  trigger          text NOT NULL CHECK (trigger IN ('manual','auto_deploy','retry')),
  certificate_id   text NOT NULL CHECK (length(certificate_id) BETWEEN 1 AND 128),
  name             text NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$' AND position('..' in name) = 0),
  key_policy       text NOT NULL CHECK (key_policy IN ('require','certificate_only')),
  host_ids         uuid[] NOT NULL DEFAULT '{}' CHECK (cardinality(host_ids) <= 1000),
  host_tags        text[] NOT NULL DEFAULT '{}' CHECK (cardinality(host_tags) <= 16),
  requested_by     text NOT NULL CHECK (length(requested_by) <= 512),
  created_at       timestamptz NOT NULL DEFAULT now(),
  expires_at       timestamptz NOT NULL,
  UNIQUE (tenant_id, source, idempotency_key)
);

-- One item per host of a delivery; the item id is the only id agents see.
CREATE TABLE inventory_cert_delivery_items (
  id                 uuid PRIMARY KEY,
  tenant_id          uuid NOT NULL,
  delivery_id        uuid NOT NULL REFERENCES inventory_cert_deliveries(id) ON DELETE CASCADE,
  host_id            uuid NOT NULL,
  agent_id           uuid,
  name               text NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$' AND position('..' in name) = 0),
  certificate_id     text NOT NULL CHECK (length(certificate_id) BETWEEN 1 AND 128),
  state              text NOT NULL CHECK (state IN ('pending','delivered','fetched','installed','unchanged',
                       'failed','hook_failed','unsupported','superseded','expired','cancelled')),
  reason             text NOT NULL DEFAULT '' CHECK (reason ~ '^[a-z_]{0,32}$'),
  attempts           integer NOT NULL DEFAULT 1 CHECK (attempts BETWEEN 1 AND 5),
  fetches            integer NOT NULL DEFAULT 0 CHECK (fetches BETWEEN 0 AND 5),
  rerun_hook         boolean NOT NULL DEFAULT false,
  serial             text NOT NULL DEFAULT '' CHECK (serial ~ '^[0-9a-fA-F:]{0,128}$'),
  fingerprint_sha256 text NOT NULL DEFAULT '' CHECK (fingerprint_sha256 ~ '^([0-9a-f]{64})?$'),
  not_after          timestamptz,
  hook_exit_code     integer CHECK (hook_exit_code BETWEEN -1 AND 256),
  detail             text NOT NULL DEFAULT '' CHECK (octet_length(detail) <= 256),
  created_at         timestamptz NOT NULL DEFAULT now(),
  updated_at         timestamptz NOT NULL DEFAULT now(),
  delivered_at       timestamptz,
  fetched_at         timestamptz,
  finished_at        timestamptz,
  UNIQUE (delivery_id, host_id)
);
-- One active item per host and name (research D8).
CREATE UNIQUE INDEX inventory_cert_items_active_uq ON inventory_cert_delivery_items (tenant_id, host_id, name)
  WHERE state IN ('pending','delivered','fetched');
-- Replay on connect: an agent's active items, oldest first.
CREATE INDEX inventory_cert_items_agent_active ON inventory_cert_delivery_items (tenant_id, agent_id, created_at)
  WHERE state IN ('pending','delivered','fetched');
CREATE INDEX inventory_cert_items_host ON inventory_cert_delivery_items (tenant_id, host_id, created_at DESC);
CREATE INDEX inventory_cert_items_cert ON inventory_cert_delivery_items (tenant_id, certificate_id);
CREATE INDEX inventory_cert_items_delivery ON inventory_cert_delivery_items (delivery_id, created_at);
CREATE INDEX inventory_cert_items_list ON inventory_cert_delivery_items (tenant_id, created_at, id);
CREATE INDEX inventory_cert_items_sweep ON inventory_cert_delivery_items (state, updated_at)
  WHERE state IN ('pending','delivered','fetched');
CREATE INDEX inventory_cert_items_purge ON inventory_cert_delivery_items (updated_at)
  WHERE state NOT IN ('pending','delivered','fetched');

-- Current certificate per host and name.
CREATE TABLE inventory_host_certificates (
  tenant_id          uuid NOT NULL,
  host_id            uuid NOT NULL,
  name               text NOT NULL CHECK (name ~ '^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$' AND position('..' in name) = 0),
  certificate_id     text NOT NULL CHECK (length(certificate_id) BETWEEN 1 AND 128),
  configuration_id   text NOT NULL DEFAULT '' CHECK (length(configuration_id) <= 128),
  common_name        text NOT NULL DEFAULT '' CHECK (octet_length(common_name) <= 256),
  serial             text NOT NULL DEFAULT '' CHECK (serial ~ '^[0-9a-fA-F:]{0,128}$'),
  fingerprint_sha256 text NOT NULL DEFAULT '' CHECK (fingerprint_sha256 ~ '^([0-9a-f]{64})?$'),
  not_after          timestamptz,
  state              text NOT NULL CHECK (state IN ('installed','unchanged','failed','hook_failed','unsupported',
                       'superseded','expired','cancelled')),
  reason             text NOT NULL DEFAULT '' CHECK (reason ~ '^[a-z_]{0,32}$'),
  hook_exit_code     integer CHECK (hook_exit_code BETWEEN -1 AND 256),
  last_item_id       uuid NOT NULL,
  last_delivered_at  timestamptz,
  revoked_at         timestamptz,
  updated_at         timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, host_id, name)
);
CREATE INDEX inventory_host_certs_cert ON inventory_host_certificates (tenant_id, certificate_id);

-- RLS for the three tenant tables, same policy as 0004_rls.sql.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['inventory_cert_deliveries','inventory_cert_delivery_items','inventory_host_certificates']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on') WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')$p$, t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO inventory_app', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS inventory_host_certificates;
DROP TABLE IF EXISTS inventory_cert_delivery_items;
DROP TABLE IF EXISTS inventory_cert_deliveries;
