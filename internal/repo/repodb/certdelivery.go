package repodb

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Feature 033: certificate deliveries (tenant tables under RLS). No column
// holds certificate or key material; every state change appends its audit
// rows in the same transaction.

const deliveryCols = `id, tenant_id, source, idempotency_key, configuration_id, target_id, trigger, certificate_id, name,
	key_policy, host_ids::text[], host_tags, requested_by, created_at, expires_at`

func scanDelivery(sc scanner) (store.CertDelivery, error) {
	var d store.CertDelivery
	err := sc.Scan(&d.ID, &d.TenantID, &d.Source, &d.IdempotencyKey, &d.ConfigurationID, &d.TargetID, &d.Trigger,
		&d.CertificateID, &d.Name, &d.KeyPolicy, &d.HostIDs, &d.HostTags, &d.RequestedBy, &d.CreatedAt, &d.ExpiresAt)
	return d, err
}

const itemCols = `id, tenant_id, delivery_id, host_id, coalesce(agent_id::text,''), name, certificate_id, state, reason,
	attempts, fetches, rerun_hook, serial, fingerprint_sha256, common_name, not_after, hook_exit_code, detail,
	created_at, updated_at, delivered_at, fetched_at, finished_at`

func scanItem(sc scanner) (store.CertDeliveryItem, error) {
	var i store.CertDeliveryItem
	err := sc.Scan(itemDest(&i)...)
	return i, err
}

// itemDest lists the scan destinations of itemCols.
func itemDest(i *store.CertDeliveryItem) []any {
	return []any{&i.ID, &i.TenantID, &i.DeliveryID, &i.HostID, &i.AgentID, &i.Name, &i.CertificateID, &i.State, &i.Reason,
		&i.Attempts, &i.Fetches, &i.RerunHook, &i.Serial, &i.FingerprintSHA256, &i.CommonName, &i.NotAfter, &i.HookExitCode, &i.Detail,
		&i.CreatedAt, &i.UpdatedAt, &i.DeliveredAt, &i.FetchedAt, &i.FinishedAt}
}

