package grpcapi

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stats"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// ---- InventoryHostService

// HostServer implements inventory.v1.InventoryHostService.
type HostServer struct {
	invv1.UnimplementedInventoryHostServiceServer
	Hosts *hosts.Service
}

func (s *HostServer) ListHosts(ctx context.Context, req *invv1.ListHostsRequest) (*invv1.ListHostsResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	f := store.HostFilter{
		Hostname: req.GetHostname(), OSName: req.GetOsName(), Manufacturer: req.GetManufacturer(),
		Status: hostStatusFromPB(req.GetStatus()), Tag: req.GetTag(),
		Limit: int(req.GetLimit()), CursorID: req.GetCursorId(),
	}
	if v := req.GetLastSeenFrom(); v > 0 {
		t := time.Unix(v, 0).UTC()
		f.LastSeenFrom = &t
	}
	if v := req.GetLastSeenTo(); v > 0 {
		t := time.Unix(v, 0).UTC()
		f.LastSeenTo = &t
	}
	items, err := s.Hosts.List(ctx, subj, f)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &invv1.ListHostsResponse{Hosts: make([]*invv1.Host, 0, len(items))}
	for _, v := range items {
		out.Hosts = append(out.Hosts, hostToPB(v))
	}
	return out, nil
}

func (s *HostServer) GetHost(ctx context.Context, req *invv1.GetHostRequest) (*invv1.Host, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Hosts.Get(ctx, subj, req.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	return hostToPB(v), nil
}

// GetHostByIdentity resolves a host from a reported identity. The host service
// exposes no identity lookup, so the tenant's hosts are listed and matched by
// identity precedence: hardware_uuid, then machine_id, then hostname.
func (s *HostServer) GetHostByIdentity(ctx context.Context, req *invv1.GetHostByIdentityRequest) (*invv1.Host, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	id := req.GetIdentity()
	if id == nil {
		return nil, status.Error(codes.InvalidArgument, "identity required")
	}
	items, err := s.Hosts.List(ctx, subj, store.HostFilter{})
	if err != nil {
		return nil, grpcError(err)
	}
	if hw := id.GetHardwareUuid(); hw != "" {
		for _, v := range items {
			if v.HardwareUUID == hw {
				return hostToPB(v), nil
			}
		}
	}
	if mid := id.GetMachineId(); mid != "" {
		for _, v := range items {
			if v.MachineID == mid {
				return hostToPB(v), nil
			}
		}
	}
	if hn := id.GetHostname(); hn != "" {
		for _, v := range items {
			if v.Hostname == hn {
				return hostToPB(v), nil
			}
		}
	}
	return nil, status.Error(codes.NotFound, "not_found")
}

func (s *HostServer) TagHost(ctx context.Context, req *invv1.TagHostRequest) (*invv1.Host, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Hosts.SetTags(ctx, subj, req.GetId(), req.GetTags())
	if err != nil {
		return nil, grpcError(err)
	}
	return hostToPB(v), nil
}

func (s *HostServer) RetireHost(ctx context.Context, req *invv1.RetireHostRequest) (*invv1.Host, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Hosts.Retire(ctx, subj, req.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	return hostToPB(v), nil
}

func (s *HostServer) DeleteHost(ctx context.Context, req *invv1.DeleteHostRequest) (*invv1.DeleteHostResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if err := s.Hosts.Delete(ctx, subj, req.GetId()); err != nil {
		return nil, grpcError(err)
	}
	return &invv1.DeleteHostResponse{}, nil
}

// ---- InventorySnapshotService

// SnapshotServer implements inventory.v1.InventorySnapshotService.
type SnapshotServer struct {
	invv1.UnimplementedInventorySnapshotServiceServer
	Snapshots *snapshots.Service
}

func (s *SnapshotServer) GetSnapshot(ctx context.Context, req *invv1.GetSnapshotRequest) (*invv1.Snapshot, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Snapshots.Get(ctx, subj, req.GetId())
	if err != nil {
		return nil, grpcError(err)
	}
	return snapshotToPB(v), nil
}

func (s *SnapshotServer) ListSnapshots(ctx context.Context, req *invv1.ListSnapshotsRequest) (*invv1.ListSnapshotsResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	items, err := s.Snapshots.ListForHost(ctx, subj, req.GetHostId(), int(req.GetLimit()), req.GetCursorId())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &invv1.ListSnapshotsResponse{Snapshots: make([]*invv1.SnapshotSummary, 0, len(items))}
	for _, v := range items {
		out.Snapshots = append(out.Snapshots, snapshotSummaryToPB(v))
	}
	return out, nil
}

func (s *SnapshotServer) GetLatestByHost(ctx context.Context, req *invv1.GetLatestByHostRequest) (*invv1.Snapshot, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Snapshots.GetLatestForHost(ctx, subj, req.GetHostId())
	if err != nil {
		return nil, grpcError(err)
	}
	return snapshotToPB(v), nil
}

func (s *SnapshotServer) DiffSnapshots(ctx context.Context, req *invv1.DiffSnapshotsRequest) (*invv1.DiffSnapshotsResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	changes, err := s.Snapshots.Diff(ctx, subj, req.GetId(), req.GetOther())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &invv1.DiffSnapshotsResponse{Changes: make([]*invv1.Change, 0, len(changes))}
	for _, c := range changes {
		out.Changes = append(out.Changes, changeToPB(c))
	}
	return out, nil
}

func (s *SnapshotServer) ListChanges(ctx context.Context, req *invv1.ListChangesRequest) (*invv1.ListChangesResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	changes, err := s.Snapshots.ListChanges(ctx, subj, req.GetHostId())
	if err != nil {
		return nil, grpcError(err)
	}
	out := &invv1.ListChangesResponse{Changes: make([]*invv1.Change, 0, len(changes))}
	for _, c := range changes {
		out.Changes = append(out.Changes, changeToPB(c))
	}
	return out, nil
}

func (s *SnapshotServer) DeleteSnapshot(ctx context.Context, req *invv1.DeleteSnapshotRequest) (*invv1.DeleteSnapshotResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if err := s.Snapshots.Delete(ctx, subj, req.GetId()); err != nil {
		return nil, grpcError(err)
	}
	return &invv1.DeleteSnapshotResponse{}, nil
}

// ---- InventoryStatisticsService

// StatisticsServer implements inventory.v1.InventoryStatisticsService.
type StatisticsServer struct {
	invv1.UnimplementedInventoryStatisticsServiceServer
	Stats *stats.Service
}

func (s *StatisticsServer) GetStatistics(ctx context.Context, req *invv1.GetStatisticsRequest) (*invv1.Stats, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Stats.Tenant(ctx, subj)
	if err != nil {
		return nil, grpcError(err)
	}
	return statsToPB(v), nil
}

// ---- InventoryAgentService

// AgentServer implements inventory.v1.InventoryAgentService.
type AgentServer struct {
	invv1.UnimplementedInventoryAgentServiceServer
	Enroll   *enroll.Service
	Registry registry.Registry
}

func (s *AgentServer) ListConnectedAgents(ctx context.Context, req *invv1.ListConnectedAgentsRequest) (*invv1.ListConnectedAgentsResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if s.Registry == nil {
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	agents, err := s.Registry.ListConnected(ctx, subj.TenantID)
	if err != nil {
		return nil, grpcError(err)
	}
	out := &invv1.ListConnectedAgentsResponse{Agents: make([]*invv1.ConnectedAgent, 0, len(agents))}
	for _, a := range agents {
		out.Agents = append(out.Agents, connectedAgentToPB(a))
	}
	return out, nil
}

func (s *AgentServer) RefreshInventory(ctx context.Context, req *invv1.RefreshInventoryRequest) (*invv1.RefreshInventoryResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if s.Registry == nil {
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	agents, err := s.Registry.ListConnected(ctx, subj.TenantID)
	if err != nil {
		return nil, grpcError(err)
	}
	cmd := registry.Command{ID: store.NewID(), Type: "refresh"}
	delivered := false
	for _, a := range agents {
		if a.HostID == req.GetHostId() {
			ok, derr := s.Registry.Deliver(ctx, a.AgentID, cmd)
			if derr != nil {
				return nil, grpcError(derr)
			}
			if ok {
				delivered = true
				break
			}
		}
	}
	return &invv1.RefreshInventoryResponse{Delivered: delivered, CommandId: cmd.ID}, nil
}

func (s *AgentServer) MintEnrollmentToken(ctx context.Context, req *invv1.MintEnrollmentTokenRequest) (*invv1.MintEnrollmentTokenResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if s.Enroll == nil {
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	ttl := time.Duration(req.GetTtlSeconds()) * time.Second
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	secret, tok, err := s.Enroll.MintToken(ctx, subj.TenantID, subj.UserID, req.GetLabel(), ttl)
	if err != nil {
		return nil, grpcError(err)
	}
	// The token secret is returned exactly once here; it is never persisted.
	return &invv1.MintEnrollmentTokenResponse{
		Id: tok.ID, Token: secret, ExpiresAt: unix(tok.ExpiresAt), Label: tok.Label,
	}, nil
}

func (s *AgentServer) RevokeAgent(ctx context.Context, req *invv1.RevokeAgentRequest) (*invv1.RevokeAgentResponse, error) {
	subj, err := caller(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	if s.Enroll == nil {
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	if err := s.Enroll.RevokeAgent(ctx, subj.TenantID, req.GetId()); err != nil {
		return nil, grpcError(err)
	}
	return &invv1.RevokeAgentResponse{}, nil
}
