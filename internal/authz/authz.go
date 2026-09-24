// Package authz is the inventory access model. Unlike paperless (which resolves
// per-resource permission tuples), inventory authorization is coarse: a caller
// is scoped to a tenant and carries API permissions/roles decided upstream by
// the gateway. This package only answers "is this caller a tenant super-user?"
// and "may this caller act within this tenant?" — system-wide operations
// (aggregate stats, cross-tenant maintenance) require an admin subject.
package authz

import (
	"errors"
	"fmt"
)

// ErrForbidden is returned when a caller lacks the required scope.
var ErrForbidden = errors.New("authz: forbidden")

// Actor kinds (closed set): a human user, a peer service on the mesh, an
// endpoint agent posting to the ingest listener, or the trusted system scope.
const (
	ActorUser    = "user"
	ActorService = "service"
	ActorAgent   = "agent"
	ActorSystem  = "system"
)

// Subjects is the authenticated caller: a tenant, an actor identity, and the
// roles the gateway resolved for it.
type Subjects struct {
	TenantID  string
	UserID    string
	Roles     []string
	ActorKind string // user | service | agent | system
}

// IsAdmin reports whether the caller is a tenant super-user. The platform's
// "owner" (tenant owner) and "admin" roles both confer full control; the system
// scope is likewise treated as admin so trusted worker paths pass admin gates.
func (s Subjects) IsAdmin() bool {
	if s.ActorKind == ActorSystem {
		return true
	}
	for _, r := range s.Roles {
		if r == "admin" || r == "owner" {
			return true
		}
	}
	return false
}

// ActorID is the user id, falling back to the actor kind for non-user callers.
func (s Subjects) ActorID() string {
	if s.UserID != "" {
		return s.UserID
	}
	return s.ActorKind
}

// RequireAdmin permits only tenant super-users (or the system scope). It guards
// system-wide operations such as aggregate statistics and maintenance.
func RequireAdmin(s Subjects) error {
	if s.IsAdmin() {
		return nil
	}
	return fmt.Errorf("%w: admin required", ErrForbidden)
}

// RequireTenant ensures the caller may act on tenantID. A non-admin caller may
// act only within its own tenant; an admin may act cross-tenant only via system
// paths, so a bare RequireTenant still refuses an empty or mismatched tenant.
func RequireTenant(s Subjects, tenantID string) error {
	if tenantID == "" {
		return fmt.Errorf("%w: tenant required", ErrForbidden)
	}
	if s.TenantID == tenantID {
		return nil
	}
	if s.ActorKind == ActorSystem {
		return nil
	}
	return fmt.Errorf("%w: tenant mismatch", ErrForbidden)
}
