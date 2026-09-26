-- +goose Up
-- Host report change tracking (feature 020): the digest of each host's report
-- projection and when it last changed. HostReportService lists tenants and
-- hosts changed since a watermark from these columns.
ALTER TABLE inventory_hosts
  ADD COLUMN report_digest     text NOT NULL DEFAULT '' CHECK (report_digest = '' OR report_digest ~ '^[0-9a-f]{64}$'),
  ADD COLUMN report_changed_at timestamptz;
CREATE INDEX hosts_report_changed        ON inventory_hosts (tenant_id, report_changed_at);
-- ListReportTenants (system scope) scans by change time across tenants.
CREATE INDEX hosts_report_changed_global ON inventory_hosts (report_changed_at);

-- +goose Down
DROP INDEX IF EXISTS hosts_report_changed_global;
DROP INDEX IF EXISTS hosts_report_changed;
ALTER TABLE inventory_hosts DROP COLUMN IF EXISTS report_changed_at, DROP COLUMN IF EXISTS report_digest;
