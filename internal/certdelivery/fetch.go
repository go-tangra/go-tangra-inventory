package certdelivery

import (
	"context"
	"errors"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Refusal reasons (cert_delivery_refused).
const (
	RefusedNotFound         = "not_found"
	RefusedNotActive        = "not_active"
	RefusedFetchLimit       = "fetch_limit"
	RefusedPlaintext        = "plaintext"
	RefusedSourceNotAllowed = "source_not_allowed"
)

// Material is the bundle served to an agent for one item. It lives only in
// the memory of the FetchCertificate call: the caller sends it and calls
// Wipe; nothing of it is stored, cached, logged or published.
type Material struct {
	ItemID        string
	Name          string
	CertificateID string
	Bundle        certmaterial.Bundle
	IsRenewal     bool // the host had a different certificate under the name
	RerunHook     bool // re-armed after hook_failed

	raw lcmclient.Bundle
}

// Wipe zeroes the private key bytes.
func (m *Material) Wipe() {
	clear(m.Bundle.KeyPEM)
	m.raw.Wipe()
}

func agentActor(a store.Agent) Actor { return Actor{Kind: audit.ActorAgent, ID: a.ID} }

// ownItem returns item id of agent a. Another agent's or tenant's item, a
// missing one, or one that may no longer be fetched (only when active is
// set) is audited as refused and reported as repo.ErrNotFound (no oracle).
func (s *Service) ownItem(ctx context.Context, a store.Agent, id string, active bool) (store.CertDeliveryItem, error) {
	i, err := s.repo.GetCertItem(ctx, a.TenantID, id)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return store.CertDeliveryItem{}, err
	}
	reason := ""
	switch {
	case err != nil || i.AgentID != a.ID:
		reason = RefusedNotFound
	case active && !i.Active():
		reason = RefusedNotActive
	case active && i.Fetches >= store.MaxItemFetches:
		reason = RefusedFetchLimit
	default:
		return i, nil
	}
	s.refuse(ctx, a.TenantID, agentActor(a), id, reason)
	return store.CertDeliveryItem{}, repo.ErrNotFound
}

// RefusePlaintext records a fetch refused because the ingest edge serves
// plaintext without allow_plaintext_ingest.
func (s *Service) RefusePlaintext(ctx context.Context, a store.Agent, itemID string) {
	s.refuse(ctx, a.TenantID, agentActor(a), itemID, RefusedPlaintext)
}

// lcmFailure maps a definitive lcm refusal to the item reason ("" =
// retryable: the item stays as it is).
func lcmFailure(err error) string {
	switch {
	case errors.Is(err, lcmclient.ErrNoKey):
		return store.ReasonKeyUnavailable
	case errors.Is(err, lcmclient.ErrNotFound):
		return store.ReasonCertificateNotFound
	case errors.Is(err, lcmclient.ErrRevoked):
		return store.ReasonCertificateRevoked
	case errors.Is(err, lcmclient.ErrExpired):
		return store.ReasonCertificateExpired
	}
	return ""
}

// Fetch serves item itemID to its agent a (contracts/inventory-grpc.md §2):
// only the agent's own active item with fetches left; the bundle is
// downloaded from lcm (with the key when the key policy requires it),
// validated, recorded as fetched (serial, fingerprint, audit without
// material) and returned. A definitive lcm refusal or unusable material
// fails the item (*ItemFailedError); an unreachable lcm leaves it as it is
// (ErrLCMUnavailable). The caller must Wipe the returned material.
func (s *Service) Fetch(ctx context.Context, a store.Agent, itemID string) (*Material, error) {
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
	d, _, err := s.repo.GetCertDelivery(ctx, a.TenantID, it.DeliveryID)
	if err != nil {
		return nil, err
	}
	includeKey := d.KeyPolicy == store.KeyPolicyRequire
	start := s.now()
	m := &Material{ItemID: it.ID, Name: it.Name, CertificateID: it.CertificateID, RerunHook: it.RerunHook}
	m.raw, err = s.lcm.Download(ctx, a.TenantID, it.CertificateID, includeKey)
	s.metrics.lcm(s.now().Sub(start), lcmOutcome(err))
	if err == nil && includeKey && !m.raw.HasKey() {
		err = lcmclient.ErrNoKey
	}
	if err != nil {
		m.Wipe()
		if reason := lcmFailure(err); reason != "" {
			return nil, s.failItem(ctx, a, it, reason)
		}
		return nil, ErrLCMUnavailable
	}
	var key []byte
	if includeKey {
		key = m.raw.KeyPEM
	}
	m.Bundle, err = certmaterial.ParseBundle([]byte(m.raw.CertPEM), []byte(m.raw.ChainPEM), key,
		certmaterial.Options{RequireKey: includeKey, Now: s.now})
	if err != nil {
		m.Wipe()
		return nil, s.failItem(ctx, a, it, certmaterial.Reason(err))
	}
	prev, err := s.repo.GetHostCertificate(ctx, a.TenantID, it.HostID, it.Name)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		m.Wipe()
		return nil, err
	}
	m.IsRenewal = err == nil && prev.FingerprintSHA256 != "" && prev.FingerprintSHA256 != m.Bundle.Fingerprint
	_, changed, err := s.update(ctx, a.TenantID, it.ID, func(cur *store.CertDeliveryItem) (repo.CertItemChange, error) {
		if !cur.Active() || cur.AgentID != a.ID || cur.Fetches >= store.MaxItemFetches {
			return repo.CertItemChange{}, errNoChange
		}
		now := s.now()
		na := m.Bundle.NotAfter
		cur.State, cur.Fetches, cur.UpdatedAt, cur.FetchedAt = store.DeliveryFetched, cur.Fetches+1, now, &now
		cur.Serial, cur.FingerprintSHA256, cur.NotAfter = m.Bundle.Serial, m.Bundle.Fingerprint, &na
		return repo.CertItemChange{Audit: []store.AuditRow{s.itemRow(audit.CertDeliveryFetched, agentActor(a), audit.OutcomeOK, *cur,
			map[string]any{"has_key": m.Bundle.HasKey, "fetches": cur.Fetches, "serial": cur.Serial,
				"fingerprint_sha256": cur.FingerprintSHA256})}}, nil
	})
	if err != nil || !changed {
		m.Wipe()
		if err == nil {
			s.refuse(ctx, a.TenantID, agentActor(a), itemID, RefusedNotActive)
			err = repo.ErrNotFound
		}
		return nil, err
	}
	return m, nil
}

// failItem fails an active item for reason (server side) and returns the
// *ItemFailedError for the agent.
func (s *Service) failItem(ctx context.Context, a store.Agent, it store.CertDeliveryItem, reason string) error {
	_, changed, err := s.update(ctx, a.TenantID, it.ID, func(cur *store.CertDeliveryItem) (repo.CertItemChange, error) {
		if !cur.Active() {
			return repo.CertItemChange{}, errNoChange
		}
		s.finish(cur, store.DeliveryFailed, reason)
		return repo.CertItemChange{Audit: []store.AuditRow{s.itemRow(audit.CertDeliveryFailed, system, audit.OutcomeError, *cur, nil)}}, nil
	})
	switch {
	case err != nil:
		return err
	case !changed:
		return repo.ErrNotFound
	}
	return &ItemFailedError{Reason: reason}
}
