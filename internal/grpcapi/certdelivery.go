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
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certdelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// CertDeliveryServer implements inventory.v1.CertificateDeliveryService
// (feature 033): the deployer asks for deliveries by reference. Every RPC
// requires a SPIFFE peer whose service name is listed in Sources
// (cert_delivery.sources), on top of the inbound mesh policy; the tenant is
// the uuid in the request. No response carries certificate or key
// material.
type CertDeliveryServer struct {
	invv1.UnimplementedCertificateDeliveryServiceServer
	Svc     *certdelivery.Service
	Sources []string
}

// source admits only a SPIFFE peer of a configured source service and
// returns it; a refused peer is audited in the request tenant when that is
// a uuid.
func (s *CertDeliveryServer) source(ctx context.Context, tenantID string) (certdelivery.Actor, string, error) {
	id, ok := callerFunc(ctx)
	if !ok {
		return certdelivery.Actor{}, "", status.Error(codes.Unauthenticated, "service identity required")
	}
	name := serviceName(id)
	for _, src := range s.Sources {
		if name != "" && src == name {
			if !uuidRE.MatchString(tenantID) {
				return certdelivery.Actor{}, "", status.Error(codes.InvalidArgument, "tenant_id must be a uuid")
			}
			return certdelivery.Actor{Kind: audit.ActorService, ID: name}, id, nil
		}
	}
	if uuidRE.MatchString(tenantID) {
		actor := name
		if actor == "" {
			actor = "unknown"
		}
		s.Svc.Refuse(ctx, strings.ToLower(tenantID), certdelivery.Actor{Kind: audit.ActorService, ID: actor}, certdelivery.RefusedSourceNotAllowed)
	}
	return certdelivery.Actor{}, "", status.Error(codes.PermissionDenied, "not a certificate delivery source")
}

// certError maps a relay error to a gRPC status with a stable message.
func certError(err error) error {
	var ie *certdelivery.InvalidError
	switch {
	case errors.As(err, &ie):
		return status.Error(codes.InvalidArgument, ie.Error())
	case errors.Is(err, certdelivery.ErrDisabled):
		return status.Error(codes.FailedPrecondition, certdelivery.ErrDisabled.Error())
	case errors.Is(err, certdelivery.ErrCertificateNotFound), errors.Is(err, certdelivery.ErrCertificateRevoked),
		errors.Is(err, certdelivery.ErrCertificateExpired):
		return status.Error(codes.FailedPrecondition, err.Error())
	case errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, "not_found")
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}

func selectorOf(sel *invv1.HostSelector) ([]string, []string) {
	return sel.GetHostIds(), sel.GetHostTags()
}

