package repodb

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Automatic enrollment (feature 029).

const autoKeyCols = `id, tenant_id, key_id, name, secret_sealed, allowed_cidrs::text[], enabled, expires_at, max_enrollments,
	enrollments, last_used_at, last_used_ip, created_by, created_at, updated_at`

func scanAutoKey(sc scanner) (store.AutoEnrollKey, error) {
	var k store.AutoEnrollKey
	if err := sc.Scan(&k.ID, &k.TenantID, &k.KeyID, &k.Name, &k.SecretSealed, &k.AllowedCIDRs, &k.Enabled, &k.ExpiresAt,
		&k.MaxEnrollments, &k.Enrollments, &k.LastUsedAt, &k.LastUsedIP, &k.CreatedBy, &k.CreatedAt, &k.UpdatedAt); err != nil {
		return store.AutoEnrollKey{}, err
	}
	return k, nil
}

func (d *DB) GetAutoEnrollSettings(ctx context.Context, tenantID string) (out store.AutoEnrollSettings, found bool, err error) {
	out.TenantID = tenantID
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		e := tx.QueryRow(ctx, `SELECT enabled, updated_by, updated_at FROM inventory_auto_enroll_settings WHERE tenant_id=$1`, tenantID).
			Scan(&out.Enabled, &out.UpdatedBy, &out.UpdatedAt)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		if e != nil {
			return e
		}
		found = true
		return nil
	})
	return
}

func (d *DB) PutAutoEnrollSettings(ctx context.Context, s store.AutoEnrollSettings, row store.AuditRow) error {
	return d.tenant(ctx, s.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_auto_enroll_settings (tenant_id, enabled, updated_by, updated_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (tenant_id) DO UPDATE SET enabled=EXCLUDED.enabled, updated_by=EXCLUDED.updated_by, updated_at=EXCLUDED.updated_at`,
			s.TenantID, s.Enabled, s.UpdatedBy, s.UpdatedAt); e != nil {
			return mapErr(e)
		}
		return insertAuditTx(ctx, tx, row)
	})
}

func (d *DB) ListAutoEnrollKeys(ctx context.Context, tenantID string) (out []store.AutoEnrollKey, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+autoKeyCols+" FROM inventory_auto_enroll_keys WHERE tenant_id=$1 ORDER BY name", tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			k, e := scanAutoKey(rows)
			if e != nil {
				return e
			}
			out = append(out, k)
		}
		return rows.Err()
	})
	return
}

func (d *DB) CreateAutoEnrollKey(ctx context.Context, k store.AutoEnrollKey, row store.AuditRow) error {
	return d.tenant(ctx, k.TenantID, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_auto_enroll_keys
			(id, tenant_id, key_id, name, secret_sealed, allowed_cidrs, enabled, expires_at, max_enrollments, enrollments, created_by, created_at, updated_at)
			VALUES ($1,$2,$3,$4,$5,$6::cidr[],$7,$8,$9,0,$10,$11,$11)`,
			k.ID, k.TenantID, k.KeyID, k.Name, k.SecretSealed, k.AllowedCIDRs, k.Enabled, k.ExpiresAt, k.MaxEnrollments, k.CreatedBy, k.CreatedAt); e != nil {
			return mapErr(e)
		}
		return insertAuditTx(ctx, tx, row)
	})
}

func (d *DB) UpdateAutoEnrollKey(ctx context.Context, tenantID, id string, fn func(*store.AutoEnrollKey) (store.AuditRow, error)) (out store.AutoEnrollKey, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		k, e := scanAutoKey(tx.QueryRow(ctx, "SELECT "+autoKeyCols+" FROM inventory_auto_enroll_keys WHERE tenant_id=$1 AND id=$2 FOR UPDATE", tenantID, id))
		if e != nil {
			return mapErr(e)
		}
		row, e := fn(&k)
		if e != nil {
			return e
		}
		if _, e = tx.Exec(ctx, `UPDATE inventory_auto_enroll_keys SET name=$3, secret_sealed=$4, allowed_cidrs=$5::cidr[], enabled=$6,
			expires_at=$7, max_enrollments=$8, updated_at=$9 WHERE tenant_id=$1 AND id=$2`,
			tenantID, id, k.Name, k.SecretSealed, k.AllowedCIDRs, k.Enabled, k.ExpiresAt, k.MaxEnrollments, k.UpdatedAt); e != nil {
			return mapErr(e)
		}
		out = k
		return insertAuditTx(ctx, tx, row)
	})
	return
}

func (d *DB) DeleteAutoEnrollKey(ctx context.Context, tenantID, id string, row store.AuditRow) error {
	return d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var keyID string
		if e := tx.QueryRow(ctx, "DELETE FROM inventory_auto_enroll_keys WHERE tenant_id=$1 AND id=$2 RETURNING key_id", tenantID, id).Scan(&keyID); e != nil {
			return mapErr(e)
		}
		if _, e := tx.Exec(ctx, "DELETE FROM inventory_auto_enroll_nonces WHERE key_id=$1", keyID); e != nil {
			return e
		}
		return insertAuditTx(ctx, tx, row)
	})
}

func (d *DB) LookupAutoEnrollKey(ctx context.Context, keyID string) (out store.AutoEnrollKey, enabled bool, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		var e error
		if out, e = scanAutoKey(tx.QueryRow(ctx, "SELECT "+autoKeyCols+" FROM inventory_auto_enroll_keys WHERE key_id=$1", keyID)); e != nil {
			return mapErr(e)
		}
		e = tx.QueryRow(ctx, "SELECT enabled FROM inventory_auto_enroll_settings WHERE tenant_id=$1", out.TenantID).Scan(&enabled)
		if errors.Is(e, pgx.ErrNoRows) {
			return nil
		}
		return e
	})
	return
}

func (d *DB) EnrollWithAutoKey(ctx context.Context, e store.AutoEnrollment) error {
	return d.tenant(ctx, e.TenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "DELETE FROM inventory_auto_enroll_nonces WHERE key_id=$1 AND seen_at < $2", e.KeyID, e.PruneBefore); err != nil {
			return err
		}
		ct, err := tx.Exec(ctx, `INSERT INTO inventory_auto_enroll_nonces (tenant_id, key_id, nonce, seen_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT DO NOTHING`, e.TenantID, e.KeyID, e.Nonce, e.At)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrReplay
		}
		// The key row is locked by the UPDATE, so concurrent enrollments
		// cannot overrun max_enrollments.
		ct, err = tx.Exec(ctx, `UPDATE inventory_auto_enroll_keys k SET enrollments = enrollments + 1, last_used_at=$4, last_used_ip=$5
			WHERE k.tenant_id=$1 AND k.id=$2 AND k.key_id=$3 AND k.enabled
			  AND (k.expires_at IS NULL OR k.expires_at > $4)
			  AND (k.max_enrollments = 0 OR k.enrollments < k.max_enrollments)
			  AND EXISTS (SELECT 1 FROM inventory_auto_enroll_settings s WHERE s.tenant_id=k.tenant_id AND s.enabled)`,
			e.TenantID, e.KeyUUID, e.KeyID, e.At, e.IP)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrConflict
		}
		if err := insertAgentTx(ctx, tx, e.Agent); err != nil {
			return err
		}
		return insertAuditTx(ctx, tx, e.Audit)
	})
}
