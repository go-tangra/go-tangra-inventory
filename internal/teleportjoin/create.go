package teleportjoin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// MaxBatch bounds the enrollment ids of one GetTeleportJoins call.
const MaxBatch = 200

// Request is a CreateTeleportJoin call of a mesh source.
type Request struct {
	TenantID     string
	Source       string // SPIFFE service name of the caller
	RequestedBy  string // SPIFFE id of the caller
	EnrollmentID string // the source's id; idempotency key per (tenant, source)
	HostID       string
	ClusterID    string
	ExpiresAt    time.Time
}

// View is an item with what the caller shows next to it.
type View struct {
	store.TeleportJoinItem
	Hostname    string
	AgentOnline bool
	Created     bool // false: idempotent replay of an existing enrollment
}

// checkRequest validates every field of r.
func (s *Service) checkRequest(r Request) error {
	now := s.now()
	switch {
	case !uuidRE.MatchString(r.TenantID):
		return invalid("tenant_id")
	case !sourceRE.MatchString(r.Source):
		return invalid("source")
	case !uuidRE.MatchString(r.EnrollmentID):
		return invalid("enrollment_id")
	case !uuidRE.MatchString(r.HostID):
		return invalid("host_id")
	case !uuidRE.MatchString(r.ClusterID):
		return invalid("cluster_id")
	case !r.ExpiresAt.After(now) || r.ExpiresAt.After(now.Add(store.MaxTeleportTTL)):
		return invalid("expires_at")
	}
	return nil
}

// Create creates a join item: validates the request, replays an existing
// item of the same (tenant, source, enrollment id), refuses a host that
// cannot join now (*IneligibleError, audited, nothing stored), stores the
// item with its audit row (an older active item of the same host and
// cluster is superseded in the same transaction) and pushes the
// TELEPORT_JOIN command when the agent is online.
func (s *Service) Create(ctx context.Context, r Request) (View, error) {
	if !s.cfg.Enabled {
		return View{}, ErrDisabled
	}
	r.TenantID, r.EnrollmentID = strings.ToLower(r.TenantID), strings.ToLower(r.EnrollmentID)
	r.HostID, r.ClusterID = strings.ToLower(r.HostID), strings.ToLower(r.ClusterID)
	if err := s.checkRequest(r); err != nil {
		return View{}, err
	}
	actor := Actor{Kind: audit.ActorService, ID: r.Source}
	if v, done, err := s.replay(ctx, r); done {
		return v, err
	}
	h, err := s.repo.GetHost(ctx, r.TenantID, r.HostID)
	if err != nil {
		return View{}, err
	}
	a, reason, err := s.eligibility(ctx, h)
	if err != nil {
		return View{}, err
	}
	if reason != "" {
		s.refuse(ctx, r.TenantID, actor, r.EnrollmentID, reason)
		return View{}, &IneligibleError{Reason: reason}
	}
	for attempt := 0; ; attempt++ {
		n := s.build(r, actor, a.ID)
		superseded, err := s.repo.CreateTeleportItem(ctx, n)
		if errors.Is(err, repo.ErrConflict) && attempt == 0 {
			// The same enrollment id was created concurrently (replay it),
			// or another enrollment made an active item for the host and
			// cluster first (build again: it is superseded now).
			if v, done, err := s.replay(ctx, r); done {
				return v, err
			}
			continue
		}
		if err != nil {
			return View{}, err
		}
		for _, i := range superseded {
			s.metrics.transition(i.State, i.Reason)
		}
		s.metrics.transition(n.Item.State, n.Item.Reason)
		s.push(ctx, n.Item)
		v, err := s.view(ctx, r.TenantID, n.Item.ID)
		v.Created = true
		return v, err
	}
}

// replay returns the existing item of r's enrollment id (done). An id
// reused for another host or cluster is refused.
func (s *Service) replay(ctx context.Context, r Request) (View, bool, error) {
	i, err := s.repo.GetTeleportItemByEnrollment(ctx, r.TenantID, r.Source, r.EnrollmentID)
	if errors.Is(err, repo.ErrNotFound) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, true, err
	}
	if i.HostID != r.HostID || i.ClusterID != r.ClusterID {
		return View{}, true, invalid("enrollment_id")
	}
	v, err := s.view(ctx, r.TenantID, i.ID)
	return v, true, err
}

