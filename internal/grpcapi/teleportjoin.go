package grpcapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/teleportjoin"
)

// TeleportJoinServer implements inventory.v1.TeleportJoinDeliveryService
// (feature 038): the teleport module asks for SSH-node enrollments of
// inventory hosts by reference. Every RPC requires a SPIFFE peer whose
// service name is listed in Sources (teleport_join.sources), on top of the
// inbound mesh policy; the tenant is the uuid in the request. No response
// carries join material.
type TeleportJoinServer struct {
	invv1.UnimplementedTeleportJoinDeliveryServiceServer
	Svc     *teleportjoin.Service
	Sources []string
}

// source admits only a SPIFFE peer of a configured source service and
// returns its service name and SPIFFE id; a refused peer is audited in the
// request tenant when that is a uuid.
func (s *TeleportJoinServer) source(ctx context.Context, tenantID string) (string, string, error) {
	id, ok := callerFunc(ctx)
	if !ok {
		return "", "", status.Error(codes.Unauthenticated, "service identity required")
	}
	name := serviceName(id)
	for _, src := range s.Sources {
		if name != "" && src == name {
			if !uuidRE.MatchString(tenantID) {
				return "", "", status.Error(codes.InvalidArgument, "tenant_id must be a uuid")
			}
			return name, id, nil
		}
	}
	if uuidRE.MatchString(tenantID) {
		actor := name
		if actor == "" {
			actor = "unknown"
		}
		s.Svc.Refuse(ctx, strings.ToLower(tenantID), teleportjoin.Actor{Kind: audit.ActorService, ID: actor}, teleportjoin.RefusedSourceNotAllowed)
	}
	return "", "", status.Error(codes.PermissionDenied, "not a teleport join source")
}

// teleportError maps a relay error to a gRPC status with a stable message:
// FailedPrecondition carries exactly the reason token (disabled_on_server,
// no_agent, ...), which the teleport module shows.
func teleportError(err error) error {
	var ie *teleportjoin.InvalidError
	var ne *teleportjoin.IneligibleError
	switch {
	case errors.As(err, &ie):
		return status.Error(codes.InvalidArgument, ie.Error())
	case errors.As(err, &ne):
		return status.Error(codes.FailedPrecondition, ne.Reason)
	case errors.Is(err, teleportjoin.ErrDisabled):
		return status.Error(codes.FailedPrecondition, teleportjoin.ErrDisabled.Error())
	case errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, "not_found")
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}

