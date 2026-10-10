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
	"github.com/go-tangra/go-tangra-inventory/v4/internal/moduledelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// ModuleDeliveryServer implements inventory.v1.ModuleDeliveryService
// (feature 037): the gateway asks for module deliveries by reference. Every
// RPC requires a SPIFFE peer whose service name is listed in Sources
// (module_delivery.sources), on top of the inbound mesh policy; the tenant
// is the uuid in the request. No response carries bundle material.
type ModuleDeliveryServer struct {
	invv1.UnimplementedModuleDeliveryServiceServer
	Svc     *moduledelivery.Service
	Sources []string
}

// source admits only a SPIFFE peer of a configured source service and
// returns its service name and SPIFFE id; a refused peer is audited in the
// request tenant when that is a uuid.
func (s *ModuleDeliveryServer) source(ctx context.Context, tenantID string) (string, string, error) {
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
		s.Svc.Refuse(ctx, strings.ToLower(tenantID), moduledelivery.Actor{Kind: audit.ActorService, ID: actor}, moduledelivery.RefusedSourceNotAllowed)
	}
	return "", "", status.Error(codes.PermissionDenied, "not a module delivery source")
}

// moduleError maps a relay error to a gRPC status with a stable message:
// FailedPrecondition carries exactly the reason token (disabled_on_server,
// no_agent, ...), which the gateway shows.
func moduleError(err error) error {
	var ie *moduledelivery.InvalidError
	var ne *moduledelivery.IneligibleError
	switch {
	case errors.As(err, &ie):
		return status.Error(codes.InvalidArgument, ie.Error())
	case errors.As(err, &ne):
		return status.Error(codes.FailedPrecondition, ne.Reason)
	case errors.Is(err, moduledelivery.ErrDisabled):
		return status.Error(codes.FailedPrecondition, moduledelivery.ErrDisabled.Error())
	case errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, "not_found")
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}

// CreateModuleDelivery creates (or replays) a delivery to one host.
func (s *ModuleDeliveryServer) CreateModuleDelivery(ctx context.Context, req *invv1.CreateModuleDeliveryRequest) (*invv1.ModuleDelivery, error) {
	name, spiffeID, err := s.source(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Svc.Create(ctx, moduledelivery.Request{TenantID: req.GetTenantId(), Source: name, RequestedBy: spiffeID,
		DeliveryID: req.GetDeliveryId(), HostID: req.GetHostId(), Module: req.GetModule(), Version: req.GetVersion(),
		ExpiresAt: time.Unix(req.GetExpiresAt(), 0).UTC()})
	if err != nil {
		return nil, moduleError(err)
	}
	return moduleToPB(v), nil
}

// GetModuleDelivery returns the caller's delivery by its delivery id.
func (s *ModuleDeliveryServer) GetModuleDelivery(ctx context.Context, req *invv1.GetModuleDeliveryRequest) (*invv1.ModuleDelivery, error) {
	name, _, err := s.source(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	v, err := s.Svc.Get(ctx, req.GetTenantId(), name, req.GetDeliveryId())
	if err != nil {
		return nil, moduleError(err)
	}
	return moduleToPB(v), nil
}

// ListModuleTargets lists the tenant's hosts with their module capability.
func (s *ModuleDeliveryServer) ListModuleTargets(ctx context.Context, req *invv1.ListModuleTargetsRequest) (*invv1.ListModuleTargetsResponse, error) {
	if _, _, err := s.source(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}
	hosts, truncated, err := s.Svc.ListTargets(ctx, strings.ToLower(req.GetTenantId()), req.GetQuery(), int(req.GetLimit()))
	if err != nil {
		return nil, moduleError(err)
	}
	out := &invv1.ListModuleTargetsResponse{Hosts: make([]*invv1.ModuleTarget, 0, len(hosts)), Truncated: truncated}
	for _, h := range hosts {
		out.Hosts = append(out.Hosts, &invv1.ModuleTarget{HostId: h.HostID, Hostname: h.Hostname, OsName: h.OSName,
			AgentOnline: h.AgentOnline, Capability: h.Capability, IpAddresses: h.IPAddresses})
	}
	return out, nil
}

// moduleStates maps item states to the proto enum.
var moduleStates = map[string]invv1.ModuleDeliveryState{
	store.DeliveryPending:     invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_PENDING,
	store.DeliveryDelivered:   invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_DELIVERED,
	store.DeliveryFetched:     invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_FETCHED,
	store.DeliveryInstalled:   invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_INSTALLED,
	store.DeliveryFailed:      invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_FAILED,
	store.DeliveryHookFailed:  invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_HOOK_FAILED,
	store.DeliveryUnsupported: invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_UNSUPPORTED,
	store.DeliverySuperseded:  invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_SUPERSEDED,
	store.DeliveryExpired:     invv1.ModuleDeliveryState_MODULE_DELIVERY_STATE_EXPIRED,
}

func moduleToPB(v moduledelivery.View) *invv1.ModuleDelivery {
	return &invv1.ModuleDelivery{ItemId: v.ID, DeliveryId: v.DeliveryID, TenantId: v.TenantID, HostId: v.HostID, Hostname: v.Hostname,
		Module: v.Module, Version: v.Version, State: moduleStates[v.State], Reason: v.Reason,
		HookExitCode: int32(v.HookExitCode), // #nosec G115 -- -1..256
		AgentOnline:  v.AgentOnline, CreatedAt: v.CreatedAt.Unix(), UpdatedAt: v.UpdatedAt.Unix(), ExpiresAt: v.ExpiresAt.Unix(),
		Created: v.Created}
}
