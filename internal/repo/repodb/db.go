// Package repodb binds repo.Store to TimescaleDB via *store.Store. Tenant-scoped
// calls run in a tenant transaction (RLS); system-scoped calls (stale marking,
// retention purge, ingest-auth agent lookup/touch, token consume, tenant
// enumeration, audit) run under the system-scope pin. InsertSnapshot writes the
// snapshot row and the normalized component child rows in one transaction.
package repodb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// DB implements repo.Store over *store.Store.
type DB struct {
	St *store.Store
}

// New wraps the store.
func New(st *store.Store) *DB { return &DB{St: st} }

// Close releases the underlying pool.
func (d *DB) Close() { d.St.Close() }

func (d *DB) tenant(ctx context.Context, tid string, fn func(tx pgx.Tx) error) error {
	return d.St.Tx(ctx, store.Scope{TenantID: tid}, fn)
}
func (d *DB) system(ctx context.Context, fn func(tx pgx.Tx) error) error {
	return d.St.Tx(ctx, store.Scope{System: true}, fn)
}

// scanner is satisfied by both pgx.Row and pgx.Rows.
type scanner interface{ Scan(dest ...any) error }

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return repo.ErrNotFound
	}
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return repo.ErrConflict
	}
	return err
}

func toi64(u uint64) int64 { return int64(u) } // #nosec G115 -- domain values fit int64

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

// ---- hosts

const hostCols = `id, tenant_id, hostname, machine_id, hardware_uuid, system_serial,
	identity_key, manufacturer, model, os_name, os_version, os_arch, agent_version,
	assigned_user, status, tags, first_seen, last_seen, last_snapshot_id, created_at, updated_at,
	report_digest, report_changed_at`

func scanHost(sc scanner) (store.Host, error) {
	var h store.Host
	var tags []byte
	var changedAt *time.Time
	if err := sc.Scan(&h.ID, &h.TenantID, &h.Hostname, &h.MachineID, &h.HardwareUUID,
		&h.SystemSerial, &h.IdentityKey, &h.Manufacturer, &h.Model, &h.OSName, &h.OSVersion,
		&h.OSArch, &h.AgentVersion, &h.AssignedUser, &h.Status, &tags, &h.FirstSeen,
		&h.LastSeen, &h.LastSnapshotID, &h.CreatedAt, &h.UpdatedAt,
		&h.ReportDigest, &changedAt); err != nil {
		return store.Host{}, err
	}
	if changedAt != nil {
		h.ReportChangedAt = changedAt.UTC()
	}
	if len(tags) > 0 {
		_ = json.Unmarshal(tags, &h.Tags)
	}
	if h.Tags == nil {
		h.Tags = map[string]string{}
	}
	return h, nil
}

func identityKey(hwUUID, machineID, hostname string) string {
	switch {
	case hwUUID != "":
		return store.IdentityHardwareUUID
	case machineID != "":
		return store.IdentityMachineID
	default:
		return store.IdentityHostname
	}
}

// selectByIdentity finds a host row matching the identity precedence, locking it
// FOR UPDATE when lock is set (used by the upsert path).
func selectByIdentity(ctx context.Context, tx pgx.Tx, tenantID, hwUUID, machineID, hostname string, lock bool) (store.Host, error) {
	var where string
	var val string
	switch identityKey(hwUUID, machineID, hostname) {
	case store.IdentityHardwareUUID:
		where, val = "hardware_uuid = $2", hwUUID
	case store.IdentityMachineID:
		where, val = "hardware_uuid = '' AND machine_id = $2", machineID
	default:
		where, val = "hardware_uuid = '' AND machine_id = '' AND hostname = $2", hostname
	}
	q := "SELECT " + hostCols + " FROM inventory_hosts WHERE tenant_id = $1 AND " + where
	if lock {
		q += " FOR UPDATE"
	}
	return scanHost(tx.QueryRow(ctx, q, tenantID, val))
}

