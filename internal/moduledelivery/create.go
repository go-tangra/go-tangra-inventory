package moduledelivery

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Request is a CreateModuleDelivery call of a mesh source.
type Request struct {
	TenantID    string
	Source      string // SPIFFE service name of the caller
	RequestedBy string // SPIFFE id of the caller
	DeliveryID  string // the source's id; idempotency key per (tenant, source)
	HostID      string
	Module      string
	Version     string
	ExpiresAt   time.Time
}

// View is an item with what the caller shows next to it.
type View struct {
	store.ModuleDeliveryItem
	Hostname    string
	AgentOnline bool
	Created     bool // false: idempotent replay of an existing delivery
}

// checkRequest validates every field of r.
func (s *Service) checkRequest(r Request) error {
	now := s.now()
	switch {
	case !uuidRE.MatchString(r.TenantID):
		return invalid("tenant_id")
	case !sourceRE.MatchString(r.Source):
		return invalid("source")
	case !uuidRE.MatchString(r.DeliveryID):
		return invalid("delivery_id")
	case !uuidRE.MatchString(r.HostID):
		return invalid("host_id")
	case !ValidModule(r.Module):
		return invalid("module")
	case !ValidVersion(r.Version):
		return invalid("version")
	case !r.ExpiresAt.After(now) || r.ExpiresAt.After(now.Add(store.MaxModuleTTL)):
		return invalid("expires_at")
	}
	return nil
}

// Create creates a delivery item (contracts/proto.md): validates the
// request, replays an existing item of the same (tenant, source, delivery
// id), refuses a host that cannot receive a module now (*IneligibleError,
// audited, nothing stored), stores the item with its audit row (an older
// active item of the same host and module is superseded in the same
// transaction) and pushes the MODULE command when the agent is online.
func (s *Service) Create(ctx context.Context, r Request) (View, error) {
	if !s.cfg.Enabled {
		return View{}, ErrDisabled
	}
	r.TenantID, r.DeliveryID, r.HostID = strings.ToLower(r.TenantID), strings.ToLower(r.DeliveryID), strings.ToLower(r.HostID)
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
		s.refuse(ctx, r.TenantID, actor, r.DeliveryID, reason)
		return View{}, &IneligibleError{Reason: reason}
	}
	for attempt := 0; ; attempt++ {
		n := s.build(r, actor, a.ID)
		superseded, err := s.repo.CreateModuleItem(ctx, n)
		if errors.Is(err, repo.ErrConflict) && attempt == 0 {
			// The same delivery id was created concurrently (replay it), or
			// another delivery made an active item for the host and module
			// first (build again: it is superseded now).
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

// replay returns the existing item of r's delivery id (done). An id reused
// for another host, module or version is refused.
func (s *Service) replay(ctx context.Context, r Request) (View, bool, error) {
	i, err := s.repo.GetModuleItemByDelivery(ctx, r.TenantID, r.Source, r.DeliveryID)
	if errors.Is(err, repo.ErrNotFound) {
		return View{}, false, nil
	}
	if err != nil {
		return View{}, true, err
	}
	if i.HostID != r.HostID || i.Module != r.Module || i.Version != r.Version {
		return View{}, true, invalid("delivery_id")
	}
	v, err := s.view(ctx, r.TenantID, i.ID)
	return v, true, err
}

// build assembles the item and its audit row.
func (s *Service) build(r Request, actor Actor, agentID string) repo.NewModuleItem {
	now := s.now()
	i := store.ModuleDeliveryItem{ID: s.newID(), TenantID: r.TenantID, Source: r.Source, DeliveryID: r.DeliveryID, HostID: r.HostID,
		AgentID: agentID, Module: r.Module, Version: r.Version, State: store.DeliveryPending, HookExitCode: -1, Attempt: 1,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: r.ExpiresAt.UTC()}
	return repo.NewModuleItem{Item: i,
		Audit: []store.AuditRow{s.itemRow(audit.ModuleDeliveryCreated, actor, audit.OutcomeOK, i,
			map[string]any{"requested_by": r.RequestedBy, "expires_at": i.ExpiresAt.Format(time.RFC3339)})},
		SupersedeAudit: func(old store.ModuleDeliveryItem, newID string) store.AuditRow {
			return s.itemRow(audit.ModuleDeliverySuperseded, system, audit.OutcomeOK, old, map[string]any{"superseded_by": newID})
		}}
}

// Get returns the item of a source's delivery id.
func (s *Service) Get(ctx context.Context, tenantID, source, deliveryID string) (View, error) {
	if !s.cfg.Enabled {
		return View{}, ErrDisabled
	}
	if !uuidRE.MatchString(deliveryID) {
		return View{}, repo.ErrNotFound
	}
	i, err := s.repo.GetModuleItemByDelivery(ctx, strings.ToLower(tenantID), source, strings.ToLower(deliveryID))
	if err != nil {
		return View{}, err
	}
	return s.view(ctx, i.TenantID, i.ID)
}

// view reads item id with its hostname and whether its agent is online.
func (s *Service) view(ctx context.Context, tenantID, id string) (View, error) {
	i, err := s.repo.GetModuleItem(ctx, tenantID, id)
	if err != nil {
		return View{}, err
	}
	v := View{ModuleDeliveryItem: i, AgentOnline: s.online(ctx, i.AgentID)}
	if h, err := s.repo.GetHost(ctx, tenantID, i.HostID); err == nil {
		v.Hostname = h.Hostname
	}
	return v, nil
}
