package repodb

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Feature 037: module delivery items (tenant table under RLS). No column
// holds bundle bytes, a token or a password; every state change appends its
// audit rows in the same transaction.

const moduleCols = `id, tenant_id, source, delivery_id, host_id, coalesce(agent_id::text,''), module, version, state, reason,
	hook_exit_code, attempt, fetches, created_at, updated_at, expires_at, delivered_at, fetched_at, reported_at`

func scanModuleItem(sc scanner) (store.ModuleDeliveryItem, error) {
	var i store.ModuleDeliveryItem
	err := sc.Scan(&i.ID, &i.TenantID, &i.Source, &i.DeliveryID, &i.HostID, &i.AgentID, &i.Module, &i.Version, &i.State, &i.Reason,
		&i.HookExitCode, &i.Attempt, &i.Fetches, &i.CreatedAt, &i.UpdatedAt, &i.ExpiresAt, &i.DeliveredAt, &i.FetchedAt, &i.ReportedAt)
	return i, err
}

func queryModuleItems(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]store.ModuleDeliveryItem, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.ModuleDeliveryItem
	for rows.Next() {
		i, err := scanModuleItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// withLimit appends a LIMIT placeholder when limit > 0.
func withLimit(q string, args []any, limit int) (string, []any) {
	if limit > 0 {
		args = append(args, limit)
		q += " LIMIT $" + strconv.Itoa(len(args))
	}
	return q, args
}

// CreateModuleItem implements repo.ModuleDeliveryStore.
func (d *DB) CreateModuleItem(ctx context.Context, n repo.NewModuleItem) (superseded []store.ModuleDeliveryItem, err error) {
	it := n.Item
	err = d.tenant(ctx, it.TenantID, func(tx pgx.Tx) error {
		superseded = nil
		rows := append([]store.AuditRow{}, n.Audit...)
		if it.Active() {
			old, e := queryModuleItems(ctx, tx, "SELECT "+moduleCols+` FROM inventory_module_delivery_items
				WHERE tenant_id=$1 AND host_id=$2 AND module=$3 AND state IN ('pending','delivered','fetched') FOR UPDATE`,
				it.TenantID, it.HostID, it.Module)
			if e != nil {
				return e
			}
			for _, o := range old {
				o.State, o.Reason, o.UpdatedAt = store.DeliverySuperseded, "", it.CreatedAt
				if _, e := tx.Exec(ctx, `UPDATE inventory_module_delivery_items SET state=$3, reason='', updated_at=$4
					WHERE tenant_id=$1 AND id=$2`, o.TenantID, o.ID, o.State, o.UpdatedAt); e != nil {
					return e
				}
				superseded = append(superseded, o)
				if n.SupersedeAudit != nil {
					rows = append(rows, n.SupersedeAudit(o, it.ID))
				}
			}
		}
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_module_delivery_items
			(id, tenant_id, source, delivery_id, host_id, agent_id, module, version, state, reason, hook_exit_code, attempt, fetches,
			 created_at, updated_at, expires_at, delivered_at, fetched_at, reported_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)`,
			it.ID, it.TenantID, it.Source, it.DeliveryID, it.HostID, nullUUID(it.AgentID), it.Module, it.Version, it.State, it.Reason,
			it.HookExitCode, it.Attempt, it.Fetches, it.CreatedAt, it.UpdatedAt, it.ExpiresAt, it.DeliveredAt, it.FetchedAt,
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

// GetModuleItem implements repo.ModuleDeliveryStore.
func (d *DB) GetModuleItem(ctx context.Context, tenantID, id string) (out store.ModuleDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanModuleItem(tx.QueryRow(ctx, "SELECT "+moduleCols+" FROM inventory_module_delivery_items WHERE tenant_id=$1 AND id=$2",
			tenantID, id))
		return mapErr(e)
	})
	return out, err
}

// GetModuleItemByDelivery implements repo.ModuleDeliveryStore.
func (d *DB) GetModuleItemByDelivery(ctx context.Context, tenantID, source, deliveryID string) (out store.ModuleDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanModuleItem(tx.QueryRow(ctx, "SELECT "+moduleCols+` FROM inventory_module_delivery_items
			WHERE tenant_id=$1 AND source=$2 AND delivery_id=$3`, tenantID, source, deliveryID))
		return mapErr(e)
	})
	return out, err
}

// UpdateModuleItem implements repo.ModuleDeliveryStore.
func (d *DB) UpdateModuleItem(ctx context.Context, tenantID, id string, fn func(*store.ModuleDeliveryItem) ([]store.AuditRow, error)) (out store.ModuleDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		i, e := scanModuleItem(tx.QueryRow(ctx, "SELECT "+moduleCols+" FROM inventory_module_delivery_items WHERE tenant_id=$1 AND id=$2 FOR UPDATE",
			tenantID, id))
		if e != nil {
			return mapErr(e)
		}
		out = i
		rows, e := fn(&i)
		if e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE inventory_module_delivery_items SET agent_id=$3, state=$4, reason=$5, hook_exit_code=$6,
			attempt=$7, fetches=$8, updated_at=$9, expires_at=$10, delivered_at=$11, fetched_at=$12, reported_at=$13
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, nullUUID(i.AgentID), i.State, i.Reason, i.HookExitCode, i.Attempt, i.Fetches,
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

// ListActiveModuleItemsForAgent implements repo.ModuleDeliveryStore.
func (d *DB) ListActiveModuleItemsForAgent(ctx context.Context, tenantID, agentID string, limit int) (out []store.ModuleDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		q, args := withLimit("SELECT "+moduleCols+` FROM inventory_module_delivery_items
			WHERE tenant_id=$1 AND agent_id=$2 AND state IN ('pending','delivered','fetched') ORDER BY created_at, id`,
			[]any{tenantID, agentID}, limit)
		var e error
		out, e = queryModuleItems(ctx, tx, q, args...)
		return e
	})
	return out, err
}

// ListStaleModuleItems implements repo.ModuleDeliveryStore (system scope).
func (d *DB) ListStaleModuleItems(ctx context.Context, now, reportBefore time.Time, limit int) (out []store.ModuleDeliveryItem, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		q, args := withLimit("SELECT "+moduleCols+` FROM inventory_module_delivery_items
			WHERE state IN ('pending','delivered','fetched') AND (expires_at < $1 OR (state = 'fetched' AND updated_at < $2))
			ORDER BY created_at, id`, []any{now, reportBefore}, limit)
		var e error
		out, e = queryModuleItems(ctx, tx, q, args...)
		return e
	})
	return out, err
}

// PurgeModuleItems implements repo.ModuleDeliveryStore (system scope).
func (d *DB) PurgeModuleItems(ctx context.Context, olderThan time.Time) (n int64, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `DELETE FROM inventory_module_delivery_items
			WHERE state NOT IN ('pending','delivered','fetched') AND updated_at < $1`, olderThan)
		if e != nil {
			return e
		}
		n = ct.RowsAffected()
		return nil
	})
	return n, err
}