func queryItems(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]store.CertDeliveryItem, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.CertDeliveryItem
	for rows.Next() {
		i, err := scanItem(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func insertItemTx(ctx context.Context, tx pgx.Tx, i store.CertDeliveryItem) error {
	_, err := tx.Exec(ctx, `INSERT INTO inventory_cert_delivery_items
		(id, tenant_id, delivery_id, host_id, agent_id, name, certificate_id, state, reason, attempts, fetches, rerun_hook,
		 serial, fingerprint_sha256, not_after, hook_exit_code, detail, created_at, updated_at, delivered_at, fetched_at, finished_at,
		 common_name)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		i.ID, i.TenantID, i.DeliveryID, i.HostID, nullUUID(i.AgentID), i.Name, i.CertificateID, i.State, i.Reason, i.Attempts,
		i.Fetches, i.RerunHook, i.Serial, i.FingerprintSHA256, i.NotAfter, i.HookExitCode, i.Detail, i.CreatedAt, i.UpdatedAt,
		i.DeliveredAt, i.FetchedAt, i.FinishedAt, i.CommonName)
	return mapErr(err)
}

// CreateCertDelivery implements repo.CertDeliveryStore.
func (d *DB) CreateCertDelivery(ctx context.Context, n repo.NewCertDelivery) (superseded []store.CertDeliveryItem, err error) {
	dl := n.Delivery
	err = d.tenant(ctx, dl.TenantID, func(tx pgx.Tx) error {
		superseded = nil
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_cert_deliveries
			(id, tenant_id, source, idempotency_key, configuration_id, target_id, trigger, certificate_id, name, key_policy,
			 host_ids, host_tags, requested_by, created_at, expires_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::uuid[],$12,$13,$14,$15)`,
			dl.ID, dl.TenantID, dl.Source, dl.IdempotencyKey, dl.ConfigurationID, dl.TargetID, dl.Trigger, dl.CertificateID,
			dl.Name, dl.KeyPolicy, nonNil(dl.HostIDs), nonNil(dl.HostTags), dl.RequestedBy, dl.CreatedAt, dl.ExpiresAt); e != nil {
			return mapErr(e)
		}
		rows := append([]store.AuditRow{}, n.Audit...)
		for _, it := range n.Items {
			if it.Active() {
				old, e := queryItems(ctx, tx, "SELECT "+itemCols+` FROM inventory_cert_delivery_items
					WHERE tenant_id=$1 AND host_id=$2 AND name=$3 AND state IN ('pending','delivered','fetched') FOR UPDATE`,
					it.TenantID, it.HostID, it.Name)
				if e != nil {
					return e
				}
				for _, o := range old {
					at := dl.CreatedAt
					o.State, o.Reason, o.UpdatedAt, o.FinishedAt = store.DeliverySuperseded, "", at, &at
					if _, e := tx.Exec(ctx, `UPDATE inventory_cert_delivery_items SET state=$3, reason='', updated_at=$4, finished_at=$4
						WHERE tenant_id=$1 AND id=$2`, o.TenantID, o.ID, o.State, at); e != nil {
						return e
					}
					superseded = append(superseded, o)
					if n.SupersedeAudit != nil {
						rows = append(rows, n.SupersedeAudit(o, it.ID))
					}
				}
			}
			if e := insertItemTx(ctx, tx, it); e != nil {
				return e
			}
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

// GetCertDelivery implements repo.CertDeliveryStore.
func (d *DB) GetCertDelivery(ctx context.Context, tenantID, id string) (out store.CertDelivery, items []store.CertDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		if out, e = scanDelivery(tx.QueryRow(ctx, "SELECT "+deliveryCols+" FROM inventory_cert_deliveries WHERE tenant_id=$1 AND id=$2", tenantID, id)); e != nil {
			return mapErr(e)
		}
		items, e = queryItems(ctx, tx, "SELECT "+itemCols+" FROM inventory_cert_delivery_items WHERE tenant_id=$1 AND delivery_id=$2 ORDER BY created_at, id", tenantID, id)
		return e
	})
	return out, items, err
}

// ListCertDeliveriesByID implements repo.CertDeliveryStore.
func (d *DB) ListCertDeliveriesByID(ctx context.Context, tenantID string, ids []string) (out []store.CertDelivery, err error) {
	out = []store.CertDelivery{}
	if len(ids) == 0 {
		return out, nil
	}
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+deliveryCols+" FROM inventory_cert_deliveries WHERE tenant_id=$1 AND id::text = ANY($2)", tenantID, ids)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			dl, e := scanDelivery(rows)
			if e != nil {
				return e
			}
			out = append(out, dl)
		}
		return rows.Err()
	})
	return out, err
}

// HostnamesByID implements repo.CertDeliveryStore.
func (d *DB) HostnamesByID(ctx context.Context, tenantID string, ids []string) (out map[string]string, err error) {
	out = map[string]string{}
	if len(ids) == 0 {
		return out, nil
	}
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT id::text, hostname FROM inventory_hosts WHERE tenant_id=$1 AND id::text = ANY($2)", tenantID, ids)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var id, name string
			if e := rows.Scan(&id, &name); e != nil {
				return e
			}
			out[id] = name
		}
		return rows.Err()
	})
	return out, err
}

// GetCertDeliveryByKey implements repo.CertDeliveryStore.
func (d *DB) GetCertDeliveryByKey(ctx context.Context, tenantID, source, key string) (out store.CertDelivery, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanDelivery(tx.QueryRow(ctx, "SELECT "+deliveryCols+` FROM inventory_cert_deliveries
			WHERE tenant_id=$1 AND source=$2 AND idempotency_key=$3`, tenantID, source, key))
		return mapErr(e)
	})
	return out, err
}

// GetCertItem implements repo.CertDeliveryStore.
func (d *DB) GetCertItem(ctx context.Context, tenantID, id string) (out store.CertDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanItem(tx.QueryRow(ctx, "SELECT "+itemCols+" FROM inventory_cert_delivery_items WHERE tenant_id=$1 AND id=$2", tenantID, id))
		return mapErr(e)
	})
	return out, err
}