func (d *DB) ResolveHost(ctx context.Context, tenantID string, h store.Host) (out store.Host, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		now := time.Now().UTC()
		key := identityKey(h.HardwareUUID, h.MachineID, h.Hostname)
		existing, e := selectByIdentity(ctx, tx, tenantID, h.HardwareUUID, h.MachineID, h.Hostname, true)
		if e != nil && !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		if errors.Is(e, pgx.ErrNoRows) {
			id := h.ID
			if id == "" {
				id = store.NewID()
			}
			status := h.Status
			if status == "" {
				status = store.HostActive
			}
			first := h.FirstSeen
			if first.IsZero() {
				first = now
			}
			last := h.LastSeen
			if last.IsZero() {
				last = now
			}
			row := tx.QueryRow(ctx, `INSERT INTO inventory_hosts
				(id, tenant_id, hostname, machine_id, hardware_uuid, system_serial, identity_key,
				 manufacturer, model, os_name, os_version, os_arch, agent_version, assigned_user,
				 status, tags, first_seen, last_seen, last_snapshot_id, created_at, updated_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16::jsonb,$17,$18,$19,$20,$20)
				RETURNING `+hostCols,
				id, tenantID, h.Hostname, h.MachineID, h.HardwareUUID, h.SystemSerial, key,
				h.Manufacturer, h.Model, h.OSName, h.OSVersion, h.OSArch, h.AgentVersion, h.AssignedUser,
				status, mustJSON(nz(h.Tags)), first, last, h.LastSnapshotID, now)
			out, e = scanHost(row)
			return mapErr(e)
		}
		// Merge non-empty summary fields onto the existing row.
		m := mergeHost(existing, h)
		m.IdentityKey = key
		if !h.LastSeen.IsZero() && h.LastSeen.After(m.LastSeen) {
			m.LastSeen = h.LastSeen
		} else if h.LastSeen.IsZero() {
			m.LastSeen = now
		}
		row := tx.QueryRow(ctx, `UPDATE inventory_hosts SET
			hostname=$3, machine_id=$4, hardware_uuid=$5, system_serial=$6, identity_key=$7,
			manufacturer=$8, model=$9, os_name=$10, os_version=$11, os_arch=$12, agent_version=$13,
			assigned_user=$14, status=$15, tags=$16::jsonb, last_seen=$17, last_snapshot_id=$18, updated_at=$19
			WHERE tenant_id=$1 AND id=$2 RETURNING `+hostCols,
			tenantID, existing.ID, m.Hostname, m.MachineID, m.HardwareUUID, m.SystemSerial, m.IdentityKey,
			m.Manufacturer, m.Model, m.OSName, m.OSVersion, m.OSArch, m.AgentVersion, m.AssignedUser,
			m.Status, mustJSON(nz(m.Tags)), m.LastSeen, m.LastSnapshotID, now)
		out, e = scanHost(row)
		return mapErr(e)
	})
	return
}

func nz(t map[string]string) map[string]string {
	if t == nil {
		return map[string]string{}
	}
	return t
}

func mergeHost(dst, src store.Host) store.Host {
	set := func(d *string, s string) {
		if s != "" {
			*d = s
		}
	}
	set(&dst.Hostname, src.Hostname)
	set(&dst.MachineID, src.MachineID)
	set(&dst.HardwareUUID, src.HardwareUUID)
	set(&dst.SystemSerial, src.SystemSerial)
	set(&dst.Manufacturer, src.Manufacturer)
	set(&dst.Model, src.Model)
	set(&dst.OSName, src.OSName)
	set(&dst.OSVersion, src.OSVersion)
	set(&dst.OSArch, src.OSArch)
	set(&dst.AgentVersion, src.AgentVersion)
	set(&dst.AssignedUser, src.AssignedUser)
	set(&dst.Status, src.Status)
	set(&dst.LastSnapshotID, src.LastSnapshotID)
	if src.Tags != nil {
		dst.Tags = src.Tags
	}
	return dst
}

func (d *DB) GetHost(ctx context.Context, tenantID, id string) (out store.Host, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanHost(tx.QueryRow(ctx, "SELECT "+hostCols+" FROM inventory_hosts WHERE tenant_id=$1 AND id=$2", tenantID, id))
		return mapErr(e)
	})
	return
}

func (d *DB) GetHostByIdentity(ctx context.Context, tenantID string, id store.Identity) (out store.Host, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = selectByIdentity(ctx, tx, tenantID, id.HardwareUUID, id.MachineID, id.Hostname, false)
		return mapErr(e)
	})
	return
}

