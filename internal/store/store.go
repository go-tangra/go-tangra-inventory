package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// InventoryRecord represents a stored inventory row.
type InventoryRecord struct {
	ID            int64
	Hostname      string
	Username      string
	SystemUUID    string
	SystemSerial  string
	CollectedAt   time.Time
	StoredAt      time.Time
	InventoryJSON string
}

// ListFilter holds optional query parameters for listing inventories.
type ListFilter struct {
	Hostname        string
	Username        string
	SystemUUID      string
	CollectedAfter  *time.Time
	CollectedBefore *time.Time
	PageSize        int
	Page            int
}

// Store provides CRUD operations for inventory records.
type Store struct {
	db *sql.DB
}

// New opens the SQLite database at path and runs migrations.
func New(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	db.SetMaxOpenConns(1)

	if _, err := db.Exec(createTableSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("run migrations: %w", err)
	}

	return &Store{db: db}, nil
}

// Close closes the database connection.
func (s *Store) Close() error {
	return s.db.Close()
}

// Insert stores an inventory record and returns the new ID and stored_at time.
func (s *Store) Insert(ctx context.Context, rec *InventoryRecord) (int64, time.Time, error) {
	storedAt := time.Now().UTC()
	result, err := s.db.ExecContext(ctx,
		`INSERT INTO inventories (hostname, username, system_uuid, system_serial, collected_at, stored_at, inventory_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		rec.Hostname,
		rec.Username,
		rec.SystemUUID,
		rec.SystemSerial,
		rec.CollectedAt.UTC().Format(time.RFC3339),
		storedAt.Format(time.RFC3339),
		rec.InventoryJSON,
	)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("insert inventory: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("get last insert id: %w", err)
	}

	return id, storedAt, nil
}

// Get retrieves an inventory record by ID.
func (s *Store) Get(ctx context.Context, id int64) (*InventoryRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hostname, username, system_uuid, system_serial, collected_at, stored_at, inventory_json
		 FROM inventories WHERE id = ?`, id)

	return scanRecord(row)
}

// GetLatestByHostname retrieves the most recent inventory for a hostname.
func (s *Store) GetLatestByHostname(ctx context.Context, hostname string) (*InventoryRecord, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, hostname, username, system_uuid, system_serial, collected_at, stored_at, inventory_json
		 FROM inventories WHERE hostname = ? ORDER BY collected_at DESC LIMIT 1`, hostname)

	return scanRecord(row)
}

// Delete removes an inventory record by ID.
func (s *Store) Delete(ctx context.Context, id int64) error {
	result, err := s.db.ExecContext(ctx, `DELETE FROM inventories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete inventory: %w", err)
	}

	n, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if n == 0 {
		return sql.ErrNoRows
	}

	return nil
}

// List returns inventory summaries matching the given filter.
func (s *Store) List(ctx context.Context, f ListFilter) ([]InventoryRecord, int, error) {
	where, args := buildWhere(f)

	// Count total matching rows.
	var total int
	countQuery := "SELECT COUNT(*) FROM inventories" + where
	if err := s.db.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count inventories: %w", err)
	}

	// Fetch page.
	pageSize := f.PageSize
	if pageSize <= 0 {
		pageSize = 50
	}
	page := f.Page
	if page <= 0 {
		page = 1
	}
	offset := (page - 1) * pageSize

	query := `SELECT id, hostname, username, system_uuid, system_serial, collected_at, stored_at, ''
		FROM inventories` + where + ` ORDER BY collected_at DESC LIMIT ? OFFSET ?`
	args = append(args, pageSize, offset)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list inventories: %w", err)
	}
	defer rows.Close()

	var records []InventoryRecord
	for rows.Next() {
		rec, err := scanRecordFromRows(rows)
		if err != nil {
			return nil, 0, err
		}
		records = append(records, *rec)
	}

	return records, total, rows.Err()
}

// Purge deletes inventory records older than the given duration.
func (s *Store) Purge(ctx context.Context, olderThan time.Duration) (int64, error) {
	cutoff := time.Now().UTC().Add(-olderThan).Format(time.RFC3339)
	result, err := s.db.ExecContext(ctx, `DELETE FROM inventories WHERE collected_at < ?`, cutoff)
	if err != nil {
		return 0, fmt.Errorf("purge inventories: %w", err)
	}
	return result.RowsAffected()
}

func buildWhere(f ListFilter) (string, []any) {
	var conditions []string
	var args []any

	if f.Hostname != "" {
		conditions = append(conditions, "hostname = ?")
		args = append(args, f.Hostname)
	}
	if f.Username != "" {
		conditions = append(conditions, "username = ?")
		args = append(args, f.Username)
	}
	if f.SystemUUID != "" {
		conditions = append(conditions, "system_uuid = ?")
		args = append(args, f.SystemUUID)
	}
	if f.CollectedAfter != nil {
		conditions = append(conditions, "collected_at >= ?")
		args = append(args, f.CollectedAfter.UTC().Format(time.RFC3339))
	}
	if f.CollectedBefore != nil {
		conditions = append(conditions, "collected_at <= ?")
		args = append(args, f.CollectedBefore.UTC().Format(time.RFC3339))
	}

	if len(conditions) == 0 {
		return "", nil
	}

	where := " WHERE "
	for i, c := range conditions {
		if i > 0 {
			where += " AND "
		}
		where += c
	}
	return where, args
}

type scanner interface {
	Scan(dest ...any) error
}

func scanRecord(row *sql.Row) (*InventoryRecord, error) {
	var rec InventoryRecord
	var collectedAt, storedAt string
	err := row.Scan(&rec.ID, &rec.Hostname, &rec.Username, &rec.SystemUUID, &rec.SystemSerial, &collectedAt, &storedAt, &rec.InventoryJSON)
	if err != nil {
		return nil, err
	}

	rec.CollectedAt, _ = time.Parse(time.RFC3339, collectedAt)
	rec.StoredAt, _ = time.Parse(time.RFC3339, storedAt)

	return &rec, nil
}

func scanRecordFromRows(rows *sql.Rows) (*InventoryRecord, error) {
	var rec InventoryRecord
	var collectedAt, storedAt string
	err := rows.Scan(&rec.ID, &rec.Hostname, &rec.Username, &rec.SystemUUID, &rec.SystemSerial, &collectedAt, &storedAt, &rec.InventoryJSON)
	if err != nil {
		return nil, err
	}

	rec.CollectedAt, _ = time.Parse(time.RFC3339, collectedAt)
	rec.StoredAt, _ = time.Parse(time.RFC3339, storedAt)

	return &rec, nil
}