// UpdateCertItem implements repo.CertDeliveryStore.
func (d *DB) UpdateCertItem(ctx context.Context, tenantID, id string, fn func(*store.CertDeliveryItem) (repo.CertItemChange, error)) (out store.CertDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		i, e := scanItem(tx.QueryRow(ctx, "SELECT "+itemCols+" FROM inventory_cert_delivery_items WHERE tenant_id=$1 AND id=$2 FOR UPDATE", tenantID, id))
		if e != nil {
			return mapErr(e)
		}
		out = i
		ch, e := fn(&i)
		if e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE inventory_cert_delivery_items SET agent_id=$3, state=$4, reason=$5, attempts=$6, fetches=$7,
			rerun_hook=$8, serial=$9, fingerprint_sha256=$10, not_after=$11, hook_exit_code=$12, detail=$13, updated_at=$14,
			delivered_at=$15, fetched_at=$16, finished_at=$17, common_name=$18
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, nullUUID(i.AgentID), i.State, i.Reason, i.Attempts, i.Fetches,
			i.RerunHook, i.Serial, i.FingerprintSHA256, i.NotAfter, i.HookExitCode, i.Detail, i.UpdatedAt,
			i.DeliveredAt, i.FetchedAt, i.FinishedAt, i.CommonName); e != nil {
			return mapErr(e)
		}
		if h := ch.HostCert; h != nil {
			if e := upsertHostCertTx(ctx, tx, tenantID, *h); e != nil {
				return e
			}
		}
		for _, r := range ch.Audit {
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

const hostCertCols = `tenant_id, host_id, name, certificate_id, configuration_id, common_name, serial, fingerprint_sha256,
	not_after, state, reason, hook_exit_code, last_item_id, last_delivered_at, revoked_at, updated_at`

func scanHostCert(sc scanner) (store.HostCertificate, error) {
	var h store.HostCertificate
	err := sc.Scan(&h.TenantID, &h.HostID, &h.Name, &h.CertificateID, &h.ConfigurationID, &h.CommonName, &h.Serial,
		&h.FingerprintSHA256, &h.NotAfter, &h.State, &h.Reason, &h.HookExitCode, &h.LastItemID, &h.LastDeliveredAt,
		&h.RevokedAt, &h.UpdatedAt)
	return h, err
}

func upsertHostCertTx(ctx context.Context, tx pgx.Tx, tenantID string, h store.HostCertificate) error {
	if h.UpdatedAt.IsZero() {
		h.UpdatedAt = time.Now().UTC()
	}
	_, err := tx.Exec(ctx, `INSERT INTO inventory_host_certificates (`+hostCertCols+`)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (tenant_id, host_id, name) DO UPDATE SET certificate_id=EXCLUDED.certificate_id,
		  configuration_id=EXCLUDED.configuration_id, common_name=EXCLUDED.common_name, serial=EXCLUDED.serial,
		  fingerprint_sha256=EXCLUDED.fingerprint_sha256, not_after=EXCLUDED.not_after, state=EXCLUDED.state,
		  reason=EXCLUDED.reason, hook_exit_code=EXCLUDED.hook_exit_code, last_item_id=EXCLUDED.last_item_id,
		  last_delivered_at=EXCLUDED.last_delivered_at,
		  -- a revocation of the same certificate is never cleared (a report
		  -- built from a read taken before MarkRevoked must not undo it)
		  revoked_at=CASE WHEN EXCLUDED.certificate_id=inventory_host_certificates.certificate_id
		    THEN COALESCE(EXCLUDED.revoked_at, inventory_host_certificates.revoked_at) ELSE EXCLUDED.revoked_at END,
		  updated_at=EXCLUDED.updated_at`,
		tenantID, h.HostID, h.Name, h.CertificateID, h.ConfigurationID, h.CommonName, h.Serial, h.FingerprintSHA256,
		h.NotAfter, h.State, h.Reason, h.HookExitCode, h.LastItemID, h.LastDeliveredAt, h.RevokedAt, h.UpdatedAt)
	return mapErr(err)
}

// ListActiveCertItemsForAgent implements repo.CertDeliveryStore.
func (d *DB) ListActiveCertItemsForAgent(ctx context.Context, tenantID, agentID string, limit int) (out []store.CertDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		q := "SELECT " + itemCols + ` FROM inventory_cert_delivery_items
			WHERE tenant_id=$1 AND agent_id=$2 AND state IN ('pending','delivered','fetched') ORDER BY created_at, id`
		args := []any{tenantID, agentID}
		if limit > 0 {
			q += " LIMIT $3"
			args = append(args, limit)
		}
		var e error
		out, e = queryItems(ctx, tx, q, args...)
		return e
	})
	return out, err
}

