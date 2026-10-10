package moduledelivery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Refusal reasons (module_delivery_refused) besides the ineligibility
// reasons of Create.
const (
	RefusedNotFound         = "not_found"
	RefusedNotActive        = "not_active"
	RefusedFetchLimit       = "fetch_limit"
	RefusedPlaintext        = "plaintext"
	RefusedSourceNotAllowed = "source_not_allowed"
	RefusedNotFetched       = "not_fetched"
)

// MaxChunkBytes bounds one chunk of a bundle stream (both hops).
const MaxChunkBytes = 1 << 20

// Bundle is the rendered bundle of one item. It lives only in the memory of
// the FetchModuleBundle call: the caller streams it to the agent and calls
// Wipe; nothing of it is stored, cached, logged or published.
type Bundle struct {
	ItemID     string
	DeliveryID string
	Module     string
	Version    string
	SHA256     string // lowercase hex of Data
	Data       []byte
}

// Wipe zeroes the bundle bytes.
func (b *Bundle) Wipe() {
	clear(b.Data)
	b.Data = nil
}

// Source outcomes (metrics) and the errors of a render.
var (
	errSourceRefused = errors.New("moduledelivery: source refused the render")
	errBadBundle     = errors.New("moduledelivery: malformed bundle stream")
)

func agentActor(a store.Agent) Actor { return Actor{Kind: audit.ActorAgent, ID: a.ID} }

// ownItem returns item id of agent a. Another agent's or tenant's item, a
// missing one, or (only when active is set) one that may no longer be
// fetched is audited as refused and reported as repo.ErrNotFound (no
// oracle).
func (s *Service) ownItem(ctx context.Context, a store.Agent, id string, active bool) (store.ModuleDeliveryItem, error) {
	i, err := s.repo.GetModuleItem(ctx, a.TenantID, id)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return store.ModuleDeliveryItem{}, err
	}
	reason := ""
	switch {
	case err != nil || i.AgentID != a.ID || i.HostID != a.HostID:
		reason = RefusedNotFound
	case active && (!i.Active() || !s.now().Before(i.ExpiresAt)):
		reason = RefusedNotActive
	case active && i.Fetches >= store.MaxModuleFetches:
		reason = RefusedFetchLimit
	default:
		return i, nil
	}
	s.refuse(ctx, a.TenantID, agentActor(a), id, reason)
	return store.ModuleDeliveryItem{}, repo.ErrNotFound
}

// RefusePlaintext records a fetch refused because the ingest edge serves
// plaintext without allow_plaintext_ingest.
func (s *Service) RefusePlaintext(ctx context.Context, a store.Agent, itemID string) {
	s.refuse(ctx, a.TenantID, agentActor(a), itemID, RefusedPlaintext)
}

