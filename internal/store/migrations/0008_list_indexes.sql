-- +goose Up
-- Server-side list sorting (go-tangra specs/032-server-side-tables): the host
-- table pages by created_at and, by default, by case-insensitive hostname
-- within a tenant, with the id as tie-breaker. The other host sort fields are
-- served by the existing (tenant_id, <column>) indexes; snapshots and changes
-- by (tenant_id, host_id, collected_at / detected_at DESC).
CREATE INDEX IF NOT EXISTS hosts_created_at     ON inventory_hosts (tenant_id, created_at, id);
CREATE INDEX IF NOT EXISTS hosts_hostname_lower ON inventory_hosts (tenant_id, lower(hostname), id);

-- +goose Down
DROP INDEX IF EXISTS hosts_hostname_lower;
DROP INDEX IF EXISTS hosts_created_at;
