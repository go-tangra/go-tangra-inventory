// Package repo defines the storage contract for the inventory service. The
// concrete implementations are repodb (TimescaleDB) and memstore (in-memory).
package repo

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Sentinel errors.
var (
	ErrNotFound = errors.New("not found")
	ErrConflict = errors.New("conflict")
	// ErrReplay reports a reused auto-enrollment nonce (feature 029).
	ErrReplay = errors.New("replay")
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
	// DeleteHost deletes the host; in the same transaction its active
	// certificate delivery items are cancelled (host_deleted, audited) and
	// its host certificates deleted (feature 033).
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

	// Paged lists (list contract, go-tangra specs/032-server-side-tables).
	// Each counts the rows matching the filter, clamps req to that total and
	// returns one page ordered by the req field of its Spec (store.HostList,
	// store.SnapshotList, store.ChangeList) with the id as tie-breaker,
	// together with the total and the request actually applied. The cursor
	// variants above keep serving gRPC, backup and internal callers.
	// ListSnapshotsPage returns payload-free summaries.
	ListHostsPage(ctx context.Context, tenantID string, f store.HostFilter, req listquery.Request) ([]store.Host, int, listquery.Request, error)
	ListSnapshotsPage(ctx context.Context, tenantID, hostID string, req listquery.Request) ([]store.Snapshot, int, listquery.Request, error)
	ListChangesPage(ctx context.Context, tenantID, hostID string, req listquery.Request) ([]store.Change, int, listquery.Request, error)

	// Agents
	CreateAgent(ctx context.Context, a store.Agent) error
	GetAgent(ctx context.Context, tenantID, id string) (store.Agent, error)
	GetAgentByID(ctx context.Context, id string) (store.Agent, error) // ingest auth: id -> tenant/host scope
	TouchAgent(ctx context.Context, id, version string, hostID string, at time.Time) error
	// RevokeAgent revokes the agent; in the same transaction its active
	// certificate delivery items are cancelled (agent_revoked, audited).
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

	// Host reports (feature 020). SetReportDigest records a new projection
	// digest and its change time only when the digest differs from the stored
	// one (changed reports whether it did). ListReportTenants is system scope:
	// tenants with a host whose report changed after since (zero = every
	// tenant with hosts), at most limit (<= 0: no limit), and the highest change
	// time among them (since when none). ListHostReportRows is tenant scope,
	// ordered by (report_changed_at, id).
	SetReportDigest(ctx context.Context, tenantID, hostID, digest string, changedAt time.Time) (changed bool, err error)
	ListReportTenants(ctx context.Context, since time.Time, limit int) (ids []string, maxChanged time.Time, err error)
	ListHostReportRows(ctx context.Context, tenantID string, f store.ReportRowFilter) ([]store.Host, error)

	ReleaseStore
	UpgradeStore
	AutoEnrollStore
	CertDeliveryStore
}

// ArtifactOpener opens the content of one artifact of a release being
// imported (the releases service wraps it in a verifying reader).
type ArtifactOpener func(a store.AgentArtifact) (io.ReadCloser, error)

// ReleaseStore holds the signed agent releases (feature 023). The tables are
// global (public binaries, not tenant data); only the startup seeder and the
// import CLI write them.
type ReleaseStore interface {
	// ImportAgentRelease stores a release, its artifacts and their chunks
	// in one transaction under a per-version advisory lock, so concurrent
	// replicas seeding the same bundle store it once. imported is false
	// when the same release (same manifest digest) is already complete;
	// a stored release with another manifest is ErrConflict. An error of
	// open or of a returned reader aborts the whole import.
	ImportAgentRelease(ctx context.Context, rel store.AgentRelease, open ArtifactOpener, chunkBytes int) (imported bool, err error)
	// ListAgentReleases returns every stored release with its artifacts.
	ListAgentReleases(ctx context.Context) ([]store.AgentRelease, error)
	// GetAgentRelease returns one release with manifest, signature and artifacts.
	GetAgentRelease(ctx context.Context, version string) (store.AgentRelease, error)
	// ReadArtifactChunk returns chunk seq of an artifact (ErrNotFound past the end).
	ReadArtifactChunk(ctx context.Context, a store.AgentArtifact, seq int) ([]byte, error)
	// DeleteAgentRelease removes a release and (cascade) its artifacts.
	DeleteAgentRelease(ctx context.Context, version string) error
	// ProtectedAgentVersions lists versions retention must keep: tenant
	// policy pins and targets of active upgrade requests (system scope).
	ProtectedAgentVersions(ctx context.Context) ([]string, error)
}

// UpgradeFilter constrains ListAgentUpgrades (newest first).
type UpgradeFilter struct {
	State    string
	AgentID  string
	Limit    int    // <= 0: no limit
	CursorID string // requests created before this one
}