// Fetch serves item itemID to its agent a (contracts/proto.md): only the
// agent's own active, unexpired item with fetches left, and only while the
// host is claimed by that agent alone. The fetch is counted first; then the
// source renders the bundle for the verified agent's host, the stream is
// buffered (at most store.MaxModuleBundleBytes) and its size and SHA-256
// are checked. A source that refuses (NotFound) fails the item
// (source_refused) and the agent gets repo.ErrNotFound; any other source
// failure returns ErrSourceUnavailable until the fetch budget is spent,
// which fails the item (fetch_budget). The caller must Wipe the bundle.
func (s *Service) Fetch(ctx context.Context, a store.Agent, itemID string) (*Bundle, error) {
	if !s.cfg.Enabled {
		return nil, ErrDisabled
	}
	if !uuidRE.MatchString(itemID) {
		return nil, invalid("item_id")
	}
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
		// is handed out until an operator revokes one of them (SR-001).
		return nil, s.failItem(ctx, it, store.DeliveryUnsupported, store.ReasonAmbiguousAgent, audit.ModuleDeliveryUnsupported)
	}
	it, changed, err := s.update(ctx, a.TenantID, it.ID, func(cur *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
		if !cur.Active() || cur.AgentID != a.ID || cur.Fetches >= store.MaxModuleFetches {
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
	b, err := s.render(ctx, it)
	s.metrics.source(s.now().Sub(start), sourceOutcome(err))
	switch {
	case errors.Is(err, errSourceRefused):
		return nil, s.failItem(ctx, it, store.DeliveryFailed, store.ReasonSourceRefused, audit.ModuleDeliveryFailed)
	case err != nil && it.Fetches >= store.MaxModuleFetches:
		return nil, s.failItem(ctx, it, store.DeliveryFailed, store.ReasonFetchBudget, audit.ModuleDeliveryFailed)
	case err != nil:
		return nil, ErrSourceUnavailable
	}
	_, changed, err = s.update(ctx, a.TenantID, it.ID, func(cur *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
		if !cur.Active() || cur.AgentID != a.ID {
			return nil, errNoChange
		}
		now := s.now()
		cur.State, cur.UpdatedAt, cur.FetchedAt = store.DeliveryFetched, now, &now
		return []store.AuditRow{s.itemRow(audit.ModuleDeliveryFetched, agentActor(a), audit.OutcomeOK, *cur,
			map[string]any{"fetches": cur.Fetches, "size": len(b.Data), "sha256": b.SHA256})}, nil
	})
	if err != nil || !changed {
		b.Wipe()
		if err == nil {
			s.refuse(ctx, a.TenantID, agentActor(a), itemID, RefusedNotActive)
			err = repo.ErrNotFound
		}
		return nil, err
	}
	return b, nil
}

// failItem ends an active item with state and reason (server side) and
// returns repo.ErrNotFound for the agent: the item is no longer its to
// fetch.
func (s *Service) failItem(ctx context.Context, it store.ModuleDeliveryItem, state, reason string, t audit.EventType) error {
	_, _, err := s.update(ctx, it.TenantID, it.ID, func(cur *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
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
	case errors.Is(err, errBadBundle):
		return "bad_bundle"
	}
	return "unavailable"
}

// sourceErr maps a source failure: NotFound (unknown, expired or mismatched
// join, render budget spent) is a refusal; anything else is retryable. The
// remote message is never propagated.
func sourceErr(err error) error {
	if status.Code(err) == codes.NotFound {
		return errSourceRefused
	}
	return ErrSourceUnavailable
}

// render asks the source for the bundle of it (the verified agent's host)
// and reads the whole stream.
func (s *Service) render(ctx context.Context, it store.ModuleDeliveryItem) (*Bundle, error) {
	if s.cfg.SourceTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.cfg.SourceTimeout)
		defer cancel()
	}
	st, err := s.src.RenderModuleBundle(ctx, &invv1.RenderModuleBundleRequest{TenantId: it.TenantID, DeliveryId: it.DeliveryID,
		HostId: it.HostID, ItemId: it.ID})
	if err != nil {
		return nil, sourceErr(err)
	}
	return readBundle(st, it)
}

// chunkStream is the receiving side of a bundle stream.
type chunkStream interface {
	Recv() (*invv1.ModuleBundleChunk, error)
}

// readBundle reads the header (the item's module, 1 byte..12 MiB, a SHA-256)
// and the contiguous chunks of at most 1 MiB, and checks the total size and
// digest. The buffer is zeroed on every failure.
func readBundle(st chunkStream, it store.ModuleDeliveryItem) (*Bundle, error) {
	first, err := st.Recv()
	if err != nil {
		return nil, sourceErr(err)
	}
	h := first.GetHeader()
	if h == nil || h.GetModule() != it.Module || h.GetSize() < 1 || h.GetSize() > store.MaxModuleBundleBytes || !sha256RE.MatchString(h.GetSha256()) {
		return nil, errBadBundle
	}
	b := &Bundle{ItemID: it.ID, DeliveryID: it.DeliveryID, Module: it.Module, Version: it.Version, SHA256: h.GetSha256(),
		Data: make([]byte, 0, h.GetSize())}
	fail := func(err error) (*Bundle, error) {
		clear(b.Data[:cap(b.Data)])
		return nil, err
	}
	for {
		m, err := st.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fail(sourceErr(err))
		}
		c := m.GetChunk()
		if c == nil || c.GetOffset() != int64(len(b.Data)) || len(c.GetData()) > MaxChunkBytes ||
			int64(len(b.Data)+len(c.GetData())) > h.GetSize() {
			return fail(errBadBundle)
		}
		b.Data = append(b.Data, c.GetData()...)
	}
	sum := sha256.Sum256(b.Data)
	if int64(len(b.Data)) != h.GetSize() || hex.EncodeToString(sum[:]) != h.GetSha256() {
		return fail(errBadBundle)
	}
	return b, nil
}
