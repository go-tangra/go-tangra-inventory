// Package stats computes inventory statistics rollups. A tenant caller gets its
// own rollup (hosts by status/OS/manufacturer, agent online/offline, stale
// hosts, snapshot counts, and hardware/software aggregates); an administrator
// can request the system-wide breakdown keyed by tenant. The "stale" cutoff is
// derived from the service clock minus StaleAfter and passed to the store so the
// stale-host count is computed consistently with the maintenance worker.
package stats

import (
	"context"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/repo"
)

// DefaultStaleAfter is the age past a host's last_seen after which it counts as
// stale when no explicit window is configured.
const DefaultStaleAfter = 7 * 24 * time.Hour

// Service computes statistics.
type Service struct {
	st         repo.Store
	staleAfter time.Duration
	now        func() time.Time
}

// New builds the service with the default stale window.
func New(st repo.Store) *Service {
	return &Service{st: st, staleAfter: DefaultStaleAfter, now: time.Now}
}

// SetStaleAfter overrides the stale window (the age past last_seen at which a
// host is counted stale).
func (s *Service) SetStaleAfter(d time.Duration) {
	if d > 0 {
		s.staleAfter = d
	}
}

// SetClock injects the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// staleBefore is the last_seen cutoff: hosts last seen before it are stale.
func (s *Service) staleBefore() time.Time {
	return s.now().UTC().Add(-s.staleAfter)
}

// Tenant returns the caller's tenant statistics.
func (s *Service) Tenant(ctx context.Context, subj authz.Subjects) (repo.Stats, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return repo.Stats{}, err
	}
	return s.st.TenantStats(ctx, subj.TenantID, s.staleBefore())
}

// System returns per-tenant statistics keyed by tenant id. Admin only:
// non-admin callers get authz.ErrForbidden.
func (s *Service) System(ctx context.Context, subj authz.Subjects) (map[string]repo.Stats, error) {
	if err := authz.RequireAdmin(subj); err != nil {
		return nil, err
	}
	ids, err := s.st.TenantIDs(ctx)
	if err != nil {
		return nil, err
	}
	cutoff := s.staleBefore()
	out := make(map[string]repo.Stats, len(ids))
	for _, id := range ids {
		st, err := s.st.TenantStats(ctx, id, cutoff)
		if err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, nil
}
