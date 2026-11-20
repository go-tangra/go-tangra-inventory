package repodb

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Feature 038: Teleport join items (tenant table under RLS). No column holds
// a join token, CA pins or labels; every state change appends its audit rows
// in the same transaction.

const teleportCols = `id, tenant_id, source, enrollment_id, host_id, coalesce(agent_id::text,''), cluster_id, state, reason,
	node_host_uuid, attempt, fetches, created_at, updated_at, expires_at, delivered_at, fetched_at, reported_at`

// teleportActive is the SQL list of the active states (store.TeleportJoinActive).
const teleportActive = `('pending','delivered','fetched','installing')`

func scanTeleportItem(sc scanner) (store.TeleportJoinItem, error) {
	var i store.TeleportJoinItem
	err := sc.Scan(&i.ID, &i.TenantID, &i.Source, &i.EnrollmentID, &i.HostID, &i.AgentID, &i.ClusterID, &i.State, &i.Reason,
		&i.NodeHostUUID, &i.Attempt, &i.Fetches, &i.CreatedAt, &i.UpdatedAt, &i.ExpiresAt, &i.DeliveredAt, &i.FetchedAt, &i.ReportedAt)
	return i, err
}

func queryTeleportItems(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]store.TeleportJoinItem, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.TeleportJoinItem
	for rows.Next() {
		i, err := scanTeleportItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CreateTeleportItem implements repo.TeleportJoinStore.
func (d *DB) CreateTeleportItem(ctx context.Context, n repo.NewTeleportItem) (superseded []store.TeleportJoinItem, err error) {
	it := n.Item
	err = d.tenant(ctx, it.TenantID, func(tx pgx.Tx) error {
		superseded = nil
		rows := append([]store.AuditRow{}, n.Audit...)
		if it.Active() {
			old, e := queryTeleportItems(ctx, tx, "SELECT "+teleportCols+` FROM inventory_teleport_join_items
				WHERE tenant_id=$1 AND host_id=$2 AND cluster_id=$3 AND state IN `+teleportActive+` FOR UPDATE`,
				it.TenantID, it.HostID, it.ClusterID)
			if e != nil {
				return e
			}
			for _, o := range old {
				o.State, o.Reason, o.UpdatedAt = store.DeliverySuperseded, "", it.CreatedAt
				if _, e := tx.Exec(ctx, `UPDATE inventory_teleport_join_items SET state=$3, reason='', updated_at=$4
					WHERE tenant_id=$1 AND id=$2`, o.TenantID, o.ID, o.State, o.UpdatedAt); e != nil {
					return e
				}
				superseded = append(superseded, o)
				if n.SupersedeAudit != nil {
					rows = append(rows, n.SupersedeAudit(o, it.ID))
				}
			}
		}
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_teleport_join_items
			(id, tenant_id, source, enrollment_id, host_id, agent_id, cluster_id, state, reason, node_host_uuid, attempt, fetches,
			 created_at, updated_at, expires_at, delivered_at, fetched_at, reported_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			it.ID, it.TenantID, it.Source, it.EnrollmentID, it.HostID, nullUUID(it.AgentID), it.ClusterID, it.State, it.Reason,
			it.NodeHostUUID, it.Attempt, it.Fetches, it.CreatedAt, it.UpdatedAt, it.ExpiresAt, it.DeliveredAt, it.FetchedAt,
			it.ReportedAt); e != nil {
			return mapErr(e)
		}
		for _, r := range rows {
			if e := insertAuditTx(ctx, tx, r); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		superseded = nil
	}
	return superseded, err
}

// GetTeleportItem implements repo.TeleportJoinStore.
func (d *DB) GetTeleportItem(ctx context.Context, tenantID, id string) (out store.TeleportJoinItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanTeleportItem(tx.QueryRow(ctx, "SELECT "+teleportCols+" FROM inventory_teleport_join_items WHERE tenant_id=$1 AND id=$2",
			tenantID, id))
		return mapErr(e)
	})
	return out, err
}

// GetTeleportItemByEnrollment implements repo.TeleportJoinStore.
func (d *DB) GetTeleportItemByEnrollment(ctx context.Context, tenantID, source, enrollmentID string) (out store.TeleportJoinItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanTeleportItem(tx.QueryRow(ctx, "SELECT "+teleportCols+` FROM inventory_teleport_join_items
			WHERE tenant_id=$1 AND source=$2 AND enrollment_id=$3`, tenantID, source, enrollmentID))
		return mapErr(e)
	})
	return out, err
}

// UpdateTeleportItem implements repo.TeleportJoinStore.
func (d *DB) UpdateTeleportItem(ctx context.Context, tenantID, id string, fn func(*store.TeleportJoinItem) ([]store.AuditRow, error)) (out store.TeleportJoinItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		i, e := scanTeleportItem(tx.QueryRow(ctx, "SELECT "+teleportCols+" FROM inventory_teleport_join_items WHERE tenant_id=$1 AND id=$2 FOR UPDATE",
			tenantID, id))
		if e != nil {
			return mapErr(e)
		}
		out = i
		rows, e := fn(&i)
		if e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE inventory_teleport_join_items SET agent_id=$3, state=$4, reason=$5, node_host_uuid=$6,
			attempt=$7, fetches=$8, updated_at=$9, expires_at=$10, delivered_at=$11, fetched_at=$12, reported_at=$13
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, nullUUID(i.AgentID), i.State, i.Reason, i.NodeHostUUID, i.Attempt, i.Fetches,
			i.UpdatedAt, i.ExpiresAt, i.DeliveredAt, i.FetchedAt, i.ReportedAt); e != nil {
			return mapErr(e)
		}
		for _, r := range rows {
			if e := insertAuditTx(ctx, tx, r); e != nil {
				return e
			}
		}
		i.ID, i.TenantID = out.ID, out.TenantID
		out = i
		return nil
	})
	return out, err
}

// ListActiveTeleportItemsForAgent implements repo.TeleportJoinStore.
func (d *DB) ListActiveTeleportItemsForAgent(ctx context.Context, tenantID, agentID string, limit int) (out []store.TeleportJoinItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		q, args := withLimit("SELECT "+teleportCols+` FROM inventory_teleport_join_items
			WHERE tenant_id=$1 AND agent_id=$2 AND state IN `+teleportActive+` ORDER BY created_at, id`,
			[]any{tenantID, agentID}, limit)
		var e error
		out, e = queryTeleportItems(ctx, tx, q, args...)
		return e
	})
	return out, err
}

// ListStaleTeleportItems implements repo.TeleportJoinStore (system scope).
func (d *DB) ListStaleTeleportItems(ctx context.Context, now, reportBefore time.Time, limit int) (out []store.TeleportJoinItem, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		q, args := withLimit("SELECT "+teleportCols+` FROM inventory_teleport_join_items
			WHERE (state IN ('pending','delivered','fetched') AND expires_at < $1)
			   OR (state IN ('fetched','installing') AND updated_at < $2)
			ORDER BY created_at, id`, []any{now, reportBefore}, limit)
		var e error
		out, e = queryTeleportItems(ctx, tx, q, args...)
		return e
	})
	return out, err
}

// PurgeTeleportItems implements repo.TeleportJoinStore (system scope):
// removes terminal rows only.
func (d *DB) PurgeTeleportItems(ctx context.Context, olderThan time.Time) (n int64, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `DELETE FROM inventory_teleport_join_items
			WHERE state NOT IN `+teleportActive+` AND updated_at < $1`, olderThan)
		if e != nil {
			return e
		}
		n = ct.RowsAffected()
		return nil
	})
	return n, err
}