// UpgradeStore holds agent platforms, upgrade requests and the per-tenant
// upgrade policy (feature 023). Every write that changes a request or a
// policy appends its audit rows in the same transaction (FR-016).
type UpgradeStore interface {
	// ListAgents lists a tenant's enrolled, non-revoked agents.
	ListAgents(ctx context.Context, tenantID string) ([]store.Agent, error)
	// SetAgentPlatform records the platform and capabilities an agent
	// reported on its command stream (system scope, like TouchAgent).
	SetAgentPlatform(ctx context.Context, agentID, os, arch, installType string, capabilities []string, at time.Time) error

	// CreateAgentUpgrade inserts a request and its audit row; ErrConflict
	// when the agent already has an active request.
	CreateAgentUpgrade(ctx context.Context, u store.AgentUpgrade, row store.AuditRow) error
	// UpdateAgentUpgrade locks the request, applies fn and stores the
	// result together with the audit rows fn returns; an error of fn
	// changes nothing. ErrNotFound for another tenant's or a missing id.
	UpdateAgentUpgrade(ctx context.Context, tenantID, id string, fn func(*store.AgentUpgrade) ([]store.AuditRow, error)) (store.AgentUpgrade, error)
	GetAgentUpgrade(ctx context.Context, tenantID, id string) (store.AgentUpgrade, error)
	ListAgentUpgrades(ctx context.Context, tenantID string, f UpgradeFilter) ([]store.AgentUpgrade, error)
	// LatestAgentUpgrades returns the newest request of every agent.
	LatestAgentUpgrades(ctx context.Context, tenantID string) (map[string]store.AgentUpgrade, error)
	// ListStaleUpgrades lists (system scope) pending/delivered requests past
	// their expiry and downloading/installing requests not updated since
	// progressBefore, at most limit.
	ListStaleUpgrades(ctx context.Context, now, progressBefore time.Time, limit int) ([]store.AgentUpgrade, error)

	// GetUpgradePolicy returns the tenant's policy (found false: defaults apply).
	GetUpgradePolicy(ctx context.Context, tenantID string) (store.AgentUpgradePolicy, bool, error)
	// UpdateUpgradePolicy upserts the policy through fn (starting from the
	// stored row or the defaults) together with fn's audit rows.
	UpdateUpgradePolicy(ctx context.Context, tenantID string, fn func(*store.AgentUpgradePolicy) ([]store.AuditRow, error)) (store.AgentUpgradePolicy, error)
	// ListEnabledUpgradePolicies lists every enabled policy (system scope).
	ListEnabledUpgradePolicies(ctx context.Context) ([]store.AgentUpgradePolicy, error)
	// TryTenantLock runs fn while holding a cluster-wide lock named key;
	// acquired is false (fn not run) when another holder has it.
	TryTenantLock(ctx context.Context, key string, fn func(context.Context) error) (acquired bool, err error)
}

// AutoEnrollStore persists automatic enrollment (feature 029): the tenant
// switch, the keys and accepted enrollments.
type AutoEnrollStore interface {
	// GetAutoEnrollSettings returns the tenant's switch (found false: off).
	GetAutoEnrollSettings(ctx context.Context, tenantID string) (store.AutoEnrollSettings, bool, error)
	PutAutoEnrollSettings(ctx context.Context, s store.AutoEnrollSettings, row store.AuditRow) error
	ListAutoEnrollKeys(ctx context.Context, tenantID string) ([]store.AutoEnrollKey, error)
	// CreateAutoEnrollKey returns ErrConflict for a duplicate name or key id.
	CreateAutoEnrollKey(ctx context.Context, k store.AutoEnrollKey, row store.AuditRow) error
	// UpdateAutoEnrollKey applies fn to the stored key and writes its audit row.
	UpdateAutoEnrollKey(ctx context.Context, tenantID, id string, fn func(*store.AutoEnrollKey) (store.AuditRow, error)) (store.AutoEnrollKey, error)
	DeleteAutoEnrollKey(ctx context.Context, tenantID, id string, row store.AuditRow) error
	// LookupAutoEnrollKey finds a key by its public id across tenants
	// (system scope) together with its tenant's switch.
	LookupAutoEnrollKey(ctx context.Context, keyID string) (store.AutoEnrollKey, bool, error)
	// EnrollWithAutoKey applies an accepted enrollment atomically: ErrReplay
	// when the nonce was used, ErrConflict when the key is no longer usable
	// or the tenant switch is off.
	EnrollWithAutoKey(ctx context.Context, e store.AutoEnrollment) error
}

// NewCertDelivery is a delivery with its items and audit rows, created in
// one transaction by CreateCertDelivery (feature 033).
type NewCertDelivery struct {
	Delivery store.CertDelivery
	Items    []store.CertDeliveryItem
	Audit    []store.AuditRow
	// SupersedeAudit builds the audit row of an older active item of the
	// same host and name that a new active item replaces (nil: none).
	SupersedeAudit func(old store.CertDeliveryItem, newItemID string) store.AuditRow
}

// CertItemChange is what an UpdateCertItem callback stores next to the
// item: its audit rows and, when set, the host certificate to upsert.
type CertItemChange struct {
	Audit    []store.AuditRow
	HostCert *store.HostCertificate
}

