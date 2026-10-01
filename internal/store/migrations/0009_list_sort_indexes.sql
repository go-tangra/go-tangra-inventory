-- +goose Up
-- Index-backed list sorting (go-tangra specs/032-server-side-tables perf.md).
-- The host sort fields are NOT NULL, and the list spec marks them NotNull, so
-- ORDER BY carries no NULLS LAST clause: one (tenant_id, <expr>, id) btree
-- serves both directions (backward scan for desc). 0008 covers hostname and
-- created_at; this adds the case-insensitive os_name / manufacturer sorts and
-- id-suffixed status / last_seen indexes (the 0001 ones lack the tie-breaker,
-- so a page sorted by them had to sort every row of a status, or every row
-- sharing a last_seen, by id). The new indexes also serve the equality and
-- range filters of the old ones, which are dropped.
CREATE INDEX IF NOT EXISTS hosts_os_name_lower      ON inventory_hosts (tenant_id, lower(os_name), id);
CREATE INDEX IF NOT EXISTS hosts_manufacturer_lower ON inventory_hosts (tenant_id, lower(manufacturer), id);
CREATE INDEX IF NOT EXISTS hosts_status_id          ON inventory_hosts (tenant_id, status, id);
CREATE INDEX IF NOT EXISTS hosts_last_seen_id       ON inventory_hosts (tenant_id, last_seen, id);
DROP INDEX IF EXISTS hosts_status;
DROP INDEX IF EXISTS hosts_last_seen;
-- A host's changes sorted by kind (change_type).
CREATE INDEX IF NOT EXISTS changes_host_kind        ON inventory_changes (tenant_id, host_id, change_type, id);

-- +goose Down
DROP INDEX IF EXISTS changes_host_kind;
CREATE INDEX IF NOT EXISTS hosts_last_seen ON inventory_hosts (tenant_id, last_seen);
CREATE INDEX IF NOT EXISTS hosts_status    ON inventory_hosts (tenant_id, status);
DROP INDEX IF EXISTS hosts_last_seen_id;
DROP INDEX IF EXISTS hosts_status_id;
DROP INDEX IF EXISTS hosts_manufacturer_lower;
DROP INDEX IF EXISTS hosts_os_name_lower;
