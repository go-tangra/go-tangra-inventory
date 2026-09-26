package repodb

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// epoch stands in for a NULL report_changed_at in orderings and cursors, so
// hosts never reported since migration 0005 sort first.
var epoch = time.Unix(0, 0).UTC()

// SetReportDigest records digest/changedAt only when the stored digest differs
// (compare-and-set in one statement, tenant scope).
func (d *DB) SetReportDigest(ctx context.Context, tenantID, hostID, digest string, changedAt time.Time) (changed bool, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE inventory_hosts SET report_digest=$3, report_changed_at=$4
			WHERE tenant_id=$1 AND id=$2 AND report_digest <> $3`, tenantID, hostID, digest, changedAt.UTC())
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 1 {
			changed = true
			return nil
		}
		// Either unchanged or missing: tell them apart.
		var one int
		return mapErr(tx.QueryRow(ctx, "SELECT 1 FROM inventory_hosts WHERE tenant_id=$1 AND id=$2", tenantID, hostID).Scan(&one))
	})
	return
}

// ListReportTenants lists, in system scope, the tenants having a host whose
// report changed after since (zero: every tenant with hosts), oldest change
// first, and the highest change time among them (since when none).
func (d *DB) ListReportTenants(ctx context.Context, since time.Time, limit int) (ids []string, maxChanged time.Time, err error) {
	maxChanged = since
	err = d.system(ctx, func(tx pgx.Tx) error {
		q := `SELECT tenant_id::text, max(COALESCE(report_changed_at, $1)) AS m FROM inventory_hosts`
		args := []any{epoch}
		if !since.IsZero() {
			args = append(args, since.UTC())
			q += " WHERE report_changed_at > $2"
		}
		q += " GROUP BY tenant_id ORDER BY m, tenant_id"
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
			var id string
			var m time.Time
			if e := rows.Scan(&id, &m); e != nil {
				return e
			}
			ids = append(ids, id)
			if m.After(maxChanged) && !m.Equal(epoch) {
				maxChanged = m.UTC()
			}
		}
		return rows.Err()
	})
	return
}

// ListHostReportRows lists a tenant's hosts ordered by (report_changed_at, id),
// keyset-paged by f.After.
func (d *DB) ListHostReportRows(ctx context.Context, tenantID string, f store.ReportRowFilter) (out []store.Host, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var b strings.Builder
		b.WriteString("SELECT " + hostCols + " FROM inventory_hosts WHERE tenant_id=$1")
		args := []any{tenantID, epoch}
		if !f.ChangedSince.IsZero() {
			args = append(args, f.ChangedSince.UTC())
			fmt.Fprintf(&b, " AND report_changed_at > $%d", len(args))
		}
		if f.After != nil {
			at := f.After.ChangedAt
			if at.IsZero() {
				at = epoch
			}
			args = append(args, at.UTC(), f.After.ID)
			fmt.Fprintf(&b, " AND (COALESCE(report_changed_at, $2), id) > ($%d, $%d::uuid)", len(args)-1, len(args))
		}
		b.WriteString(" ORDER BY COALESCE(report_changed_at, $2), id")
		if f.Limit > 0 {
			args = append(args, f.Limit)
			fmt.Fprintf(&b, " LIMIT $%d", len(args))
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

var _ repo.Store = (*DB)(nil)
