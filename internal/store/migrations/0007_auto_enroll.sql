-- +goose Up
-- Automatic agent enrollment (feature 029): a per-tenant master switch and
-- reusable auto-enrollment keys. A key's secret is sealed with the module
-- envelope (it is needed in clear to verify HMAC proofs); only the public
-- key_id is ever shown again. Nonces of accepted proofs are kept for the
-- freshness window so a captured request cannot be replayed.
CREATE TABLE inventory_auto_enroll_settings (
  tenant_id  uuid PRIMARY KEY,
  enabled    boolean NOT NULL DEFAULT false,
  updated_by text NOT NULL DEFAULT '' CHECK (length(updated_by) <= 128),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE inventory_auto_enroll_keys (
  id              uuid PRIMARY KEY,
  tenant_id       uuid NOT NULL,
  key_id          text NOT NULL UNIQUE CHECK (key_id ~ '^ak_[0-9a-f]{24}$'),
  name            text NOT NULL CHECK (length(name) BETWEEN 1 AND 100),
  secret_sealed   bytea NOT NULL,
  allowed_cidrs   cidr[] NOT NULL CHECK (cardinality(allowed_cidrs) BETWEEN 1 AND 32),
  enabled         boolean NOT NULL DEFAULT true,
  expires_at      timestamptz,
  max_enrollments integer NOT NULL DEFAULT 0 CHECK (max_enrollments >= 0),
  enrollments     integer NOT NULL DEFAULT 0 CHECK (enrollments >= 0),
  last_used_at    timestamptz,
  last_used_ip    text NOT NULL DEFAULT '',
  created_by      text NOT NULL DEFAULT '' CHECK (length(created_by) <= 128),
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, name)
);
CREATE INDEX auto_enroll_keys_tenant ON inventory_auto_enroll_keys (tenant_id);

CREATE TABLE inventory_auto_enroll_nonces (
  tenant_id uuid NOT NULL,
  key_id    text NOT NULL,
  nonce     text NOT NULL,
  seen_at   timestamptz NOT NULL,
  PRIMARY KEY (key_id, nonce)
);
CREATE INDEX auto_enroll_nonces_seen ON inventory_auto_enroll_nonces (key_id, seen_at);

-- How an agent enrolled: 'token' or 'auto' (with the key's public id).
ALTER TABLE inventory_agents
  ADD COLUMN enrolled_via text NOT NULL DEFAULT 'token' CHECK (enrolled_via IN ('token','auto')),
  ADD COLUMN auto_enroll_key_id text NOT NULL DEFAULT '';

-- RLS, same policy as 0004_rls.sql.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['inventory_auto_enroll_settings','inventory_auto_enroll_keys','inventory_auto_enroll_nonces']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on') WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')$p$, t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO inventory_app', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
ALTER TABLE inventory_agents DROP COLUMN IF EXISTS auto_enroll_key_id, DROP COLUMN IF EXISTS enrolled_via;
DROP TABLE IF EXISTS inventory_auto_enroll_nonces;
DROP TABLE IF EXISTS inventory_auto_enroll_keys;
DROP TABLE IF EXISTS inventory_auto_enroll_settings;