// ListCertItemsPage implements repo.CertDeliveryStore.
func (d *DB) ListCertItemsPage(ctx context.Context, tenantID string, f repo.CertItemFilter, req listquery.Request) (out []store.CertDeliveryItem, total int, applied listquery.Request, err error) {
	applied = store.ListRequest(req, store.CertItemList)
	where, args := "tenant_id=$1", []any{tenantID}
	for _, c := range []struct{ col, val string }{
		{"host_id::text", f.HostID}, {"state", f.State}, {"name", f.Name},
		{"certificate_id", f.CertificateID}, {"delivery_id::text", f.DeliveryID},
	} {
		if c.val != "" {
			args = append(args, c.val)
			where += fmt.Sprintf(" AND %s=$%d", c.col, len(args))
		}
	}
	if f.Active {
		where += " AND state IN ('pending','delivered','fetched')"
	}
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, "SELECT count(*) FROM inventory_cert_delivery_items WHERE "+where, args...).Scan(&total); e != nil {
			return e
		}
		applied = applied.Clamp(total)
		var e error
		out, e = queryItems(ctx, tx, fmt.Sprintf("SELECT %s FROM inventory_cert_delivery_items WHERE %s ORDER BY %s LIMIT %d OFFSET %d",
			itemCols, where, applied.OrderBy(store.CertItemList), applied.Limit(), applied.Offset()), args...)
		return e
	})
	return out, total, applied, err
}

// errEmptyCancelScope refuses a CancelCertItems call without a scope.
var errEmptyCancelScope = errors.New("repodb: cancel scope is empty")

// CancelCertItems implements repo.CertDeliveryStore.
func (d *DB) CancelCertItems(ctx context.Context, tenantID string, scope repo.CertCancelScope, reason string, row func(store.CertDeliveryItem) store.AuditRow) (out []store.CertDeliveryItem, err error) {
	var col, val string
	switch {
	case scope.HostID != "":
		col, val = "host_id::text", scope.HostID
	case scope.AgentID != "":
		col, val = "agent_id::text", scope.AgentID
	case scope.CertificateID != "":
		col, val = "certificate_id", scope.CertificateID
	default:
		return nil, errEmptyCancelScope
	}
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = cancelItemsTx(ctx, tx, tenantID, col, val, reason, row)
		return e
	})
	if err != nil {
		return nil, err
	}
	sortItemsOldestFirst(out)
	return out, nil
}

// cancelItemsTx cancels the tenant's active items whose col equals val with
// reason, each with the audit row of row (nil: audit.CertItemCancelledRow).
func cancelItemsTx(ctx context.Context, tx pgx.Tx, tenantID, col, val, reason string, row func(store.CertDeliveryItem) store.AuditRow) ([]store.CertDeliveryItem, error) {
	now := time.Now().UTC()
	out, err := queryItems(ctx, tx, "UPDATE inventory_cert_delivery_items SET state='cancelled', reason=$3, updated_at=$4, finished_at=$4"+
		" WHERE tenant_id=$1 AND "+col+"=$2 AND state IN ('pending','delivered','fetched') RETURNING "+itemCols,
		tenantID, val, reason, now)
	if err != nil {
		return nil, mapErr(err)
	}
	for _, i := range out {
		r := audit.CertItemCancelledRow(i, now)
		if row != nil {
			r = row(i)
		}
		if err := insertAuditTx(ctx, tx, r); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func sortItemsOldestFirst(list []store.CertDeliveryItem) {
	sort.Slice(list, func(i, j int) bool {
		if !list[i].CreatedAt.Equal(list[j].CreatedAt) {
			return list[i].CreatedAt.Before(list[j].CreatedAt)
		}
		return list[i].ID < list[j].ID
	})
}

// GetHostCertificate implements repo.CertDeliveryStore.
func (d *DB) GetHostCertificate(ctx context.Context, tenantID, hostID, name string) (out store.HostCertificate, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanHostCert(tx.QueryRow(ctx, "SELECT "+hostCertCols+" FROM inventory_host_certificates WHERE tenant_id=$1 AND host_id=$2 AND name=$3",
			tenantID, hostID, name))
		return mapErr(e)
	})
	return out, err
}

