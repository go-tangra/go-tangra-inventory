package repodb

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Feature 023: agent releases (global tables), agent platforms, upgrade
// requests and policies (tenant tables under RLS). Every request and policy
// change appends its audit rows in the same transaction.

// insertAuditTx appends an audit row inside tx (transactional audit).
func insertAuditTx(ctx context.Context, tx pgx.Tx, row store.AuditRow) error {
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
	_, err := tx.Exec(ctx, `INSERT INTO inventory_audit_events
		(id, tenant_id, at, actor_kind, actor_id, action, subject_kind, subject_id, outcome, reason, detail)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11::jsonb)`,
		id, row.TenantID, at, row.ActorKind, row.ActorID, row.Action, row.SubjectKind, row.SubjectID, row.Outcome, row.Reason, detail)
	return err
}

// ---- releases

func (d *DB) ImportAgentRelease(ctx context.Context, rel store.AgentRelease, open repo.ArtifactOpener, chunkBytes int) (imported bool, err error) {
	if chunkBytes <= 0 {
		chunkBytes = 1 << 20
	}
	err = d.system(ctx, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('inventory_agent_release:' || $1, 0))", rel.Version); e != nil {
			return e
		}
		var sha string
		e := tx.QueryRow(ctx, "SELECT manifest_sha256 FROM inventory_agent_releases WHERE version=$1", rel.Version).Scan(&sha)
		switch {
		case e == nil:
			if sha != rel.ManifestSHA256 {
				return repo.ErrConflict
			}
			var incomplete int
			if e := tx.QueryRow(ctx, "SELECT count(*) FROM inventory_agent_artifacts WHERE version=$1 AND NOT complete", rel.Version).Scan(&incomplete); e != nil {
				return e
			}
			if incomplete == 0 {
				return nil
			}
			if _, e := tx.Exec(ctx, "DELETE FROM inventory_agent_releases WHERE version=$1", rel.Version); e != nil {
				return e
			}
		case errors.Is(e, pgx.ErrNoRows):
		default:
			return e
		}
		importedAt := rel.ImportedAt
		if importedAt.IsZero() {
			importedAt = time.Now().UTC()
		}
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_agent_releases (version, manifest, signature, key_id, manifest_sha256, source, imported_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7)`, rel.Version, rel.Manifest, rel.Signature, rel.KeyID, rel.ManifestSHA256, rel.Source, importedAt); e != nil {
			return mapErr(e)
		}
		for _, a := range rel.Artifacts {
			a.Version = rel.Version
			if e := importArtifact(ctx, tx, a, open, chunkBytes); e != nil {
				return e
			}
		}
		imported = true
		return nil
	})
	return imported, mapErr(err)
}

func importArtifact(ctx context.Context, tx pgx.Tx, a store.AgentArtifact, open repo.ArtifactOpener, chunkBytes int) error {
	if _, e := tx.Exec(ctx, `INSERT INTO inventory_agent_artifacts (version, os, arch, install_type, file, size, sha256)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, a.Version, a.OS, a.Arch, a.InstallType, a.File, a.Size, a.SHA256); e != nil {
		return e
	}
	rc, err := open(a)
	if err != nil {
		return err
	}
	defer rc.Close()
	buf := make([]byte, chunkBytes)
	for seq := 0; ; seq++ {
		n, rerr := io.ReadFull(rc, buf)
		if n > 0 {
			if _, e := tx.Exec(ctx, `INSERT INTO inventory_agent_artifact_chunks (version, os, arch, install_type, seq, data)
				VALUES ($1,$2,$3,$4,$5,$6)`, a.Version, a.OS, a.Arch, a.InstallType, seq, buf[:n]); e != nil {
				return e
			}
		}
		if errors.Is(rerr, io.EOF) || errors.Is(rerr, io.ErrUnexpectedEOF) {
			break
		}
		if rerr != nil {
			return rerr
		}
	}
	if err := rc.Close(); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE inventory_agent_artifacts SET complete=true
		WHERE version=$1 AND os=$2 AND arch=$3 AND install_type=$4`, a.Version, a.OS, a.Arch, a.InstallType)
	return err
}

const releaseCols = `version, manifest, signature, key_id, manifest_sha256, source, imported_at`

func scanRelease(sc scanner) (store.AgentRelease, error) {
	var r store.AgentRelease
	err := sc.Scan(&r.Version, &r.Manifest, &r.Signature, &r.KeyID, &r.ManifestSHA256, &r.Source, &r.ImportedAt)
	return r, err
}

func loadArtifacts(ctx context.Context, tx pgx.Tx, versions map[string]*store.AgentRelease) error {
	rows, err := tx.Query(ctx, `SELECT version, os, arch, install_type, file, size, sha256, complete
		FROM inventory_agent_artifacts ORDER BY version, os, arch, install_type`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a store.AgentArtifact
		if err := rows.Scan(&a.Version, &a.OS, &a.Arch, &a.InstallType, &a.File, &a.Size, &a.SHA256, &a.Complete); err != nil {
			return err
		}
		if r, ok := versions[a.Version]; ok {
			r.Artifacts = append(r.Artifacts, a)
		}
	}
	return rows.Err()
}

func (d *DB) ListAgentReleases(ctx context.Context) (out []store.AgentRelease, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+releaseCols+" FROM inventory_agent_releases ORDER BY version")
		if e != nil {
			return e
		}
		for rows.Next() {
			r, e := scanRelease(rows)
			if e != nil {
				rows.Close()
				return e
			}
			out = append(out, r)
		}
		rows.Close()
		if e := rows.Err(); e != nil {
			return e
		}
		byVersion := map[string]*store.AgentRelease{}
		for i := range out {
			byVersion[out[i].Version] = &out[i]
		}
		return loadArtifacts(ctx, tx, byVersion)
	})
	return out, err
}

func (d *DB) GetAgentRelease(ctx context.Context, version string) (out store.AgentRelease, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		var e error
		if out, e = scanRelease(tx.QueryRow(ctx, "SELECT "+releaseCols+" FROM inventory_agent_releases WHERE version=$1", version)); e != nil {
			return mapErr(e)
		}
		return loadArtifacts(ctx, tx, map[string]*store.AgentRelease{version: &out})
	})
	return out, err
}

func (d *DB) ReadArtifactChunk(ctx context.Context, a store.AgentArtifact, seq int) (data []byte, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		return mapErr(tx.QueryRow(ctx, `SELECT data FROM inventory_agent_artifact_chunks
			WHERE version=$1 AND os=$2 AND arch=$3 AND install_type=$4 AND seq=$5`, a.Version, a.OS, a.Arch, a.InstallType, seq).Scan(&data))
	})
	return data, err
}

func (d *DB) DeleteAgentRelease(ctx context.Context, version string) error {
	return d.system(ctx, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, "DELETE FROM inventory_agent_releases WHERE version=$1", version)
		if e != nil {
			return e
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

func (d *DB) ProtectedAgentVersions(ctx context.Context) (out []string, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, `SELECT target_version FROM inventory_agent_upgrade_policy WHERE target_version <> ''
			UNION SELECT target_version FROM inventory_agent_upgrades WHERE state IN ('pending','delivered','downloading','installing')
			ORDER BY 1`)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var v string
			if e := rows.Scan(&v); e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}

// ---- agents

func (d *DB) ListAgents(ctx context.Context, tenantID string) (out []store.Agent, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+agentCols+" FROM inventory_agents WHERE tenant_id=$1 AND NOT revoked ORDER BY id", tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			a, e := scanAgent(rows)
			if e != nil {
				return e
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}

func (d *DB) SetAgentPlatform(ctx context.Context, agentID, os, arch, installType string, caps []string, at time.Time) error {
	if caps == nil {
		caps = []string{}
	}
	return d.system(ctx, func(tx pgx.Tx) error {
		ct, e := tx.Exec(ctx, `UPDATE inventory_agents SET os=$2, arch=$3, install_type=$4, capabilities=$5, platform_seen_at=$6 WHERE id=$1`,
			agentID, os, arch, installType, caps, at)
		if e != nil {
			return mapErr(e)
		}
		if ct.RowsAffected() == 0 {
			return repo.ErrNotFound
		}
		return nil
	})
}

// ---- upgrade requests

const upgradeCols = `id, tenant_id, agent_id, coalesce(host_id::text,''), from_version, target_version, allow_downgrade, state, origin,
	requested_by, reason, attempts, created_at, updated_at, expires_at, delivered_at, started_at, finished_at`

func scanUpgrade(sc scanner) (store.AgentUpgrade, error) {
	var u store.AgentUpgrade
	err := sc.Scan(&u.ID, &u.TenantID, &u.AgentID, &u.HostID, &u.FromVersion, &u.TargetVersion, &u.AllowDowngrade, &u.State, &u.Origin,
		&u.RequestedBy, &u.Reason, &u.Attempts, &u.CreatedAt, &u.UpdatedAt, &u.ExpiresAt, &u.DeliveredAt, &u.StartedAt, &u.FinishedAt)
	return u, err
}

func nullUUID(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func (d *DB) CreateAgentUpgrade(ctx context.Context, u store.AgentUpgrade, row store.AuditRow) error {
	return d.tenant(ctx, u.TenantID, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO inventory_agent_upgrades
			(id, tenant_id, agent_id, host_id, from_version, target_version, allow_downgrade, state, origin, requested_by, reason,
			 attempts, created_at, updated_at, expires_at, delivered_at, started_at, finished_at)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
			u.ID, u.TenantID, u.AgentID, nullUUID(u.HostID), u.FromVersion, u.TargetVersion, u.AllowDowngrade, u.State, u.Origin,
			u.RequestedBy, u.Reason, u.Attempts, u.CreatedAt, u.UpdatedAt, u.ExpiresAt, u.DeliveredAt, u.StartedAt, u.FinishedAt)
		if e != nil {
			return mapErr(e)
		}
		return insertAuditTx(ctx, tx, row)
	})
}

func (d *DB) UpdateAgentUpgrade(ctx context.Context, tenantID, id string, fn func(*store.AgentUpgrade) ([]store.AuditRow, error)) (out store.AgentUpgrade, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		u, e := scanUpgrade(tx.QueryRow(ctx, "SELECT "+upgradeCols+" FROM inventory_agent_upgrades WHERE tenant_id=$1 AND id=$2 FOR UPDATE", tenantID, id))
		if e != nil {
			return mapErr(e)
		}
		out = u
		rows, e := fn(&u)
		if e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `UPDATE inventory_agent_upgrades SET from_version=$3, target_version=$4, allow_downgrade=$5, state=$6,
			reason=$7, attempts=$8, updated_at=$9, expires_at=$10, delivered_at=$11, started_at=$12, finished_at=$13
			WHERE tenant_id=$1 AND id=$2`, tenantID, id, u.FromVersion, u.TargetVersion, u.AllowDowngrade, u.State, u.Reason, u.Attempts,
			u.UpdatedAt, u.ExpiresAt, u.DeliveredAt, u.StartedAt, u.FinishedAt); e != nil {
			return mapErr(e)
		}
		for _, r := range rows {
			if e := insertAuditTx(ctx, tx, r); e != nil {
				return e
			}
		}
		out = u
		return nil
	})
	return out, err
}

func (d *DB) GetAgentUpgrade(ctx context.Context, tenantID, id string) (out store.AgentUpgrade, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		var e error
		out, e = scanUpgrade(tx.QueryRow(ctx, "SELECT "+upgradeCols+" FROM inventory_agent_upgrades WHERE tenant_id=$1 AND id=$2", tenantID, id))
		return mapErr(e)
	})
	return out, err
}

func queryUpgrades(ctx context.Context, tx pgx.Tx, q string, args ...any) ([]store.AgentUpgrade, error) {
	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.AgentUpgrade
	for rows.Next() {
		u, err := scanUpgrade(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (d *DB) ListAgentUpgrades(ctx context.Context, tenantID string, f repo.UpgradeFilter) (out []store.AgentUpgrade, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		q := "SELECT " + upgradeCols + " FROM inventory_agent_upgrades WHERE tenant_id=$1"
		args := []any{tenantID}
		if f.State != "" {
			args = append(args, f.State)
			q += fmt.Sprintf(" AND state=$%d", len(args))
		}
		if f.AgentID != "" {
			args = append(args, f.AgentID)
			q += fmt.Sprintf(" AND agent_id=$%d", len(args))
		}
		if f.CursorID != "" {
			args = append(args, f.CursorID)
			q += fmt.Sprintf(" AND (created_at, id) < (SELECT created_at, id FROM inventory_agent_upgrades WHERE tenant_id=$1 AND id=$%d)", len(args))
		}
		q += " ORDER BY created_at DESC, id DESC"
		if f.Limit > 0 {
			args = append(args, f.Limit)
			q += fmt.Sprintf(" LIMIT $%d", len(args))
		}
		var e error
		out, e = queryUpgrades(ctx, tx, q, args...)
		return e
	})
	return out, err
}

func (d *DB) LatestAgentUpgrades(ctx context.Context, tenantID string) (map[string]store.AgentUpgrade, error) {
	out := map[string]store.AgentUpgrade{}
	err := d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		list, e := queryUpgrades(ctx, tx, "SELECT DISTINCT ON (agent_id) "+upgradeCols+
			" FROM inventory_agent_upgrades WHERE tenant_id=$1 ORDER BY agent_id, created_at DESC, id DESC", tenantID)
		for _, u := range list {
			out[u.AgentID] = u
		}
		return e
	})
	return out, err
}

func (d *DB) ListStaleUpgrades(ctx context.Context, now, progressBefore time.Time, limit int) (out []store.AgentUpgrade, err error) {
	if limit <= 0 {
		limit = 1000
	}
	err = d.system(ctx, func(tx pgx.Tx) error {
		var e error
		out, e = queryUpgrades(ctx, tx, "SELECT "+upgradeCols+` FROM inventory_agent_upgrades
			WHERE (state IN ('pending','delivered') AND expires_at < $1)
			   OR (state IN ('downloading','installing') AND updated_at < $2)
			ORDER BY id LIMIT $3`, now, progressBefore, limit)
		return e
	})
	return out, err
}

// ---- policies

const policyCols = `tenant_id, enabled, window_start, window_end, timezone, max_concurrent, target_version, paused, paused_reason, updated_by, updated_at`

func scanPolicy(sc scanner) (store.AgentUpgradePolicy, error) {
	var p store.AgentUpgradePolicy
	err := sc.Scan(&p.TenantID, &p.Enabled, &p.WindowStart, &p.WindowEnd, &p.Timezone, &p.MaxConcurrent, &p.TargetVersion,
		&p.Paused, &p.PausedReason, &p.UpdatedBy, &p.UpdatedAt)
	return p, err
}

func (d *DB) GetUpgradePolicy(ctx context.Context, tenantID string) (out store.AgentUpgradePolicy, found bool, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		p, e := scanPolicy(tx.QueryRow(ctx, "SELECT "+policyCols+" FROM inventory_agent_upgrade_policy WHERE tenant_id=$1", tenantID))
		switch {
		case e == nil:
			out, found = p, true
			return nil
		case errors.Is(e, pgx.ErrNoRows):
			out = store.DefaultUpgradePolicy(tenantID)
			return nil
		}
		return e
	})
	return out, found, err
}

func (d *DB) UpdateUpgradePolicy(ctx context.Context, tenantID string, fn func(*store.AgentUpgradePolicy) ([]store.AuditRow, error)) (out store.AgentUpgradePolicy, err error) {
	err = d.tenant(ctx, tenantID, func(tx pgx.Tx) error {
		p, e := scanPolicy(tx.QueryRow(ctx, "SELECT "+policyCols+" FROM inventory_agent_upgrade_policy WHERE tenant_id=$1 FOR UPDATE", tenantID))
		switch {
		case e == nil:
		case errors.Is(e, pgx.ErrNoRows):
			p = store.DefaultUpgradePolicy(tenantID)
		default:
			return e
		}
		out = p
		rows, e := fn(&p)
		if e != nil {
			return e
		}
		if p.UpdatedAt.IsZero() {
			p.UpdatedAt = time.Now().UTC()
		}
		if _, e := tx.Exec(ctx, `INSERT INTO inventory_agent_upgrade_policy (`+policyCols+`)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
			ON CONFLICT (tenant_id) DO UPDATE SET enabled=EXCLUDED.enabled, window_start=EXCLUDED.window_start,
			  window_end=EXCLUDED.window_end, timezone=EXCLUDED.timezone, max_concurrent=EXCLUDED.max_concurrent,
			  target_version=EXCLUDED.target_version, paused=EXCLUDED.paused, paused_reason=EXCLUDED.paused_reason,
			  updated_by=EXCLUDED.updated_by, updated_at=EXCLUDED.updated_at`,
			tenantID, p.Enabled, p.WindowStart, p.WindowEnd, p.Timezone, p.MaxConcurrent, p.TargetVersion, p.Paused,
			p.PausedReason, p.UpdatedBy, p.UpdatedAt); e != nil {
			return mapErr(e)
		}
		for _, r := range rows {
			if e := insertAuditTx(ctx, tx, r); e != nil {
				return e
			}
		}
		p.TenantID = tenantID
		out = p
		return nil
	})
	return out, err
}

func (d *DB) ListEnabledUpgradePolicies(ctx context.Context) (out []store.AgentUpgradePolicy, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		rows, e := tx.Query(ctx, "SELECT "+policyCols+" FROM inventory_agent_upgrade_policy WHERE enabled ORDER BY tenant_id")
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			p, e := scanPolicy(rows)
			if e != nil {
				return e
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	return out, err
}

func (d *DB) TryTenantLock(ctx context.Context, key string, fn func(context.Context) error) (acquired bool, err error) {
	err = d.system(ctx, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, "SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))", "inventory:"+strings.TrimSpace(key)).Scan(&acquired); e != nil {
			return e
		}
		if !acquired {
			return nil
		}
		return fn(ctx)
	})
	return acquired, err
}
