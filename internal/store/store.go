package store

import (
	"context"
	"embed"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Store is the pgx pool with tenant-scoped transactions.
type Store struct{ pool *pgxpool.Pool }

// Open connects with the application role.
func Open(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases the pool.
func (s *Store) Close() { s.pool.Close() }

// Ping reports database reachability (health).
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies the embedded migrations (migration role) under an advisory lock.
func Migrate(ctx context.Context, dsn string) error {
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	db := stdlib.OpenDB(*cfg)
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(ctx, "SELECT pg_advisory_lock(8241110)"); err != nil {
		return fmt.Errorf("store: migrate lock: %w", err)
	}
	defer func() { _, _ = db.ExecContext(ctx, "SELECT pg_advisory_unlock(8241110)") }()
	goose.SetBaseFS(migrations)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return err
	}
	if err := goose.UpContext(ctx, db, "migrations"); err != nil {
		return fmt.Errorf("store: migrate: %w", err)
	}
	return nil
}

// Scope selects which RLS settings a transaction runs under. System-scoped
// transactions (job claiming, cleanup, audit writing) set app.system so the RLS
// policy admits cross-tenant reads/writes for the trusted worker paths.
type Scope struct {
	TenantID string // app.tenant_id
	System   bool   // app.system
}

// Tx runs fn in a transaction with the tenant scope applied via set_config(local).
func (s *Store) Tx(ctx context.Context, scope Scope, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if scope.TenantID != "" {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', $1, true)", scope.TenantID); err != nil {
			return err
		}
	}
	if scope.System {
		if _, err := tx.Exec(ctx, "SELECT set_config('app.system', 'on', true)"); err != nil {
			return err
		}
		// The RLS policy casts app.tenant_id to uuid; under system scope no tenant
		// is set, so pin it to the nil uuid to keep the cast valid (access is
		// granted by app.system='on', which the nil uuid never satisfies itself).
		if scope.TenantID == "" {
			if _, err := tx.Exec(ctx, "SELECT set_config('app.tenant_id', '00000000-0000-0000-0000-000000000000', true)"); err != nil {
				return err
			}
		}
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Errors.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
)

func notFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func conflict(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" {
		return ErrConflict
	}
	return err
}