func (d *DB) ListHosts(ctx context.Context, tenantID string, f store.HostFilter) (out []store.Host, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var b strings.Builder
		b.WriteString("SELECT " + hostCols + " FROM inventory_hosts WHERE tenant_id=$1")
		args := []any{tenantID}
		add := func(cond string, val any) {
			args = append(args, val)
			b.WriteString(fmt.Sprintf(cond, len(args)))
		}
		if f.Hostname != "" {
			add(" AND hostname ILIKE $%d", "%"+f.Hostname+"%")
		}
		if f.OSName != "" {
			add(" AND os_name = $%d", f.OSName)
		}
		if f.Manufacturer != "" {
			add(" AND manufacturer = $%d", f.Manufacturer)
		}
		if f.Status != "" {
			add(" AND status = $%d", f.Status)
		}
		if f.Tag != "" {
			if i := strings.IndexByte(f.Tag, '='); i >= 0 {
				add(" AND tags @> $%d::jsonb", mustJSON(map[string]string{f.Tag[:i]: f.Tag[i+1:]}))
			} else {
				add(" AND (tags ->> $%d) IS NOT NULL", f.Tag)
			}
		}
		if f.LastSeenFrom != nil {
			add(" AND last_seen >= $%d", *f.LastSeenFrom)
		}
		if f.LastSeenTo != nil {
			add(" AND last_seen <= $%d", *f.LastSeenTo)
		}
		if f.CursorID != "" {
			add(" AND id < $%d", f.CursorID)
		}
		b.WriteString(" ORDER BY id DESC")
		if f.Limit > 0 {
			add(" LIMIT $%d", f.Limit)
		}
		rows, e := tx.Query(ctx, b.String(), args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			h, e := scanHost(rows)
			if e != nil {
				return e
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return
}

func (d *DB) SetHostTags(ctx context.Context, tenantID, id string, tags map[string]string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, "UPDATE inventory_hosts SET tags=$3::jsonb, updated_at=now() WHERE tenant_id=$1 AND id=$2",
			tenantID, id, mustJSON(nz(tags)))
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

func (d *DB) RetireHost(ctx context.Context, tenantID, id string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		// Status is part of the host report: invalidate the digest (recomputed
		// on read) and bump the report change time.
		ct, e := tx.Exec(ctx, `UPDATE inventory_hosts SET status=$3, updated_at=now(),
			report_digest='', report_changed_at=date_trunc('milliseconds', now())
			WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, store.HostRetired)
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

func (d *DB) DeleteHost(ctx context.Context, tenantID, id string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, "DELETE FROM inventory_hosts WHERE tenant_id=$1 AND id=$2", tenantID, id)
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

func (d *DB) MarkStaleHosts(ctx context.Context, olderThan time.Time) (n int64, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, "UPDATE inventory_hosts SET status=$1, updated_at=now() WHERE status=$2 AND last_seen < $3",
			store.HostStale, store.HostActive, olderThan)
		if e != nil {
			return e
		}
		n = ct.RowsAffected()
		return nil
	})
	return
}

// ---- snapshots

const snapCols = `id, tenant_id, host_id, collected_at, received_at, agent_version, source,
	os_name, os_version, manufacturer, model, payload`

func scanSnapshot(sc scanner) (store.Snapshot, error) {
	var s store.Snapshot
	var payload []byte
	if err := sc.Scan(&s.ID, &s.TenantID, &s.HostID, &s.CollectedAt, &s.ReceivedAt,
		&s.AgentVersion, &s.Source, &s.OSName, &s.OSVersion, &s.Manufacturer, &s.Model, &payload); err != nil {
		return store.Snapshot{}, err
	}
	if len(payload) > 0 {
		_ = json.Unmarshal(payload, &s.Payload)
	}
	return s, nil
}

// childTables are the per-snapshot normalized tables (plus changes) removed when
// a snapshot is deleted or purged.
var childTables = []string{
	"inventory_processors", "inventory_memory_modules", "inventory_disks",
	"inventory_network_interfaces", "inventory_software", "inventory_services",
	"inventory_monitors", "inventory_changes",
}

func (d *DB) InsertSnapshot(ctx context.Context, s store.Snapshot) error {
	if s.ID == "" {
		s.ID = store.NewID()
	}
	if s.Source == "" {
		s.Source = store.SourceAgent
	}
	now := time.Now().UTC()
	recv := s.ReceivedAt
	if recv.IsZero() {
		recv = now
	}
	return d.tenant(ctx, s.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_snapshots
			(id, tenant_id, host_id, collected_at, received_at, agent_version, source,
			 os_name, os_version, manufacturer, model, payload)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb)`,
			s.ID, s.TenantID, s.HostID, s.CollectedAt, recv, s.AgentVersion, s.Source,
			s.OSName, s.OSVersion, s.Manufacturer, s.Model, mustJSON(s.Payload)); e != nil {
			return mapErr(e)
		}
		if e := insertComponents(ctx, tx, s); e != nil {
			return mapErr(e)
		}
		// Advance host pointer/summary (best-effort within the same tx).
		if _, e := tx.Exec(ctx, `UPDATE inventory_hosts SET
			last_snapshot_id=$3,
			last_seen=GREATEST(last_seen, $4),
			os_name=CASE WHEN $5<>'' THEN $5 ELSE os_name END,
			os_version=CASE WHEN $6<>'' THEN $6 ELSE os_version END,
			manufacturer=CASE WHEN $7<>'' THEN $7 ELSE manufacturer END,
			model=CASE WHEN $8<>'' THEN $8 ELSE model END,
			updated_at=now()
			WHERE tenant_id=$1 AND id=$2`,
			s.TenantID, s.HostID, s.ID, s.CollectedAt, s.OSName, s.OSVersion, s.Manufacturer, s.Model); e != nil {
			return mapErr(e)
		}
		return nil
	})
}

