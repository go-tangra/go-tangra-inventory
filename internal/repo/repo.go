// Package repo defines the storage contract for the inventory service. The
// concrete implementations are repodb (TimescaleDB) and memstore (in-memory).
package repo

import (
	"context"
	"errors"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Sentinel errors.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
)

// Stats is a per-tenant statistics rollup.
type Stats struct {
	HostsTotal          int64            `json:"hosts_total"`
	HostsByStatus       map[string]int64 `json:"hosts_by_status"`
	HostsByOS           map[string]int64 `json:"hosts_by_os"`
	HostsByManufacturer map[string]int64 `json:"hosts_by_manufacturer"`
	AgentsOnline        int64            `json:"agents_online"`
	AgentsOffline       int64            `json:"agents_offline"`
	StaleHosts          int64            `json:"stale_hosts"`
	SnapshotsTotal      int64            `json:"snapshots_total"`
	TotalMemoryBytes    uint64           `json:"total_memory_bytes"`
	TotalCPUCores       int64            `json:"total_cpu_cores"`
	TotalDiskBytes      uint64           `json:"total_disk_bytes"`
	TopPrograms         map[string]int64 `json:"top_programs"`
	OSVersions          map[string]int64 `json:"os_versions"`
}

// Store is the inventory persistence contract. All methods are tenant-scoped
// (tenantID is the RLS scope); the system-scope pin is used only by trusted
// worker/maintenance paths (stale marking, retention purge, system stats).
type Store interface {
	// Hosts
	ResolveHost(ctx context.Context, tenantID string, h store.Host) (store.Host, error) // upsert by identity precedence
	GetHost(ctx context.Context, tenantID, id string) (store.Host, error)
	GetHostByIdentity(ctx context.Context, tenantID string, id store.Identity) (store.Host, error)
	ListHosts(ctx context.Context, tenantID string, f store.HostFilter) ([]store.Host, error)
	SetHostTags(ctx context.Context, tenantID, id string, tags map[string]string) error
	RetireHost(ctx context.Context, tenantID, id string) error
	DeleteHost(ctx context.Context, tenantID, id string) error
	MarkStaleHosts(ctx context.Context, olderThan time.Time) (int64, error) // system scope

	// Snapshots (InsertSnapshot also writes the normalized component child rows)
	InsertSnapshot(ctx context.Context, s store.Snapshot) error
	GetSnapshot(ctx context.Context, tenantID, id string) (store.Snapshot, error)
	ListSnapshotsForHost(ctx context.Context, tenantID, hostID string, limit int, cursorID string) ([]store.Snapshot, error)
	GetLatestForHost(ctx context.Context, tenantID, hostID string) (store.Snapshot, error)
	DeleteSnapshot(ctx context.Context, tenantID, id string) error
	PurgeSnapshots(ctx context.Context, olderThan time.Time) (int64, error) // system scope, keeps each host's latest

	// Changes
	InsertChanges(ctx context.Context, changes []store.Change) error
	ListChangesForHost(ctx context.Context, tenantID, hostID string, limit int) ([]store.Change, error)
	ListChangesForSnapshot(ctx context.Context, tenantID, snapshotID string) ([]store.Change, error)

	// Agents
	CreateAgent(ctx context.Context, a store.Agent) error
	GetAgent(ctx context.Context, tenantID, id string) (store.Agent, error)
	GetAgentByID(ctx context.Context, id string) (store.Agent, error) // ingest auth: id -> tenant/host scope
	TouchAgent(ctx context.Context, id, version string, hostID string, at time.Time) error
	RevokeAgent(ctx context.Context, tenantID, id string) error

	// Enrollment tokens
	CreateEnrollmentToken(ctx context.Context, t store.EnrollmentToken) error
	ConsumeEnrollmentToken(ctx context.Context, tokenHash string, now time.Time) (store.EnrollmentToken, error) // atomic single-use
	RevokeEnrollmentToken(ctx context.Context, tenantID, id string) error

	// Statistics
	TenantStats(ctx context.Context, tenantID string, staleBefore time.Time) (Stats, error)
	TenantIDs(ctx context.Context) ([]string, error) // system scope

	// Audit
	AppendAudit(ctx context.Context, row store.AuditRow) error
}
