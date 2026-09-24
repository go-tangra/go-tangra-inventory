-- +goose Up
-- Per-tenant row-level security on every inventory_* table. inventory_app is
-- NOBYPASSRLS; every statement runs with app.tenant_id set to the caller's
-- tenant. Trusted worker/maintenance paths set app.system='on' (with the
-- tenant_id pinned to the nil uuid so the uuid cast stays valid) so the policy
-- admits their cross-tenant access.
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'inventory_hosts','inventory_snapshots','inventory_changes','inventory_agents',
    'inventory_enrollment_tokens','inventory_audit_events','inventory_processors',
    'inventory_memory_modules','inventory_disks','inventory_network_interfaces',
    'inventory_software','inventory_services','inventory_monitors'
  ]
  LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY tenant_isolation ON %I USING (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on') WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::uuid OR current_setting('app.system', true) = 'on')$p$, t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO inventory_app', t);
  END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
DECLARE t text;
BEGIN
  FOREACH t IN ARRAY ARRAY[
    'inventory_hosts','inventory_snapshots','inventory_changes','inventory_agents',
    'inventory_enrollment_tokens','inventory_audit_events','inventory_processors',
    'inventory_memory_modules','inventory_disks','inventory_network_interfaces',
    'inventory_software','inventory_services','inventory_monitors'
  ]
  LOOP
    EXECUTE format('DROP POLICY IF EXISTS tenant_isolation ON %I', t);
    EXECUTE format('ALTER TABLE %I DISABLE ROW LEVEL SECURITY', t);
  END LOOP;
END $$;
-- +goose StatementEnd
