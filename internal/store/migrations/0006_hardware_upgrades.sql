-- +goose Up
-- Feature 023: hardware details and agent self-upgrade.

-- Component tables: new hardware columns (the snapshot payload stays
-- authoritative). type_detail holds the DSP0134 type-detail names joined by
-- ", "; populated defaults to true because agents before 023 reported
-- populated modules only.
ALTER TABLE inventory_memory_modules
  ADD COLUMN populated   boolean NOT NULL DEFAULT true,
  ADD COLUMN type_detail text    NOT NULL DEFAULT '';
ALTER TABLE inventory_disks
  ADD COLUMN name      text    NOT NULL DEFAULT '',
  ADD COLUMN removable boolean NOT NULL DEFAULT false;
ALTER TABLE inventory_processors
  ADD COLUMN family text NOT NULL DEFAULT '';

-- Agent platform, reported on StreamCommands.
ALTER TABLE inventory_agents
  ADD COLUMN os               text   NOT NULL DEFAULT '' CHECK (os IN ('', 'linux', 'windows')),
  ADD COLUMN arch             text   NOT NULL DEFAULT '' CHECK (arch IN ('', 'amd64', 'arm64')),
  ADD COLUMN install_type     text   NOT NULL DEFAULT '' CHECK (install_type IN ('', 'deb', 'rpm', 'binary')),
  ADD COLUMN capabilities     text[] NOT NULL DEFAULT '{}' CHECK (cardinality(capabilities) <= 16),
  ADD COLUMN platform_seen_at timestamptz;

