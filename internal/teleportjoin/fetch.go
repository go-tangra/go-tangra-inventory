package teleportjoin

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Refusal reasons (teleport_join_refused) besides the ineligibility reasons
// of Create.
const (
	RefusedNotFound         = "not_found"
	RefusedNotActive        = "not_active"
	RefusedFetchLimit       = "fetch_limit"
	RefusedPlaintext        = "plaintext"
	RefusedSourceNotAllowed = "source_not_allowed"
	RefusedNotFetched       = "not_fetched"
)

// MaxTokenBytes bounds the join token a source may hand out (the agent
// checks the full material again).
const MaxTokenBytes = 256

// Source outcomes (metrics) and the errors of a render.
var (
	errSourceRefused = errors.New("teleportjoin: source refused the render")
	errBadMaterial   = errors.New("teleportjoin: malformed join material")
)

func agentActor(a store.Agent) Actor { return Actor{Kind: audit.ActorAgent, ID: a.ID} }

// ownItem returns item id of agent a. Another agent's or tenant's item, a
// missing one, or (only when fetch is set) one that may no longer be
// fetched is audited as refused and reported as repo.ErrNotFound (no
// oracle).
func (s *Service) ownItem(ctx context.Context, a store.Agent, id string, fetch bool) (store.TeleportJoinItem, error) {
	i, err := s.repo.GetTeleportItem(ctx, a.TenantID, id)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return store.TeleportJoinItem{}, err
	}
	reason := ""
	switch {
	case err != nil || i.AgentID != a.ID || i.HostID != a.HostID:
		reason = RefusedNotFound
	case fetch && (!fetchable(i.State) || !s.now().Before(i.ExpiresAt)):
		reason = RefusedNotActive
	case fetch && i.Fetches >= store.MaxTeleportFetches:
		reason = RefusedFetchLimit
	default:
		return i, nil
	}
	s.refuse(ctx, a.TenantID, agentActor(a), id, reason)
	return store.TeleportJoinItem{}, repo.ErrNotFound
}

// RefusePlaintext records a fetch refused because the ingest edge serves
// plaintext without allow_plaintext_ingest.
func (s *Service) RefusePlaintext(ctx context.Context, a store.Agent, itemID string) {
	s.refuse(ctx, a.TenantID, agentActor(a), itemID, RefusedPlaintext)
}

// Wipe drops the secret of m (Go strings cannot be zeroed in place: the
// reference is dropped so nothing of the inventory keeps it).
func Wipe(m *invv1.TeleportJoinMaterial) {
	if m != nil {
		m.Token = ""
	}
}

// Fetch serves item itemID to its agent a: only the agent's own pending,
// delivered or fetched unexpired item with fetches left, and only while the
// host is claimed by that agent alone. The fetch is counted first; then the
// source renders the material for the verified agent's host (minting the
// single-use token at that moment). A source that refuses (NotFound) fails
// the item (source_refused) and the agent gets repo.ErrNotFound; any other
// source failure returns ErrSourceUnavailable until the fetch budget is
// spent, which fails the item (fetch_budget). The returned material carries
// the item id and is relayed as is; the caller keeps no copy.
func (s *Service) Fetch(ctx context.Context, a store.Agent, itemID string) (*invv1.TeleportJoinMaterial, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if !uuidRE.MatchString(itemID) {
		return nil, invalid("item_id")
	}
	itemID = strings.ToLower(itemID)
	it, err := s.ownItem(ctx, a, itemID, true)
	if err != nil {
		return nil, err
	}
	agents, err := s.repo.ListAgents(ctx, a.TenantID)
	if err != nil {
		return nil, err
	}
	if _, ambiguous := hostAgents(agents); ambiguous[it.HostID] {
		// Another agent claims the host since the item was created: nothing
		// is handed out until an operator revokes one of them.
		return nil, s.failItem(ctx, it, store.DeliveryUnsupported, store.ReasonAmbiguousAgent, audit.TeleportJoinUnsupported)
	}
	it, changed, err := s.update(ctx, a.TenantID, it.ID, func(cur *store.TeleportJoinItem) ([]store.AuditRow, error) {
		if !fetchable(cur.State) || cur.AgentID != a.ID || cur.Fetches >= store.MaxTeleportFetches {
			return nil, errNoChange
		}
		cur.Fetches++
		cur.UpdatedAt = s.now()
		return nil, nil
	})
	if err != nil {
		return nil, err
	}
	if !changed {
		s.refuse(ctx, a.TenantID, agentActor(a), itemID, RefusedNotActive)
		return nil, repo.ErrNotFound
	}
	start := s.now()
	m, err := s.render(ctx, it)
	s.metrics.source(s.now().Sub(start), sourceOutcome(err))
	if err != nil {
		// The agent only learns NotFound or Unavailable; the operator needs
		// the source's status code to tell a refusal from a broken path.
		s.log.Warn("teleport join: source failed", "item", it.ID, "source", s.cfg.SourceService,
			"outcome", sourceOutcome(err), "code", sourceCode(err).String(), "fetches", it.Fetches)
	}
	switch {
	case errors.Is(err, errSourceRefused):
		return nil, s.failItem(ctx, it, store.DeliveryFailed, store.ReasonSourceRefused, audit.TeleportJoinFailed)
	case err != nil && it.Fetches >= store.MaxTeleportFetches:
		return nil, s.failItem(ctx, it, store.DeliveryFailed, store.ReasonFetchBudget, audit.TeleportJoinFailed)
	case err != nil:
		return nil, ErrSourceUnavailable
	}
	_, changed, err = s.update(ctx, a.TenantID, it.ID, func(cur *store.TeleportJoinItem) ([]store.AuditRow, error) {
		if !fetchable(cur.State) || cur.AgentID != a.ID {
			return nil, errNoChange
		}
		now := s.now()
		cur.State, cur.UpdatedAt, cur.FetchedAt = store.DeliveryFetched, now, &now
		return []store.AuditRow{s.itemRow(audit.TeleportJoinFetched, agentActor(a), audit.OutcomeOK, *cur,
			map[string]any{"fetches": cur.Fetches, "teleport_version": m.GetTeleportVersion(), "proxy_addr": m.GetProxyAddr(),
				"labels": len(m.GetLabels()), "ca_pins": len(m.GetCaPins())})}, nil
	})
	if err != nil || !changed {
		Wipe(m)
		if err == nil {
			s.refuse(ctx, a.TenantID, agentActor(a), itemID, RefusedNotActive)
			err = repo.ErrNotFound
		}
		return nil, err
	}
	m.ItemId, m.EnrollmentId = it.ID, it.EnrollmentID
	return m, nil
}

