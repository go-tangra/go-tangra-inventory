// Package grpcapi serves inventory.v1 for other platform services on the Freya
// SPIFFE mTLS channel: the caller is an authenticated service acting for the
// tenant named in the request. Nothing here is proxied by the gateway. The
// tenant comes from the request and the actor identity from the verified SPIFFE
// peer. Responses never carry agent credentials or sealed fields; the only
// secret ever returned is the one-time token from MintEnrollmentToken.
package grpcapi

import (
	"context"
	"errors"
	"regexp"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/go-freya/freya/authn"
	invv1 "github.com/go-freya/freya/services/inventory/api/proto/inventory/v1"
	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/backup"
	"github.com/go-freya/freya/services/inventory/internal/enroll"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/registry"
	"github.com/go-freya/freya/services/inventory/internal/repo"
	"github.com/go-freya/freya/services/inventory/internal/snapshots"
	"github.com/go-freya/freya/services/inventory/internal/stats"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// callerFunc resolves the SPIFFE identity of a call (overridable in tests).
var callerFunc = func(ctx context.Context) (string, bool) {
	p, ok := authn.FromContext(ctx)
	if !ok {
		return "", false
	}
	return p.ID.String(), true
}

// caller returns the service subjects for the tenant named in the request; the
// tenant must be a uuid and the peer must present a SPIFFE identity.
func caller(ctx context.Context, tenantID string) (authz.Subjects, error) {
	id, ok := callerFunc(ctx)
	if !ok {
		return authz.Subjects{}, status.Error(codes.Unauthenticated, "service identity required")
	}
	if !uuidRE.MatchString(tenantID) {
		return authz.Subjects{}, status.Error(codes.InvalidArgument, "tenant_id must be a uuid")
	}
	return authz.Subjects{TenantID: tenantID, UserID: id, ActorKind: authz.ActorService}, nil
}

// grpcError maps a service/domain error to a gRPC status. Detail is never
// surfaced beyond the stable reason string.
func grpcError(err error) error {
	switch {
	case errors.Is(err, hosts.ErrNotFound), errors.Is(err, snapshots.ErrNotFound), errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, "not_found")
	case errors.Is(err, authz.ErrForbidden):
		return status.Error(codes.PermissionDenied, "forbidden")
	case errors.Is(err, backup.ErrBadSchema), errors.Is(err, enroll.ErrTokenInvalid):
		return status.Error(codes.InvalidArgument, "validation_failed")
	case errors.Is(err, repo.ErrConflict):
		return status.Error(codes.FailedPrecondition, "conflict")
	case errors.Is(err, enroll.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}
	return status.Error(codes.Unavailable, "temporarily_unavailable")
}

// Deps carries the services the inventory.v1 servers use. Backup is wired for
// parity with the app but has no mesh RPC of its own.
type Deps struct {
	Hosts     *hosts.Service
	Snapshots *snapshots.Service
	Stats     *stats.Service
	Backup    *backup.Service
	Enroll    *enroll.Service
	Registry  registry.Registry
}

// Register registers the inventory.v1 mesh servers on the gRPC server. Callers
// are authenticated services; nothing here is gateway-proxied.
func Register(gs grpc.ServiceRegistrar, d Deps) {
	if d.Hosts != nil {
		invv1.RegisterInventoryHostServiceServer(gs, &HostServer{Hosts: d.Hosts})
	}
	if d.Snapshots != nil {
		invv1.RegisterInventorySnapshotServiceServer(gs, &SnapshotServer{Snapshots: d.Snapshots})
	}
	if d.Stats != nil {
		invv1.RegisterInventoryStatisticsServiceServer(gs, &StatisticsServer{Stats: d.Stats})
	}
	if d.Enroll != nil || d.Registry != nil {
		invv1.RegisterInventoryAgentServiceServer(gs, &AgentServer{Enroll: d.Enroll, Registry: d.Registry})
	}
}