// build assembles the item and its audit row.
func (s *Service) build(r Request, actor Actor, agentID string) repo.NewTeleportItem {
	now := s.now()
	i := store.TeleportJoinItem{ID: s.newID(), TenantID: r.TenantID, Source: r.Source, EnrollmentID: r.EnrollmentID, HostID: r.HostID,
		AgentID: agentID, ClusterID: r.ClusterID, State: store.DeliveryPending, Attempt: 1,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: r.ExpiresAt.UTC()}
	return repo.NewTeleportItem{Item: i,
		Audit: []store.AuditRow{s.itemRow(audit.TeleportJoinCreated, actor, audit.OutcomeOK, i,
			map[string]any{"requested_by": r.RequestedBy, "expires_at": i.ExpiresAt.Format(time.RFC3339)})},
		SupersedeAudit: func(old store.TeleportJoinItem, newID string) store.AuditRow {
			return s.itemRow(audit.TeleportJoinSuperseded, system, audit.OutcomeOK, old, map[string]any{"superseded_by": newID})
		}}
}

// GetMany returns the items of a source's enrollment ids (1..MaxBatch);
// unknown ids, and ids that are not uuids, are omitted. Each id is answered
// once, in the order asked.
func (s *Service) GetMany(ctx context.Context, tenantID, source string, enrollmentIDs []string) ([]View, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if len(enrollmentIDs) < 1 || len(enrollmentIDs) > MaxBatch {
		return nil, invalid("enrollment_ids")
	}
	tenantID = strings.ToLower(tenantID)
	out := make([]View, 0, len(enrollmentIDs))
	seen := make(map[string]bool, len(enrollmentIDs))
	for _, id := range enrollmentIDs {
		id = strings.ToLower(id)
		if !uuidRE.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		i, err := s.repo.GetTeleportItemByEnrollment(ctx, tenantID, source, id)
		if errors.Is(err, repo.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		v, err := s.view(ctx, tenantID, i.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// Cancel ends the active item of a source's enrollment id (cancelled,
// audited); a terminal item is returned unchanged. An agent that already
// fetched the material learns of it at its next installing check (every
// 30 s) and rolls its join back; its later report is ignored, and the
// source revokes the token it minted.
func (s *Service) Cancel(ctx context.Context, tenantID, source, requestedBy, enrollmentID string) (View, error) {
	if !s.cfg.Enabled {
		return View{}, ErrDisabled
	}
	tenantID, enrollmentID = strings.ToLower(tenantID), strings.ToLower(enrollmentID)
	if !uuidRE.MatchString(enrollmentID) {
		return View{}, repo.ErrNotFound
	}
	i, err := s.repo.GetTeleportItemByEnrollment(ctx, tenantID, source, enrollmentID)
	if err != nil {
		return View{}, err
	}
	actor := Actor{Kind: audit.ActorService, ID: source}
	if _, _, err := s.update(ctx, tenantID, i.ID, func(cur *store.TeleportJoinItem) ([]store.AuditRow, error) {
		if !cur.Active() {
			return nil, errNoChange
		}
		s.finish(cur, store.DeliveryCancelled, "")
		return []store.AuditRow{s.itemRow(audit.TeleportJoinCancelled, actor, audit.OutcomeOK, *cur,
			map[string]any{"requested_by": requestedBy})}, nil
	}); err != nil {
		return View{}, err
	}
	return s.view(ctx, tenantID, i.ID)
}

// view reads item id with its hostname and whether its agent is online.
func (s *Service) view(ctx context.Context, tenantID, id string) (View, error) {
	i, err := s.repo.GetTeleportItem(ctx, tenantID, id)
	if err != nil {
		return View{}, err
	}
	v := View{TeleportJoinItem: i, AgentOnline: s.online(ctx, i.AgentID)}
	if h, err := s.repo.GetHost(ctx, tenantID, i.HostID); err == nil {
		v.Hostname = h.Hostname
	}
	return v, nil
}
