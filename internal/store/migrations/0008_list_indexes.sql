-- +goose Up
-- Server-side list sorting (go-tangra specs/032-server-side-tables): the host
-- table pages by created_at and, by default, by case-insensitive hostname
-- within a tenant, with the id as tie-breaker. The remaining host sort fields
-- (lower(os_name), lower(manufacturer), status, last_seen) get index support
-- in 0009; the (tenant_id, <column>) indexes of 0001 cannot serve the lower()
-- sorts at all. Snapshots and changes page through
-- (tenant_id, host_id, collected_at / detected_at DESC).
CREATE INDEX IF NOT EXISTS hosts_created_at     ON inventory_hosts (tenant_id, created_at, id);
CREATE INDEX IF NOT EXISTS hosts_hostname_lower ON inventory_hosts (tenant_id, lower(hostname), id);

-- +goose Down
DROP INDEX IF EXISTS hosts_hostname_lower;
DROP INDEX IF EXISTS hosts_created_at;