// CertItemFilter constrains ListCertItemsPage (contracts/inventory-http.md);
// empty fields match all.
type CertItemFilter struct {
	HostID        string
	State         string
	Name          string
	CertificateID string
	DeliveryID    string
}

// CertCancelScope selects the active items CancelCertItems cancels: those
// of a host, of an agent or of an lcm certificate (exactly one is set).
type CertCancelScope struct {
	HostID        string
	AgentID       string
	CertificateID string
}

// CertDeliveryStore persists certificate deliveries, their per-host items
// and the per-host certificate state (feature 033). No method stores
// certificate or key material. Every state change appends its audit rows in
// the same transaction.
type CertDeliveryStore interface {
	// CreateCertDelivery inserts the delivery and its items with the audit
	// rows atomically. Every active item of the same tenant, host and name
	// that a new active item replaces is set to superseded first (with
	// SupersedeAudit's row); those items are returned. ErrConflict when the
	// (tenant, source, idempotency key) exists or an id is reused.
	CreateCertDelivery(ctx context.Context, n NewCertDelivery) (superseded []store.CertDeliveryItem, err error)
	// GetCertDelivery returns a delivery with its items (oldest first).
	GetCertDelivery(ctx context.Context, tenantID, id string) (store.CertDelivery, []store.CertDeliveryItem, error)
	// GetCertDeliveryByKey finds a delivery by its idempotency key.
	GetCertDeliveryByKey(ctx context.Context, tenantID, source, key string) (store.CertDelivery, error)
	GetCertItem(ctx context.Context, tenantID, id string) (store.CertDeliveryItem, error)
	// UpdateCertItem locks the item, applies fn and stores the result with
	// fn's audit rows and host certificate; an error of fn changes nothing.
	// ErrNotFound for another tenant's or a missing id; ErrConflict when
	// the result would make a second active item for the host and name.
	UpdateCertItem(ctx context.Context, tenantID, id string, fn func(*store.CertDeliveryItem) (CertItemChange, error)) (store.CertDeliveryItem, error)
	// ListActiveCertItemsForAgent lists an agent's pending, delivered and
	// fetched items, oldest first, at most limit (<= 0: no limit).
	ListActiveCertItemsForAgent(ctx context.Context, tenantID, agentID string, limit int) ([]store.CertDeliveryItem, error)
	// ListCertItemsPage pages a tenant's items (store.CertItemList order).
	ListCertItemsPage(ctx context.Context, tenantID string, f CertItemFilter, req listquery.Request) ([]store.CertDeliveryItem, int, listquery.Request, error)
	// CancelCertItems cancels the scope's active items with reason, each with
	// the audit row of row, in one transaction, and returns them.
	CancelCertItems(ctx context.Context, tenantID string, scope CertCancelScope, reason string, row func(store.CertDeliveryItem) store.AuditRow) ([]store.CertDeliveryItem, error)
	// GetHostCertificate returns the current certificate of a host under name.
	GetHostCertificate(ctx context.Context, tenantID, hostID, name string) (store.HostCertificate, error)
	// ListHostCertificates lists a host's certificates by name.
	ListHostCertificates(ctx context.Context, tenantID, hostID string) ([]store.HostCertificate, error)
	// PurgeCertItems deletes (system scope) terminal items last updated
	// before olderThan and the deliveries left without items.
	PurgeCertItems(ctx context.Context, olderThan time.Time) (int64, error)

	// ExtendCertDelivery moves a delivery's expiry to at when at is later
	// (a re-arm opens a new delivery window). ErrNotFound for another
	// tenant's or a missing id.
	ExtendCertDelivery(ctx context.Context, tenantID, id string, at time.Time) error
	// ListStaleCertItems lists (system scope) the active items whose
	// delivery expired before now and the fetched items not updated since
	// reportBefore, oldest first, at most limit (<= 0: no limit).
	ListStaleCertItems(ctx context.Context, now, reportBefore time.Time, limit int) ([]StaleCertItem, error)
	// ListHostCertificatesByName lists the tenant's host certificates under
	// name (one per host).
	ListHostCertificatesByName(ctx context.Context, tenantID, name string) ([]store.HostCertificate, error)
	// ListActiveCertItemsByName lists the tenant's active items under name
	// (at most one per host).
	ListActiveCertItemsByName(ctx context.Context, tenantID, name string) ([]store.CertDeliveryItem, error)
	// RevokeHostCertificates sets revoked_at = at on the tenant's host
	// certificates holding certificateID that are not flagged yet, each with
	// the audit row of row, in one transaction, and returns them.
	RevokeHostCertificates(ctx context.Context, tenantID, certificateID string, at time.Time, row func(store.HostCertificate) store.AuditRow) ([]store.HostCertificate, error)
}

// StaleCertItem is an active delivery item with the expiry of its delivery
// (ListStaleCertItems).
type StaleCertItem struct {
	Item      store.CertDeliveryItem
	ExpiresAt time.Time
}