func insertComponents(ctx context.Context, tx pgx.Tx, s store.Snapshot) error {
	p := s.Payload
	for _, c := range p.Processors {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_processors
			(id, tenant_id, host_id, snapshot_id, socket_designation, manufacturer, version,
			 max_speed_mhz, current_speed_mhz, core_count, core_enabled, thread_count,
			 part_number, serial_number, socket_populated)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			store.NewID(), s.TenantID, s.HostID, s.ID, c.SocketDesignation, c.Manufacturer, c.Version,
			int64(c.MaxSpeedMHz), int64(c.CurrentSpeedMHz), int64(c.CoreCount), int64(c.CoreEnabled),
			int64(c.ThreadCount), c.PartNumber, c.SerialNumber, c.SocketPopulated); e != nil {
			return e
		}
	}
	for _, mo := range p.Memory.Modules {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_memory_modules
			(id, tenant_id, host_id, snapshot_id, device_locator, bank_locator, capacity_bytes,
			 form_factor, memory_type, speed_mt_s, configured_speed_mt_s, manufacturer, serial_number, part_number)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14)`,
			store.NewID(), s.TenantID, s.HostID, s.ID, mo.DeviceLocator, mo.BankLocator, toi64(mo.CapacityBytes),
			mo.FormFactor, mo.MemoryType, int64(mo.SpeedMTs), int64(mo.ConfiguredSpeedMTs), mo.Manufacturer,
			mo.SerialNumber, mo.PartNumber); e != nil {
			return e
		}
	}
	for _, dk := range p.Disks {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_disks
			(id, tenant_id, host_id, snapshot_id, model, serial, size_bytes, media_type, interface)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			store.NewID(), s.TenantID, s.HostID, s.ID, dk.Model, dk.Serial, toi64(dk.SizeBytes),
			dk.MediaType, dk.Interface); e != nil {
			return e
		}
	}
	for _, ni := range p.Networks {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_network_interfaces
			(id, tenant_id, host_id, snapshot_id, name, mac, ip_addresses, subnet, gateway, dns, dhcp, speed_bps, type, up)
			VALUES ($1,$2,$3,$4,$5,$6,$7::jsonb,$8,$9,$10::jsonb,$11,$12,$13,$14)`,
			store.NewID(), s.TenantID, s.HostID, s.ID, ni.Name, ni.MAC, mustJSON(sliceOrEmpty(ni.IPAddresses)),
			ni.Subnet, ni.Gateway, mustJSON(sliceOrEmpty(ni.DNS)), ni.DHCP, toi64(ni.SpeedBps), ni.Type, ni.Up); e != nil {
			return e
		}
	}
	for _, pr := range p.Programs {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_software
			(id, tenant_id, host_id, snapshot_id, name, version, publisher, install_date, install_location, size_bytes)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			store.NewID(), s.TenantID, s.HostID, s.ID, pr.Name, pr.Version, pr.Publisher,
			pr.InstallDate, pr.InstallLocation, toi64(pr.SizeBytes)); e != nil {
			return e
		}
	}
	for _, sv := range p.Services {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_services
			(id, tenant_id, host_id, snapshot_id, name, display_name, state, start_mode, account)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			store.NewID(), s.TenantID, s.HostID, s.ID, sv.Name, sv.DisplayName, sv.State, sv.StartMode, sv.Account); e != nil {
			return e
		}
	}
	for _, mn := range p.Monitors {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_monitors
			(id, tenant_id, host_id, snapshot_id, manufacturer, model, serial_number)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`,
			store.NewID(), s.TenantID, s.HostID, s.ID, mn.Manufacturer, mn.Model, mn.SerialNumber); e != nil {
			return e
		}
	}
	return nil
}

func sliceOrEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func (d *DB) GetSnapshot(ctx context.Context, tenantID, id string) (out store.Snapshot, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanSnapshot(tx.QueryRow(ctx, "SELECT "+snapCols+" FROM inventory_snapshots WHERE tenant_id=$1 AND id=$2", tenantID, id))
		return mapErr(e)
	})
	return
}

func (d *DB) ListSnapshotsForHost(ctx context.Context, tenantID, hostID string, limit int, cursorID string) (out []store.Snapshot, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		q := "SELECT " + snapCols + " FROM inventory_snapshots WHERE tenant_id=$1 AND host_id=$2"
		args := []any{tenantID, hostID}
		if cursorID != "" {
			args = append(args, cursorID)
			q += fmt.Sprintf(" AND id < $%d", len(args))
		}
		q += " ORDER BY collected_at DESC, id DESC"
		if limit > 0 {
			args = append(args, limit)
			q += fmt.Sprintf(" LIMIT $%d", len(args))
		}
		rows, e := tx.Query(ctx, q, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			s, e := scanSnapshot(rows)
			if e != nil {
				return e
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return
}

func (d *DB) GetLatestForHost(ctx context.Context, tenantID, hostID string) (out store.Snapshot, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanSnapshot(tx.QueryRow(ctx,
			"SELECT "+snapCols+" FROM inventory_snapshots WHERE tenant_id=$1 AND host_id=$2 ORDER BY collected_at DESC LIMIT 1",
			tenantID, hostID))
		return mapErr(e)
	})
	return
}

func (d *DB) DeleteSnapshot(ctx context.Context, tenantID, id string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		for _, tbl := range childTables {
			if _, e := tx.Exec(ctx, "DELETE FROM "+tbl+" WHERE tenant_id=$1 AND snapshot_id=$2", tenantID, id); e != nil {
				return mapErr(e)
			}
		}
		ct, e := tx.Exec(ctx, "DELETE FROM inventory_snapshots WHERE tenant_id=$1 AND id=$2", tenantID, id)
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

func (d *DB) PurgeSnapshots(ctx context.Context, olderThan time.Time) (n int64, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `
			SELECT id::text FROM inventory_snapshots
			WHERE collected_at < $1
			AND id NOT IN (SELECT DISTINCT ON (host_id) id FROM inventory_snapshots ORDER BY host_id, collected_at DESC)`,
			olderThan)
		if e != nil {
			return e
		}
		var ids []string
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				rows.Close()
				return e
			}
			ids = append(ids, id)
		}
		rows.Close()
		if e := rows.Err(); e != nil {
			return e
		}
		if len(ids) == 0 {
			return nil
		}
		for _, tbl := range childTables {
			if _, e := tx.Exec(ctx, "DELETE FROM "+tbl+" WHERE snapshot_id::text = ANY($1)", ids); e != nil {
				return e
			}
		}
		ct, e := tx.Exec(ctx, "DELETE FROM inventory_snapshots WHERE id::text = ANY($1)", ids)
		if e != nil {
			return e
		}
		n = ct.RowsAffected()
		return nil
	})
	return
}

// ---- changes

func (d *DB) InsertChanges(ctx context.Context, changes []store.Change) error {
	if len(changes) == 0 {
		return nil
	}
	return d.tenant(ctx, changes[0].TenantID, func(tx pgx.Tx) error {
		for _, c := range changes {
			id := c.ID
			if id == "" {
				id = store.NewID()
			}
			var prev *string
			if c.PrevSnapshotID != "" {
				prev = &c.PrevSnapshotID
			}
			detected := c.DetectedAt
			if detected.IsZero() {
				detected = time.Now().UTC()
			}
			if _, e := tx.Exec(ctx, `INSERT INTO inventory_changes
				(id, tenant_id, host_id, snapshot_id, prev_snapshot_id, detected_at, category, change_type, component_key, before, after)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10::jsonb,$11::jsonb)`,
				id, c.TenantID, c.HostID, c.SnapshotID, prev, detected, c.Category, c.ChangeType, c.ComponentKey,
				jsonOrNil(c.Before), jsonOrNil(c.After)); e != nil {
				return mapErr(e)
			}
		}
		return nil
	})
}

func jsonOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scanChange(sc scanner) (store.Change, error) {
	var c store.Change
	var prev *string
	var before, after []byte
	if err := sc.Scan(&c.ID, &c.TenantID, &c.HostID, &c.SnapshotID, &prev, &c.DetectedAt,
		&c.Category, &c.ChangeType, &c.ComponentKey, &before, &after); err != nil {
		return store.Change{}, err
	}
	if prev != nil {
		c.PrevSnapshotID = *prev
	}
	c.Before = string(before)
	c.After = string(after)
	return c, nil
}

const changeCols = `id, tenant_id, host_id, snapshot_id, prev_snapshot_id, detected_at, category, change_type, component_key, before, after`

func (d *DB) ListChangesForHost(ctx context.Context, tenantID, hostID string, limit int) (out []store.Change, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		q := "SELECT " + changeCols + " FROM inventory_changes WHERE tenant_id=$1 AND host_id=$2 ORDER BY detected_at DESC"
		args := []any{tenantID, hostID}
		if limit > 0 {
			args = append(args, limit)
			q += fmt.Sprintf(" LIMIT $%d", len(args))
		}
		rows, e := tx.Query(ctx, q, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			c, e := scanChange(rows)
			if e != nil {
				return e
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return
}

func (d *DB) ListChangesForSnapshot(ctx context.Context, tenantID, snapshotID string) (out []store.Change, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+changeCols+" FROM inventory_changes WHERE tenant_id=$1 AND snapshot_id=$2 ORDER BY detected_at DESC",
			tenantID, snapshotID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			c, e := scanChange(rows)
			if e != nil {
				return e
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return
}

// ---- agents

const agentCols = `id, tenant_id, coalesce(host_id::text,''), credential_sealed, enrolled_at, last_seen, agent_version, revoked, identity_hint`

func scanAgent(sc scanner) (store.Agent, error) {
	var a store.Agent
	if err := sc.Scan(&a.ID, &a.TenantID, &a.HostID, &a.CredentialSealed, &a.EnrolledAt,
		&a.LastSeen, &a.AgentVersion, &a.Revoked, &a.IdentityHint); err != nil {
		return store.Agent{}, err
	}
	return a, nil
}

func (d *DB) CreateAgent(ctx context.Context, a store.Agent) error {
	return d.tenant(ctx, a.TenantID, func(tx pgx.Tx) error {
		id := a.ID
		if id == "" {
			id = store.NewID()
		}
		enrolled := a.EnrolledAt
		if enrolled.IsZero() {
			enrolled = time.Now().UTC()
		}
		last := a.LastSeen
		if last.IsZero() {
			last = enrolled
		}
		var hostID *string
		if a.HostID != "" {
			hostID = &a.HostID
		}
		_, e := tx.Exec(ctx, `INSERT INTO inventory_agents
			(id, tenant_id, host_id, credential_sealed, enrolled_at, last_seen, agent_version, revoked, identity_hint)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			id, a.TenantID, hostID, a.CredentialSealed, enrolled, last, a.AgentVersion, a.Revoked, a.IdentityHint)
		return mapErr(e)
	})
}