// failItem ends an active item with state and reason (server side) and
// returns repo.ErrNotFound for the agent: the item is no longer its to
// fetch.
func (s *Service) failItem(ctx context.Context, it store.TeleportJoinItem, state, reason string, t audit.EventType) error {
	_, _, err := s.update(ctx, it.TenantID, it.ID, func(cur *store.TeleportJoinItem) ([]store.AuditRow, error) {
		if !cur.Active() {
			return nil, errNoChange
		}
		s.finish(cur, state, reason)
		return []store.AuditRow{s.itemRow(t, system, audit.OutcomeError, *cur, map[string]any{"fetches": cur.Fetches})}, nil
	})
	if err != nil {
		return err
	}
	return repo.ErrNotFound
}

// sourceOutcome names a render result for the metrics.
func sourceOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, errSourceRefused):
		return "refused"
	case errors.Is(err, errBadMaterial):
		return "bad_material"
	}
	return "unavailable"
}

// sourceErr maps a source failure: NotFound (unknown, expired, mismatched
// or cancelled enrollment, renders spent) is a refusal; anything else is
// retryable. The remote message is never propagated; the status code is
// kept for the log.
func sourceErr(err error) error {
	f := sourceFailure{err: ErrSourceUnavailable, code: status.Code(err)}
	if f.code == codes.NotFound {
		f.err = errSourceRefused
	}
	return f
}

// sourceFailure is a mapped source error with the source's status code.
type sourceFailure struct {
	err  error
	code codes.Code
}

func (f sourceFailure) Error() string { return f.err.Error() }
func (f sourceFailure) Unwrap() error { return f.err }

// sourceCode is the source's status code of err (Unknown when err did not
// come from the source, e.g. malformed material).
func sourceCode(err error) codes.Code {
	var f sourceFailure
	if errors.As(err, &f) {
		return f.code
	}
	return codes.Unknown
}

// render asks the source for the material of it (the verified agent's
// host) and checks that it belongs to the item's enrollment and carries a
// token of at most MaxTokenBytes. Malformed material is dropped.
func (s *Service) render(ctx context.Context, it store.TeleportJoinItem) (*invv1.TeleportJoinMaterial, error) {
	if s.cfg.SourceTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.SourceTimeout)
		defer cancel()
	}
	m, err := s.src.RenderTeleportJoin(ctx, &invv1.RenderTeleportJoinRequest{TenantId: it.TenantID, EnrollmentId: it.EnrollmentID,
		HostId: it.HostID, ItemId: it.ID})
	if err != nil {
		return nil, sourceErr(err)
	}
	if m.GetToken() == "" || len(m.GetToken()) > MaxTokenBytes ||
		(m.GetEnrollmentId() != "" && !strings.EqualFold(m.GetEnrollmentId(), it.EnrollmentID)) {
		Wipe(m)
		return nil, errBadMaterial
	}
	return m, nil
}