// CreateCertificateDelivery creates (or replays) a delivery.
func (s *CertDeliveryServer) CreateCertificateDelivery(ctx context.Context, req *invv1.CreateCertificateDeliveryRequest) (*invv1.CertificateDelivery, error) {
	actor, spiffeID, err := s.source(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	ids, tags := selectorOf(req.GetSelector())
	v, err := s.Svc.Create(ctx, certdelivery.Request{
		TenantID: strings.ToLower(req.GetTenantId()), Source: actor.ID, RequestedBy: spiffeID, IdempotencyKey: req.GetIdempotencyKey(),
		ConfigurationID: req.GetConfigurationId(), TargetID: req.GetTargetId(), Trigger: req.GetTrigger(), CertificateID: req.GetCertificateId(),
		Name: req.GetName(), KeyPolicy: req.GetKeyPolicy(), HostIDs: ids, HostTags: tags, RearmFailed: req.GetRearmFailed(),
	})
	if err != nil {
		return nil, certError(err)
	}
	return deliveryToPB(v), nil
}

// GetCertificateDelivery returns a delivery of the request tenant.
func (s *CertDeliveryServer) GetCertificateDelivery(ctx context.Context, req *invv1.GetCertificateDeliveryRequest) (*invv1.CertificateDelivery, error) {
	if _, _, err := s.source(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}
	v, err := s.Svc.Get(ctx, strings.ToLower(req.GetTenantId()), req.GetId())
	if err != nil {
		return nil, certError(err)
	}
	return deliveryToPB(v), nil
}

// PreviewCertificateTargets resolves a selector without writing.
func (s *CertDeliveryServer) PreviewCertificateTargets(ctx context.Context, req *invv1.PreviewCertificateTargetsRequest) (*invv1.PreviewCertificateTargetsResponse, error) {
	if _, _, err := s.source(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}
	ids, tags := selectorOf(req.GetSelector())
	p, err := s.Svc.Preview(ctx, strings.ToLower(req.GetTenantId()), ids, tags)
	if err != nil {
		return nil, certError(err)
	}
	out := &invv1.PreviewCertificateTargetsResponse{Hosts: make([]*invv1.TargetHost, 0, len(p.Hosts)), UnknownHostIds: p.UnknownHostIDs, Truncated: p.Truncated}
	for _, h := range p.Hosts {
		out.Hosts = append(out.Hosts, &invv1.TargetHost{HostId: h.HostID, Hostname: h.Hostname, OsName: h.OSName, Tags: h.Tags,
			AgentOnline: h.AgentOnline, Capability: h.Capability})
	}
	return out, nil
}

// VerifyHostCertificates compares reported fingerprints with the expected.
func (s *CertDeliveryServer) VerifyHostCertificates(ctx context.Context, req *invv1.VerifyHostCertificatesRequest) (*invv1.VerifyHostCertificatesResponse, error) {
	if _, _, err := s.source(ctx, req.GetTenantId()); err != nil {
		return nil, err
	}
	ids, tags := selectorOf(req.GetSelector())
	hosts, matched, err := s.Svc.Verify(ctx, strings.ToLower(req.GetTenantId()), ids, tags, req.GetName(), req.GetExpectedFingerprintSha256())
	if err != nil {
		return nil, certError(err)
	}
	out := &invv1.VerifyHostCertificatesResponse{Hosts: make([]*invv1.HostCertificateStatus, 0, len(hosts)), Matched: int32(matched), // #nosec G115 -- <= 1000
		Total: int32(len(hosts))} // #nosec G115 -- <= 1000
	for _, h := range hosts {
		out.Hosts = append(out.Hosts, &invv1.HostCertificateStatus{HostId: h.HostID, Hostname: h.Hostname, Status: h.Status,
			FingerprintSha256: h.Fingerprint, Serial: h.Serial, Reason: h.Reason, LastDeliveredAt: unixOf(h.LastDeliveredAt)})
	}
	return out, nil
}

// MarkCertificateRevoked cancels queued deliveries of a revoked certificate
// and flags the hosts holding it.
func (s *CertDeliveryServer) MarkCertificateRevoked(ctx context.Context, req *invv1.MarkCertificateRevokedRequest) (*invv1.MarkCertificateRevokedResponse, error) {
	actor, _, err := s.source(ctx, req.GetTenantId())
	if err != nil {
		return nil, err
	}
	cancelled, flagged, err := s.Svc.MarkRevoked(ctx, strings.ToLower(req.GetTenantId()), actor, req.GetCertificateId())
	if err != nil {
		return nil, certError(err)
	}
	return &invv1.MarkCertificateRevokedResponse{CancelledItems: int32(cancelled), FlaggedHosts: int32(flagged)}, nil // #nosec G115 -- bounded by the tenant's items
}

// deliveryStates maps item states to the proto enum.
var deliveryStates = map[string]invv1.DeliveryState{
	store.DeliveryPending: invv1.DeliveryState_DELIVERY_STATE_PENDING, store.DeliveryDelivered: invv1.DeliveryState_DELIVERY_STATE_DELIVERED,
	store.DeliveryFetched: invv1.DeliveryState_DELIVERY_STATE_FETCHED, store.DeliveryInstalled: invv1.DeliveryState_DELIVERY_STATE_INSTALLED,
	store.DeliveryUnchanged: invv1.DeliveryState_DELIVERY_STATE_UNCHANGED, store.DeliveryFailed: invv1.DeliveryState_DELIVERY_STATE_FAILED,
	store.DeliveryHookFailed: invv1.DeliveryState_DELIVERY_STATE_HOOK_FAILED, store.DeliveryUnsupported: invv1.DeliveryState_DELIVERY_STATE_UNSUPPORTED,
	store.DeliverySuperseded: invv1.DeliveryState_DELIVERY_STATE_SUPERSEDED, store.DeliveryExpired: invv1.DeliveryState_DELIVERY_STATE_EXPIRED,
	store.DeliveryCancelled: invv1.DeliveryState_DELIVERY_STATE_CANCELLED,
}

func deliveryToPB(v certdelivery.DeliveryView) *invv1.CertificateDelivery {
	out := &invv1.CertificateDelivery{Id: v.ID, TenantId: v.TenantID, CertificateId: v.CertificateID, Name: v.Name, KeyPolicy: v.KeyPolicy,
		CreatedAt: v.CreatedAt.Unix(), ExpiresAt: v.ExpiresAt.Unix(), UnknownHostIds: v.UnknownHostIDs, Created: v.Created,
		Items: make([]*invv1.CertificateDeliveryItem, 0, len(v.Items))}
	for _, i := range v.Items {
		code := int32(-1)
		if i.HookExitCode != nil {
			code = int32(*i.HookExitCode) // #nosec G115 -- -1..256
		}
		out.Items = append(out.Items, &invv1.CertificateDeliveryItem{Id: i.ID, HostId: i.HostID, Hostname: i.Hostname, AgentOnline: i.AgentOnline,
			State: deliveryStates[i.State], Reason: i.Reason, Serial: i.Serial, FingerprintSha256: i.FingerprintSHA256, HookExitCode: code,
			Attempts: int32(i.Attempts), UpdatedAt: i.UpdatedAt.Unix()}) // #nosec G115 -- attempts <= 5
	}
	return out
}

// unixOf is t in unix seconds (nil: 0 = never).
func unixOf(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.Unix()
}
