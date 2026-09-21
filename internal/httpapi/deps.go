package httpapi

import (
	"errors"
	"net/http"

	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/backup"
	"github.com/go-freya/freya/services/inventory/internal/enroll"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/registry"
	"github.com/go-freya/freya/services/inventory/internal/repo"
	"github.com/go-freya/freya/services/inventory/internal/snapshots"
	"github.com/go-freya/freya/services/inventory/internal/stats"
	"github.com/go-freya/freya/services/inventory/internal/stream"
)

// Deps wire the inventory HTTP handlers. Every field is required except Hub,
// which is optional: when set it enables the GET /stream SSE route, otherwise
// that route stays 501 not_implemented.
type Deps struct {
	Hosts     *hosts.Service
	Snapshots *snapshots.Service
	Stats     *stats.Service
	Backup    *backup.Service
	Enroll    *enroll.Service
	Registry  registry.Registry
	Hub       *stream.Hub // optional: enables GET /stream (SSE) when set
}

// subjects derives the authz subject from the verified platform identity. The
// gateway forwards a human/user caller on every gateway-proxied route.
func subjects(r *http.Request) (authz.Subjects, error) {
	id, err := Caller(r)
	if err != nil {
		return authz.Subjects{}, err
	}
	return authz.Subjects{TenantID: id.TenantID, UserID: id.UserID, Roles: id.Roles, ActorKind: authz.ActorUser}, nil
}

// failSvc maps a service/domain error to an HTTP response from the OpenAPI
// closed vocabulary. Not-found sentinels (hosts, snapshots, repo) collapse to
// 404; authorization to 403; a bad backup schema or an invalid enrollment
// request to 422; a store conflict to 409; a missing caller to 401; anything
// else to 500. Error detail is never leaked to the client.
func failSvc(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, hosts.ErrNotFound), errors.Is(err, snapshots.ErrNotFound), errors.Is(err, repo.ErrNotFound):
		WriteError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, authz.ErrForbidden):
		WriteError(w, http.StatusForbidden, "forbidden")
	case errors.Is(err, backup.ErrBadSchema), errors.Is(err, enroll.ErrTokenInvalid):
		WriteError(w, http.StatusUnprocessableEntity, "validation_failed")
	case errors.Is(err, repo.ErrConflict):
		WriteError(w, http.StatusConflict, "conflict")
	case errors.Is(err, enroll.ErrUnauthenticated), errors.Is(err, ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "unauthenticated")
	default:
		WriteError(w, http.StatusInternalServerError, "internal")
	}
}