-- Agent releases: GLOBAL tables (no tenant_id, no RLS) holding public, signed
-- binaries. Only the startup seeder and `inventorysvc agent-release import`
-- write them; every tenant scope may read them.
CREATE TABLE inventory_agent_releases (
  version         text PRIMARY KEY CHECK (version ~ '^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$' AND length(version) <= 64),
  manifest        bytea NOT NULL CHECK (octet_length(manifest) <= 65536),
  signature       bytea NOT NULL CHECK (octet_length(signature) = 64),
  key_id          text  NOT NULL CHECK (key_id ~ '^[a-z0-9-]{1,32}$'),
  manifest_sha256 text  NOT NULL CHECK (manifest_sha256 ~ '^[0-9a-f]{64}$'),
  source          text  NOT NULL CHECK (source IN ('bundled', 'import')),
  imported_at     timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE inventory_agent_artifacts (
  version      text   NOT NULL REFERENCES inventory_agent_releases(version) ON DELETE CASCADE,
  os           text   NOT NULL CHECK (os IN ('linux', 'windows')),
  arch         text   NOT NULL CHECK (arch IN ('amd64', 'arm64')),
  install_type text   NOT NULL CHECK (install_type IN ('deb', 'rpm', 'binary')),
  file         text   NOT NULL CHECK (file ~ '^[A-Za-z0-9._+~-]{1,128}$' AND file NOT IN ('.', '..')),
  size         bigint NOT NULL CHECK (size > 0 AND size <= 157286400),
  sha256       text   NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
  complete     boolean NOT NULL DEFAULT false,
  PRIMARY KEY (version, os, arch, install_type)
);
CREATE TABLE inventory_agent_artifact_chunks (
  version      text    NOT NULL,
  os           text    NOT NULL,
  arch         text    NOT NULL,
  install_type text    NOT NULL,
  seq          integer NOT NULL CHECK (seq >= 0),
  data         bytea   NOT NULL CHECK (octet_length(data) BETWEEN 1 AND 1048576),
  PRIMARY KEY (version, os, arch, install_type, seq),
  FOREIGN KEY (version, os, arch, install_type)
    REFERENCES inventory_agent_artifacts (version, os, arch, install_type) ON DELETE CASCADE
);
GRANT SELECT, INSERT, UPDATE, DELETE ON inventory_agent_releases, inventory_agent_artifacts, inventory_agent_artifact_chunks TO inventory_app;

-- Upgrade requests (tenant-scoped).
CREATE TABLE inventory_agent_upgrades (
  id              uuid PRIMARY KEY,
  tenant_id       uuid NOT NULL,
  agent_id        uuid NOT NULL REFERENCES inventory_agents(id) ON DELETE CASCADE,
  host_id         uuid REFERENCES inventory_hosts(id) ON DELETE SET NULL,
  from_version    text NOT NULL DEFAULT '' CHECK (length(from_version) <= 128),
  target_version  text NOT NULL CHECK (length(target_version) BETWEEN 1 AND 64),
  allow_downgrade boolean NOT NULL DEFAULT false,
  state           text NOT NULL CHECK (state IN ('pending','delivered','downloading','installing',
                                                 'succeeded','failed','rolled_back','expired','cancelled')),
  origin          text NOT NULL CHECK (origin IN ('user','policy','agent')),
  requested_by    text NOT NULL DEFAULT '' CHECK (length(requested_by) <= 128),
  reason          text NOT NULL DEFAULT '' CHECK (reason ~ '^[a-z_]{0,32}$'),
  attempts        integer NOT NULL DEFAULT 0,
  created_at      timestamptz NOT NULL DEFAULT now(),
  updated_at      timestamptz NOT NULL DEFAULT now(),
  expires_at      timestamptz NOT NULL,
  delivered_at    timestamptz,
  started_at      timestamptz,
  finished_at     timestamptz
);
CREATE UNIQUE INDEX agent_upgrades_one_active ON inventory_agent_upgrades (tenant_id, agent_id)
  WHERE state IN ('pending','delivered','downloading','installing');
CREATE INDEX agent_upgrades_tenant_state ON inventory_agent_upgrades (tenant_id, state, updated_at);
CREATE INDEX agent_upgrades_agent ON inventory_agent_upgrades (tenant_id, agent_id, created_at DESC);
CREATE INDEX agent_upgrades_expiry ON inventory_agent_upgrades (expires_at)
  WHERE state IN ('pending','delivered');
CREATE INDEX agent_upgrades_progress ON inventory_agent_upgrades (updated_at)
  WHERE state IN ('downloading','installing');

-- Automatic upgrade policy (tenant-scoped, one row per tenant).
CREATE TABLE inventory_agent_upgrade_policy (
  tenant_id      uuid PRIMARY KEY,
  enabled        boolean NOT NULL DEFAULT false,
  window_start   text NOT NULL DEFAULT '02:00' CHECK (window_start ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  window_end     text NOT NULL DEFAULT '04:00' CHECK (window_end   ~ '^([01][0-9]|2[0-3]):[0-5][0-9]$'),
  timezone       text NOT NULL DEFAULT 'UTC' CHECK (length(timezone) BETWEEN 1 AND 64),
  max_concurrent integer NOT NULL DEFAULT 5 CHECK (max_concurrent BETWEEN 1 AND 100),
  target_version text NOT NULL DEFAULT '' CHECK (length(target_version) <= 64),
  paused         boolean NOT NULL DEFAULT false,
  paused_reason  text NOT NULL DEFAULT '' CHECK (length(paused_reason) <= 128),
  updated_by     text NOT NULL DEFAULT '' CHECK (length(updated_by) <= 128),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- RLS for the two tenant tables, same policy as 0004_rls.sql.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY['inventory_agent_upgrades','inventory_agent_upgrade_policy']
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on') WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')$p$, t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO inventory_app', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TABLE IF EXISTS inventory_agent_upgrade_policy;
DROP TABLE IF EXISTS inventory_agent_upgrades;
DROP TABLE IF EXISTS inventory_agent_artifact_chunks;
DROP TABLE IF EXISTS inventory_agent_artifacts;
DROP TABLE IF EXISTS inventory_agent_releases;
ALTER TABLE inventory_agents
  DROP COLUMN IF EXISTS platform_seen_at, DROP COLUMN IF EXISTS capabilities,
  DROP COLUMN IF EXISTS install_type, DROP COLUMN IF EXISTS arch, DROP COLUMN IF EXISTS os;
ALTER TABLE inventory_processors DROP COLUMN IF EXISTS family;
ALTER TABLE inventory_disks DROP COLUMN IF EXISTS removable, DROP COLUMN IF EXISTS name;
ALTER TABLE inventory_memory_modules DROP COLUMN IF EXISTS type_detail, DROP COLUMN IF EXISTS populated;
