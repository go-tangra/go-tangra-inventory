// Package inventoryclient is a thin, typed Go client for the inventory.v1
// service-to-service gRPC API, for other Freya modules that need to read managed
// hosts, their inventory snapshots and change history, per-tenant statistics, or
// to control agents (refresh, enrollment). It wraps the generated gRPC stubs so
// callers deal in ordinary Go values, not protobuf messages. The caller supplies
// a connected, SPIFFE-mTLS *grpc.ClientConn (e.g. from freya.App.Client(ctx,
// "inventory")); this package does not dial or manage the connection. Agent
// credentials and sealed fields are never returned; the only secret is the
// one-time token from MintEnrollmentToken.
package inventoryclient

import (
	"context"
	"time"

	"google.golang.org/grpc"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// Client calls the inventory.v1 API over a caller-provided gRPC connection.
type Client struct {
	hosts     invv1.InventoryHostServiceClient
	snapshots invv1.InventorySnapshotServiceClient
	stats     invv1.InventoryStatisticsServiceClient
	agents    invv1.InventoryAgentServiceClient
	reports   invv1.HostReportServiceClient
}

// New builds a client from a connected (SPIFFE-mTLS) gRPC connection to the
// inventory service.
func New(conn grpc.ClientConnInterface) *Client {
	return &Client{
		hosts:     invv1.NewInventoryHostServiceClient(conn),
		snapshots: invv1.NewInventorySnapshotServiceClient(conn),
		stats:     invv1.NewInventoryStatisticsServiceClient(conn),
		agents:    invv1.NewInventoryAgentServiceClient(conn),
		reports:   invv1.NewHostReportServiceClient(conn),
	}
}

// ---- plain Go values

// Host is a managed endpoint's metadata. It never carries agent credentials.
type Host struct {
	ID             string
	TenantID       string
	Hostname       string
	MachineID      string
	HardwareUUID   string
	SystemSerial   string
	IdentityKey    string
	Manufacturer   string
	Model          string
	OSName         string
	OSVersion      string
	OSArch         string
	AgentVersion   string
	AssignedUser   string
	Status         string
	Tags           map[string]string
	FirstSeen      time.Time
	LastSeen       time.Time
	LastSnapshotID string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// HostFilter constrains ListHosts. Empty fields match all.
type HostFilter struct {
	Hostname     string
	OSName       string
	Manufacturer string
	Status       string
	Tag          string
	Limit        int
	CursorID     string
}

// Change is one detected inventory change.
type Change struct {
	ID             string
	TenantID       string
	HostID         string
	SnapshotID     string
	PrevSnapshotID string
	DetectedAt     time.Time
	Category       string
	ChangeType     string
	ComponentKey   string
	Before         string
	After          string
}

// Snapshot is a snapshot's summary with its full inventory payload.
type Snapshot struct {
	ID           string
	TenantID     string
	HostID       string
	CollectedAt  time.Time
	ReceivedAt   time.Time
	AgentVersion string
	Source       string
	OSName       string
	OSVersion    string
	Manufacturer string
	Model        string
	Payload      *Inventory
}

// Statistics is a tenant's inventory statistics rollup.
type Statistics struct {
	HostsTotal          int64
	HostsByStatus       map[string]int64
	HostsByOS           map[string]int64
	HostsByManufacturer map[string]int64
	AgentsOnline        int64
	AgentsOffline       int64
	StaleHosts          int64
	SnapshotsTotal      int64
	TotalMemoryBytes    uint64
	TotalCPUCores       int64
	TotalDiskBytes      uint64
	TopPrograms         map[string]int64
	OSVersions          map[string]int64
}

// EnrollmentToken is a minted enrollment token. Token is the one-time secret.
type EnrollmentToken struct {
	ID        string
	Token     string
	ExpiresAt time.Time
	Label     string
}

// ---- host methods

// ListHosts lists a tenant's hosts matching f (empty filters match all).
func (c *Client) ListHosts(ctx context.Context, tenantID string, f HostFilter) ([]Host, error) {
	resp, err := c.hosts.ListHosts(ctx, &invv1.ListHostsRequest{
		TenantId: tenantID, Hostname: f.Hostname, OsName: f.OSName, Manufacturer: f.Manufacturer,
		Status: hostStatusEnum(f.Status), Tag: f.Tag, Limit: int64(f.Limit), CursorId: f.CursorID,
	})
	if err != nil {
		return nil, err
	}
	out := make([]Host, 0, len(resp.GetHosts()))
	for _, h := range resp.GetHosts() {
		out = append(out, toHost(h))
	}
	return out, nil
}

// GetHost returns one host by id.
func (c *Client) GetHost(ctx context.Context, tenantID, id string) (Host, error) {
	h, err := c.hosts.GetHost(ctx, &invv1.GetHostRequest{TenantId: tenantID, Id: id})
	if err != nil {
		return Host{}, err
	}
	return toHost(h), nil
}

// ---- snapshot methods

// GetLatestSnapshot returns a host's most recent snapshot with its full payload.
func (c *Client) GetLatestSnapshot(ctx context.Context, tenantID, hostID string) (Snapshot, error) {
	s, err := c.snapshots.GetLatestByHost(ctx, &invv1.GetLatestByHostRequest{TenantId: tenantID, HostId: hostID})
	if err != nil {
		return Snapshot{}, err
	}
	return toSnapshot(s), nil
}

// DiffSnapshots returns the component changes from snapshot id to other.
func (c *Client) DiffSnapshots(ctx context.Context, tenantID, id, other string) ([]Change, error) {
	resp, err := c.snapshots.DiffSnapshots(ctx, &invv1.DiffSnapshotsRequest{TenantId: tenantID, Id: id, Other: other})
	if err != nil {
		return nil, err
	}
	out := make([]Change, 0, len(resp.GetChanges()))
	for _, ch := range resp.GetChanges() {
		out = append(out, toChange(ch))
	}
	return out, nil
}

// ---- statistics

// GetStatistics returns a tenant's inventory statistics.
func (c *Client) GetStatistics(ctx context.Context, tenantID string) (Statistics, error) {
	pb, err := c.stats.GetStatistics(ctx, &invv1.GetStatisticsRequest{TenantId: tenantID})
	if err != nil {
		return Statistics{}, err
	}
	return Statistics{
		HostsTotal: pb.GetHostsTotal(), HostsByStatus: pb.GetHostsByStatus(), HostsByOS: pb.GetHostsByOs(),
		HostsByManufacturer: pb.GetHostsByManufacturer(), AgentsOnline: pb.GetAgentsOnline(),
		AgentsOffline: pb.GetAgentsOffline(), StaleHosts: pb.GetStaleHosts(), SnapshotsTotal: pb.GetSnapshotsTotal(),
		TotalMemoryBytes: pb.GetTotalMemoryBytes(), TotalCPUCores: pb.GetTotalCpuCores(),
		TotalDiskBytes: pb.GetTotalDiskBytes(), TopPrograms: pb.GetTopPrograms(), OSVersions: pb.GetOsVersions(),
	}, nil
}

// ---- agent control

// RefreshInventory asks the host's connected agent to re-collect and re-report.
// delivered is false when no agent for the host is currently connected.
func (c *Client) RefreshInventory(ctx context.Context, tenantID, hostID string) (delivered bool, commandID string, err error) {
	resp, err := c.agents.RefreshInventory(ctx, &invv1.RefreshInventoryRequest{TenantId: tenantID, HostId: hostID})
	if err != nil {
		return false, "", err
	}
	return resp.GetDelivered(), resp.GetCommandId(), nil
}

// MintEnrollmentToken mints a single-use enrollment token; the secret is
// returned exactly once. ttl <= 0 uses the server default.
func (c *Client) MintEnrollmentToken(ctx context.Context, tenantID, label string, ttl time.Duration) (EnrollmentToken, error) {
	resp, err := c.agents.MintEnrollmentToken(ctx, &invv1.MintEnrollmentTokenRequest{
		TenantId: tenantID, Label: label, TtlSeconds: int64(ttl.Seconds()),
	})
	if err != nil {
		return EnrollmentToken{}, err
	}
	tok := EnrollmentToken{ID: resp.GetId(), Token: resp.GetToken(), Label: resp.GetLabel()}
	if ts := resp.GetExpiresAt(); ts != 0 {
		tok.ExpiresAt = time.Unix(ts, 0).UTC()
	}
	return tok, nil
}

// ---- mappers

func toHost(h *invv1.Host) Host {
	out := Host{
		ID: h.GetId(), TenantID: h.GetTenantId(), Hostname: h.GetHostname(), MachineID: h.GetMachineId(),
		HardwareUUID: h.GetHardwareUuid(), SystemSerial: h.GetSystemSerial(), IdentityKey: h.GetIdentityKey(),
		Manufacturer: h.GetManufacturer(), Model: h.GetModel(), OSName: h.GetOsName(), OSVersion: h.GetOsVersion(),
		OSArch: h.GetOsArch(), AgentVersion: h.GetAgentVersion(), AssignedUser: h.GetAssignedUser(),
		Status: hostStatusString(h.GetStatus()), Tags: h.GetTags(), LastSnapshotID: h.GetLastSnapshotId(),
	}
	out.FirstSeen = unixTime(h.GetFirstSeen())
	out.LastSeen = unixTime(h.GetLastSeen())
	out.CreatedAt = unixTime(h.GetCreatedAt())
	out.UpdatedAt = unixTime(h.GetUpdatedAt())
	return out
}

func toSnapshot(s *invv1.Snapshot) Snapshot {
	out := Snapshot{}
	if sum := s.GetSummary(); sum != nil {
		out = Snapshot{
			ID: sum.GetId(), TenantID: sum.GetTenantId(), HostID: sum.GetHostId(),
			CollectedAt: unixTime(sum.GetCollectedAt()), ReceivedAt: unixTime(sum.GetReceivedAt()),
			AgentVersion: sum.GetAgentVersion(), Source: snapshotSourceString(sum.GetSource()),
			OSName: sum.GetOsName(), OSVersion: sum.GetOsVersion(), Manufacturer: sum.GetManufacturer(), Model: sum.GetModel(),
		}
	}
	if p := s.GetPayload(); p != nil {
		out.Payload = toInventory(p)
	}
	return out
}

func toChange(c *invv1.Change) Change {
	return Change{
		ID: c.GetId(), TenantID: c.GetTenantId(), HostID: c.GetHostId(), SnapshotID: c.GetSnapshotId(),
		PrevSnapshotID: c.GetPrevSnapshotId(), DetectedAt: unixTime(c.GetDetectedAt()), Category: c.GetCategory(),
		ChangeType: changeTypeString(c.GetChangeType()), ComponentKey: c.GetComponentKey(),
		Before: c.GetBefore(), After: c.GetAfter(),
	}
}

func unixTime(ts int64) time.Time {
	if ts == 0 {
		return time.Time{}
	}
	return time.Unix(ts, 0).UTC()
}

// ---- enum <-> string

func hostStatusEnum(s string) invv1.HostStatus {
	switch s {
	case "active":
		return invv1.HostStatus_HOST_STATUS_ACTIVE
	case "stale":
		return invv1.HostStatus_HOST_STATUS_STALE
	case "retired":
		return invv1.HostStatus_HOST_STATUS_RETIRED
	}
	return invv1.HostStatus_HOST_STATUS_UNSPECIFIED
}

func hostStatusString(s invv1.HostStatus) string {
	switch s {
	case invv1.HostStatus_HOST_STATUS_ACTIVE:
		return "active"
	case invv1.HostStatus_HOST_STATUS_STALE:
		return "stale"
	case invv1.HostStatus_HOST_STATUS_RETIRED:
		return "retired"
	}
	return ""
}

func snapshotSourceString(s invv1.SnapshotSource) string {
	switch s {
	case invv1.SnapshotSource_SNAPSHOT_SOURCE_AGENT:
		return "agent"
	case invv1.SnapshotSource_SNAPSHOT_SOURCE_MANUAL:
		return "manual"
	case invv1.SnapshotSource_SNAPSHOT_SOURCE_IMPORT:
		return "import"
	}
	return ""
}

func changeTypeString(t invv1.ChangeType) string {
	switch t {
	case invv1.ChangeType_CHANGE_TYPE_ADDED:
		return "added"
	case invv1.ChangeType_CHANGE_TYPE_REMOVED:
		return "removed"
	case invv1.ChangeType_CHANGE_TYPE_MODIFIED:
		return "modified"
	}
	return ""
}