// CreateTeleportJoin creates (or replays) the join item of one host.
func (s *TeleportJoinServer) CreateTeleportJoin(ctx context.Context, req *invv1.CreateTeleportJoinRequest) (*invv1.TeleportJoin, error) {
	name, spiffeID, err := s.source(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Svc.Create(ctx, teleportjoin.Request{TenantID: req.GetTenantId(), Source: name, RequestedBy: spiffeID,
		EnrollmentID: req.GetEnrollmentId(), HostID: req.GetHostId(), ClusterID: req.GetClusterId(),
		ExpiresAt: time.Unix(req.GetExpiresAt(), 0).UTC()})
	if err != nil {
		return nil, teleportError(err)
	}
	return teleportToPB(v), nil
}

// GetTeleportJoins returns the caller's items by enrollment id (unknown
// ids omitted).
func (s *TeleportJoinServer) GetTeleportJoins(ctx context.Context, req *invv1.GetTeleportJoinsRequest) (*invv1.GetTeleportJoinsResponse, error) {
	name, _, err := s.source(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	vs, err := s.Svc.GetMany(ctx, req.GetTenantId(), name, req.GetEnrollmentIds())
	if err != nil {
		return nil, teleportError(err)
	}
	out := &invv1.GetTeleportJoinsResponse{Items: make([]*invv1.TeleportJoin, 0, len(vs))}
	for _, v := range vs {
		out.Items = append(out.Items, teleportToPB(v))
	}
	return out, nil
}

// CancelTeleportJoin cancels the caller's active item (a terminal one is
// returned unchanged).
func (s *TeleportJoinServer) CancelTeleportJoin(ctx context.Context, req *invv1.CancelTeleportJoinRequest) (*invv1.TeleportJoin, error) {
	name, spiffeID, err := s.source(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Svc.Cancel(ctx, req.GetTenantId(), name, spiffeID, req.GetEnrollmentId())
	if err != nil {
		return nil, teleportError(err)
	}
	return teleportToPB(v), nil
}

// ListTeleportJoinTargets lists the tenant's hosts with the facts the
// module turns into labels and their join capability.
func (s *TeleportJoinServer) ListTeleportJoinTargets(ctx context.Context, req *invv1.ListTeleportJoinTargetsRequest) (*invv1.ListTeleportJoinTargetsResponse, error) {
	if _, _, err := s.source(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}
	hosts, truncated, err := s.Svc.ListTargets(ctx, strings.ToLower(req.GetTenantId()),
		teleportjoin.TargetQuery{Query: req.GetQuery(), Limit: int(req.GetLimit()), HostIDs: req.GetHostIds()})
	if err != nil {
		return nil, teleportError(err)
	}
	out := &invv1.ListTeleportJoinTargetsResponse{Hosts: make([]*invv1.TeleportJoinTarget, 0, len(hosts)), Truncated: truncated}
	for _, h := range hosts {
		out.Hosts = append(out.Hosts, &invv1.TeleportJoinTarget{HostId: h.HostID, Hostname: h.Hostname, OsName: h.OSName,
			OsVersion: h.OSVersion, OsArch: h.OSArch, Manufacturer: h.Manufacturer, Model: h.Model, Tags: h.Tags,
			AgentOnline: h.AgentOnline, Capability: h.Capability})
	}
	return out, nil
}

// teleportStates maps item states to the proto enum.
var teleportStates = map[string]invv1.TeleportJoinState{
	store.DeliveryPending:     invv1.TeleportJoinState_TELEPORT_JOIN_STATE_PENDING,
	store.DeliveryDelivered:   invv1.TeleportJoinState_TELEPORT_JOIN_STATE_DELIVERED,
	store.DeliveryFetched:     invv1.TeleportJoinState_TELEPORT_JOIN_STATE_FETCHED,
	store.TeleportInstalling:  invv1.TeleportJoinState_TELEPORT_JOIN_STATE_INSTALLING,
	store.TeleportJoined:      invv1.TeleportJoinState_TELEPORT_JOIN_STATE_JOINED,
	store.DeliveryFailed:      invv1.TeleportJoinState_TELEPORT_JOIN_STATE_FAILED,
	store.DeliveryUnsupported: invv1.TeleportJoinState_TELEPORT_JOIN_STATE_UNSUPPORTED,
	store.DeliverySuperseded:  invv1.TeleportJoinState_TELEPORT_JOIN_STATE_SUPERSEDED,
	store.DeliveryExpired:     invv1.TeleportJoinState_TELEPORT_JOIN_STATE_EXPIRED,
	store.DeliveryCancelled:   invv1.TeleportJoinState_TELEPORT_JOIN_STATE_CANCELLED,
}

func teleportToPB(v teleportjoin.View) *invv1.TeleportJoin {
	return &invv1.TeleportJoin{ItemId: v.ID, EnrollmentId: v.EnrollmentID, TenantId: v.TenantID, HostId: v.HostID, Hostname: v.Hostname,
		ClusterId: v.ClusterID, State: teleportStates[v.State], Reason: v.Reason, NodeHostUuid: v.NodeHostUUID,
		AgentOnline: v.AgentOnline, CreatedAt: v.CreatedAt.Unix(), UpdatedAt: v.UpdatedAt.Unix(), ExpiresAt: v.ExpiresAt.Unix(),
		Created: v.Created}
}