func (d *DB) GetAgent(ctx context.Context, tenantID, id string) (out store.Agent, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanAgent(tx.QueryRow(ctx, "SELECT "+agentCols+" FROM inventory_agents WHERE tenant_id=$1 AND id=$2", tenantID, id))
		return mapErr(e)
	})
	return
}

func (d *DB) GetAgentByID(ctx context.Context, id string) (out store.Agent, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		var e error
		out, e = scanAgent(tx.QueryRow(ctx, "SELECT "+agentCols+" FROM inventory_agents WHERE id=$1", id))
		return mapErr(e)
	})
	return
}

func (d *DB) TouchAgent(ctx context.Context, id, version, hostID string, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	return d.system(ctx, func(tx pgx.Tx) error {
		var hp *string
		if hostID != "" {
			hp = &hostID
		}
		ct, e := tx.Exec(ctx, `UPDATE inventory_agents SET
			last_seen=$2,
			agent_version=CASE WHEN $3<>'' THEN $3 ELSE agent_version END,
			host_id=COALESCE($4, host_id)
			WHERE id=$1`, id, at, version, hp)
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

func (d *DB) RevokeAgent(ctx context.Context, tenantID, id string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, "UPDATE inventory_agents SET revoked=true WHERE tenant_id=$1 AND id=$2", tenantID, id)
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

// ---- enrollment tokens

const tokenCols = `id, tenant_id, token_hash, expires_at, used_at, revoked, created_by, created_at, label`

func scanToken(sc scanner) (store.EnrollmentToken, error) {
	var t store.EnrollmentToken
	if err := sc.Scan(&t.ID, &t.TenantID, &t.TokenHash, &t.ExpiresAt, &t.UsedAt,
		&t.Revoked, &t.CreatedBy, &t.CreatedAt, &t.Label); err != nil {
		return store.EnrollmentToken{}, err
	}
	return t, nil
}

func (d *DB) CreateEnrollmentToken(ctx context.Context, t store.EnrollmentToken) error {
	return d.tenant(ctx, t.TenantID, func(tx pgx.Tx) error {
		id := t.ID
		if id == "" {
			id = store.NewID()
		}
		created := t.CreatedAt
		if created.IsZero() {
			created = time.Now().UTC()
		}
		_, e := tx.Exec(ctx, `INSERT INTO inventory_enrollment_tokens
			(id, tenant_id, token_hash, expires_at, used_at, revoked, created_by, created_at, label)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
			id, t.TenantID, t.TokenHash, t.ExpiresAt, t.UsedAt, t.Revoked, t.CreatedBy, created, t.Label)
		return mapErr(e)
	})
}

func (d *DB) ConsumeEnrollmentToken(ctx context.Context, tokenHash string, now time.Time) (out store.EnrollmentToken, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		var e error
		out, e = scanToken(tx.QueryRow(ctx, `UPDATE inventory_enrollment_tokens SET used_at=$2
			WHERE token_hash=$1 AND used_at IS NULL AND NOT revoked AND expires_at > $2
			RETURNING `+tokenCols, tokenHash, now))
		if e == nil {
			return nil
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return e
		}
		// Distinguish "does not exist" from "exists but invalid".
		var exists bool
		if e2 := tx.QueryRow(ctx, "SELECT true FROM inventory_enrollment_tokens WHERE token_hash=$1", tokenHash).Scan(&exists); e2 != nil {
			if errors.Is(e2, pgx.ErrNoRows) {
				return repo.ErrNotFound
			}
			return e2
		}
		return repo.ErrConflict
	})
	return
}

func (d *DB) RevokeEnrollmentToken(ctx context.Context, tenantID, id string) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, "UPDATE inventory_enrollment_tokens SET revoked=true WHERE tenant_id=$1 AND id=$2", tenantID, id)
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

// ---- statistics

func (d *DB) TenantStats(ctx context.Context, tenantID string, staleBefore time.Time) (out repo.Stats, err error) {
	out = repo.Stats{
		HostsByStatus:       map[string]int64{},
		HostsByOS:           map[string]int64{},
		HostsByManufacturer: map[string]int64{},
		TopPrograms:         map[string]int64{},
		OSVersions:          map[string]int64{},
	}
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		// Host rollups.
		rows, e := tx.Query(ctx, "SELECT status, os_name, manufacturer, os_version, last_seen FROM inventory_hosts WHERE tenant_id=$1", tenantID)
		if e != nil {
			return e
		}
		for rows.Next() {
			var status, osName, manu, osVer string
			var lastSeen time.Time
			if e := rows.Scan(&status, &osName, &manu, &osVer, &lastSeen); e != nil {
				rows.Close()
				return e
			}
			out.HostsTotal++
			out.HostsByStatus[status]++
			if osName != "" {
				out.HostsByOS[osName]++
			}
			if manu != "" {
				out.HostsByManufacturer[manu]++
			}
			if osVer != "" {
				out.OSVersions[osVer]++
			}
			if lastSeen.Before(staleBefore) {
				out.StaleHosts++
			}
		}
		rows.Close()
		if e := rows.Err(); e != nil {
			return e
		}
		// Snapshot count.
		if e := tx.QueryRow(ctx, "SELECT count(*) FROM inventory_snapshots WHERE tenant_id=$1", tenantID).Scan(&out.SnapshotsTotal); e != nil {
			return e
		}
		// Latest snapshot payload per host for hardware/software rollups.
		pr, e := tx.Query(ctx, `SELECT DISTINCT ON (host_id) payload FROM inventory_snapshots
			WHERE tenant_id=$1 ORDER BY host_id, collected_at DESC`, tenantID)
		if e != nil {
			return e
		}
		programs := map[string]int64{}
		for pr.Next() {
			var payload []byte
			if e := pr.Scan(&payload); e != nil {
				pr.Close()
				return e
			}
			var inv store.Inventory
			if len(payload) > 0 {
				_ = json.Unmarshal(payload, &inv)
			}
			mem := inv.Memory.TotalPhysicalBytes
			if mem == 0 {
				for _, mod := range inv.Memory.Modules {
					mem += mod.CapacityBytes
				}
			}
			out.TotalMemoryBytes += mem
			for _, proc := range inv.Processors {
				out.TotalCPUCores += int64(proc.CoreCount)
			}
			for _, dk := range inv.Disks {
				out.TotalDiskBytes += dk.SizeBytes
			}
			for _, prog := range inv.Programs {
				if prog.Name != "" {
					programs[prog.Name]++
				}
			}
		}
		pr.Close()
		if e := pr.Err(); e != nil {
			return e
		}
		out.TopPrograms = topN(programs, 20)
		return nil
	})
	return
}

func topN(counts map[string]int64, n int) map[string]int64 {
	type kv struct {
		k string
		v int64
	}
	items := make([]kv, 0, len(counts))
	for k, v := range counts {
		items = append(items, kv{k, v})
	}
	// simple selection: sort by count desc, key asc
	for i := 0; i < len(items); i++ {
		for j := i + 1; j < len(items); j++ {
			if items[j].v > items[i].v || (items[j].v == items[i].v && items[j].k < items[i].k) {
				items[i], items[j] = items[j], items[i]
			}
		}
	}
	out := map[string]int64{}
	for i, it := range items {
		if n > 0 && i >= n {
			break
		}
		out[it.k] = it.v
	}
	return out
}

func (d *DB) TenantIDs(ctx context.Context) (out []string, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT DISTINCT tenant_id::text FROM (
			SELECT tenant_id FROM inventory_hosts
			UNION SELECT tenant_id FROM inventory_snapshots
			UNION SELECT tenant_id FROM inventory_agents) u ORDER BY 1`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if e := rows.Scan(&id); e != nil {
				return e
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return
}

// ---- audit

func (d *DB) AppendAudit(ctx context.Context, row store.AuditRow) error {
	return d.system(ctx, func(tx pgx.Tx) error {
		id := row.ID
		if id == "" {
			id = store.NewID()
		}
		at := row.At
		if at.IsZero() {
			at = time.Now().UTC()
		}
		var detail any
		if row.Detail != nil {
			detail = mustJSON(row.Detail)
		}
		_, e := tx.Exec(ctx, `INSERT INTO inventory_audit_events
			(id, tenant_id, at, actor_kind, actor_id, action, subject_kind, subject_id, outcome, reason, detail)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)`,
			id, row.TenantID, at, row.ActorKind, row.ActorID, row.Action, row.SubjectKind, row.SubjectID,
			row.Outcome, row.Reason, detail)
		return e
	})
}

var _ repo.Store = (*DB)(nil)