// ListHostCertificates implements repo.CertDeliveryStore.
func (d *DB) ListHostCertificates(ctx context.Context, tenantID, hostID string) (out []store.HostCertificate, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+hostCertCols+" FROM inventory_host_certificates WHERE tenant_id=$1 AND host_id=$2 ORDER BY name", tenantID, hostID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			h, e := scanHostCert(rows)
			if e != nil {
				return e
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// PurgeCertItems implements repo.CertDeliveryStore (system scope).
func (d *DB) PurgeCertItems(ctx context.Context, olderThan time.Time) (n int64, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `DELETE FROM inventory_cert_delivery_items
			WHERE state NOT IN ('pending','delivered','fetched') AND updated_at < $1`, olderThan)
		if e != nil {
			return e
		}
		n = ct.RowsAffected()
		_, e = tx.Exec(ctx, `DELETE FROM inventory_cert_deliveries d WHERE d.created_at < $1
			AND NOT EXISTS (SELECT 1 FROM inventory_cert_delivery_items i WHERE i.delivery_id = d.id)`, olderThan)
		return e
	})
	return n, err
}

// ExtendCertDelivery implements repo.CertDeliveryStore.
func (d *DB) ExtendCertDelivery(ctx context.Context, tenantID, id string, at time.Time) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE inventory_cert_deliveries SET expires_at = greatest(expires_at, $3) WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, at)
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

// ListStaleCertItems implements repo.CertDeliveryStore (system scope).
func (d *DB) ListStaleCertItems(ctx context.Context, now, reportBefore time.Time, limit int) (out []repo.StaleCertItem, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		q := `WITH i AS (SELECT it.*, d.expires_at AS delivery_expires_at FROM inventory_cert_delivery_items it
			JOIN inventory_cert_deliveries d ON d.id = it.delivery_id
			WHERE it.state IN ('pending','delivered','fetched')
			  AND (d.expires_at < $1 OR (it.state = 'fetched' AND it.updated_at < $2)))
			SELECT delivery_expires_at, ` + itemCols + ` FROM i ORDER BY created_at, id`
		args := []any{now, reportBefore}
		if limit > 0 {
			q += " LIMIT $3"
			args = append(args, limit)
		}
		rows, e := tx.Query(ctx, q, args...)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var s repo.StaleCertItem
			if e := rows.Scan(append([]any{&s.ExpiresAt}, itemDest(&s.Item)...)...); e != nil {
				return e
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// ListHostCertificatesByName implements repo.CertDeliveryStore.
func (d *DB) ListHostCertificatesByName(ctx context.Context, tenantID, name string) (out []store.HostCertificate, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+hostCertCols+" FROM inventory_host_certificates WHERE tenant_id=$1 AND name=$2 ORDER BY host_id", tenantID, name)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			h, e := scanHostCert(rows)
			if e != nil {
				return e
			}
			out = append(out, h)
		}
		return rows.Err()
	})
	return out, err
}

// ListActiveCertItemsByName implements repo.CertDeliveryStore.
func (d *DB) ListActiveCertItemsByName(ctx context.Context, tenantID, name string) (out []store.CertDeliveryItem, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = queryItems(ctx, tx, "SELECT "+itemCols+` FROM inventory_cert_delivery_items
			WHERE tenant_id=$1 AND name=$2 AND state IN ('pending','delivered','fetched') ORDER BY created_at, id`, tenantID, name)
		return e
	})
	return out, err
}

// RevokeHostCertificates implements repo.CertDeliveryStore.
func (d *DB) RevokeHostCertificates(ctx context.Context, tenantID, certificateID string, at time.Time, row func(store.HostCertificate) store.AuditRow) (out []store.HostCertificate, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		out = nil
		rows, e := tx.Query(ctx, `UPDATE inventory_host_certificates SET revoked_at=$3, updated_at=$3
			WHERE tenant_id=$1 AND certificate_id=$2 AND revoked_at IS NULL RETURNING `+hostCertCols, tenantID, certificateID, at)
		if e != nil {
			return e
		}
		for rows.Next() {
			h, e := scanHostCert(rows)
			if e != nil {
				rows.Close()
				return e
			}
			out = append(out, h)
		}
		rows.Close()
		if e := rows.Err(); e != nil {
			return e
		}
		sort.Slice(out, func(i, j int) bool {
			return out[i].HostID < out[j].HostID || (out[i].HostID == out[j].HostID && out[i].Name < out[j].Name)
		})
		for _, h := range out {
			if e := insertAuditTx(ctx, tx, row(h)); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		out = nil
	}
	return out, err
}
