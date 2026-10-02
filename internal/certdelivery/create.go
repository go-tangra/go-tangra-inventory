package certdelivery

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Request is a CreateCertificateDelivery call of a mesh source.
type Request struct {
	TenantID        string
	Source          string // SPIFFE service name of the caller
	RequestedBy     string // SPIFFE id of the caller
	IdempotencyKey  string
	ConfigurationID string
	TargetID        string
	Trigger         string
	CertificateID   string
	Name            string
	KeyPolicy       string
	HostIDs         []string
	HostTags        []string
	RearmFailed     bool
}

// ItemView is an item with what the caller shows next to it.
type ItemView struct {
	store.CertDeliveryItem
	Hostname    string
	AgentOnline bool
}

// DeliveryView is a delivery with its items.
type DeliveryView struct {
	store.CertDelivery
	Items          []ItemView
	UnknownHostIDs []string
	Created        bool // false: idempotent replay of an existing delivery
}

var sourceRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

func validRef(s string, min int) bool { return len(s) >= min && len(s) <= 128 }

// checkRequest validates every field of r and returns the deduplicated
// host ids.
func checkRequest(r Request) ([]string, error) {
	switch {
	case !uuidRE.MatchString(r.TenantID):
		return nil, invalid("tenant_id")
	case !sourceRE.MatchString(r.Source):
		return nil, invalid("source")
	case !validRef(r.IdempotencyKey, 1):
		return nil, invalid("idempotency_key")
	case !validRef(r.ConfigurationID, 0):
		return nil, invalid("configuration_id")
	case !validRef(r.TargetID, 0):
		return nil, invalid("target_id")
	case r.Trigger != store.TriggerManual && r.Trigger != store.TriggerAutoDeploy && r.Trigger != store.TriggerRetry:
		return nil, invalid("trigger")
	case !certmaterial.ValidCertificateID(r.CertificateID):
		return nil, invalid("certificate_id")
	case !certmaterial.ValidName(r.Name):
		return nil, invalid("name")
	case r.KeyPolicy != store.KeyPolicyRequire && r.KeyPolicy != store.KeyPolicyCertificateOnly:
		return nil, invalid("key_policy")
	}
	return checkSelector(r.HostIDs, r.HostTags)
}

// metadata asks lcm for the certificate's identity (never its key).
func (s *Service) metadata(ctx context.Context, tenantID, certID string) (lcmclient.Bundle, error) {
	start := s.now()
	b, err := s.lcm.Download(ctx, tenantID, certID, false)
	b.Wipe()
	s.metrics.lcm(s.now().Sub(start), lcmOutcome(err))
	switch {
	case err == nil:
		return b, nil
	case errors.Is(err, lcmclient.ErrNotFound):
		return b, ErrCertificateNotFound
	case errors.Is(err, lcmclient.ErrRevoked):
		return b, ErrCertificateRevoked
	case errors.Is(err, lcmclient.ErrExpired):
		return b, ErrCertificateExpired
	}
	return b, ErrLCMUnavailable
}

// lcmOutcome names a Download result for the metrics.
func lcmOutcome(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, lcmclient.ErrNotFound):
		return "not_found"
	case errors.Is(err, lcmclient.ErrNoKey):
		return "no_key"
	case errors.Is(err, lcmclient.ErrRevoked):
		return "revoked"
	case errors.Is(err, lcmclient.ErrExpired):
		return "expired"
	}
	return "unavailable"
}

// unsupportedReason is why t cannot receive a certificate now ("" = it can).
// An agent known to lack cert.v1 counts only while it is online: an offline
// one may have been upgraded and is judged when it connects.
func unsupportedReason(t target) string {
	switch {
	case t.host.Status == store.HostRetired:
		return store.ReasonHostRetired
	case t.agent == nil:
		return store.ReasonNoAgent
	case t.ambiguous:
		return store.ReasonAmbiguousAgent
	case t.agent.OS == "windows":
		return store.ReasonPlatform
	case t.online && !t.agent.HasCapability(store.CapCertV1):
		return store.ReasonNoCapability
	}
	return ""
}

// Create creates a delivery (contracts/inventory-grpc.md §1): validates the
// request, replays an existing delivery of the same idempotency key
// (re-arming failed items when asked), resolves the hosts, checks the
// certificate with lcm, stores the delivery with one item per host and the
// audit rows in one transaction (older active items of the same host and
// name are superseded), then pushes the command to online agents.
func (s *Service) Create(ctx context.Context, r Request) (DeliveryView, error) {
	if !s.cfg.Enabled {
		return DeliveryView{}, ErrDisabled
	}
	ids, err := checkRequest(r)
	if err != nil {
		return DeliveryView{}, err
	}
	actor := Actor{Kind: audit.ActorService, ID: r.Source}
	if v, done, err := s.replayByKey(ctx, r, actor); done {
		return v, err
	}
	sel, err := s.resolve(ctx, r.TenantID, ids, r.HostTags)
	if err != nil {
		return DeliveryView{}, err
	}
	if len(sel.targets) > store.MaxDeliveryHosts {
		return DeliveryView{}, ErrTooManyHosts
	}
	meta, err := s.metadata(ctx, r.TenantID, r.CertificateID)
	if err != nil {
		return DeliveryView{}, err
	}
	installed := map[string]store.HostCertificate{}
	if r.Trigger != store.TriggerManual {
		hcs, err := s.repo.ListHostCertificatesByName(ctx, r.TenantID, r.Name)
		if err != nil {
			return DeliveryView{}, err
		}
		for _, hc := range hcs {
			installed[hc.HostID] = hc
		}
	}
	for attempt := 0; ; attempt++ {
		n := s.build(r, actor, sel, meta, installed)
		superseded, err := s.repo.CreateCertDelivery(ctx, n)
		if errors.Is(err, repo.ErrConflict) && attempt == 0 {
			// The same key was created concurrently (replay it), or another
			// delivery made an active item for one of the hosts and names
			// first (build again: it is superseded now).
			if v, done, err := s.replayByKey(ctx, r, actor); done {
				return v, err
			}
			continue
		}
		if err != nil {
			return DeliveryView{}, err
		}
		for _, i := range superseded {
			s.committed(ctx, i)
		}
		for _, i := range n.Items {
			s.committed(ctx, i)
		}
		online := map[string]bool{}
		for _, t := range sel.targets {
			if t.online {
				online[t.agent.ID] = true
			}
		}
		s.push(ctx, n.Items, online)
		v, err := s.Get(ctx, r.TenantID, n.Delivery.ID)
		v.Created, v.UnknownHostIDs = true, sel.unknown
		return v, err
	}
}

// replayByKey returns the existing delivery of r's idempotency key (done),
// re-armed when r asks for it.
func (s *Service) replayByKey(ctx context.Context, r Request, actor Actor) (DeliveryView, bool, error) {
	d, err := s.repo.GetCertDeliveryByKey(ctx, r.TenantID, r.Source, r.IdempotencyKey)
	if errors.Is(err, repo.ErrNotFound) {
		return DeliveryView{}, false, nil
	}
	if err != nil {
		return DeliveryView{}, true, err
	}
	if d.CertificateID != r.CertificateID || d.Name != r.Name || d.KeyPolicy != r.KeyPolicy {
		// The key was used for another payload: never answer with (or
		// re-arm) a delivery of a different certificate or name.
		return DeliveryView{}, true, invalid("idempotency_key")
	}
	if r.RearmFailed {
		if err := s.rearm(ctx, d, actor); err != nil {
			return DeliveryView{}, true, err
		}
	}
	v, err := s.Get(ctx, r.TenantID, d.ID)
	return v, true, err
}

// build assembles the delivery, its items and audit rows.
func (s *Service) build(r Request, actor Actor, sel selection, meta lcmclient.Bundle, installed map[string]store.HostCertificate) repo.NewCertDelivery {
	now := s.now()
	d := store.CertDelivery{ID: s.newID(), TenantID: r.TenantID, Source: r.Source, IdempotencyKey: r.IdempotencyKey,
		ConfigurationID: r.ConfigurationID, TargetID: r.TargetID, Trigger: r.Trigger, CertificateID: r.CertificateID, Name: r.Name,
		KeyPolicy: r.KeyPolicy, HostIDs: append([]string{}, r.HostIDs...), HostTags: append([]string{}, r.HostTags...),
		RequestedBy: r.RequestedBy, CreatedAt: now, ExpiresAt: expiry(now, s.cfg.PendingTTL, meta.NotAfter)}
	n := repo.NewCertDelivery{Delivery: d, SupersedeAudit: func(old store.CertDeliveryItem, newID string) store.AuditRow {
		return s.itemRow(audit.CertDeliverySuperseded, system, audit.OutcomeOK, old, map[string]any{"superseded_by": newID})
	}}
	n.Audit = append(n.Audit, audit.SafeRow(audit.Event{TenantID: d.TenantID, EventType: audit.CertDeliveryRequested,
		ActorKind: actor.Kind, ActorID: actor.ID, SubjectKind: audit.SubjectCertDelivery, SubjectID: d.ID, Outcome: audit.OutcomeOK,
		Details: map[string]any{"delivery_id": d.ID, "certificate_id": d.CertificateID, "name": d.Name, "source": d.Source,
			"idempotency_key": d.IdempotencyKey, "configuration_id": d.ConfigurationID, "trigger": d.Trigger,
			"hosts": len(sel.targets), "unknown_hosts": len(sel.unknown), "key_policy": d.KeyPolicy, "created": true}}, now))
	var notAfter *time.Time
	if !meta.NotAfter.IsZero() {
		na := meta.NotAfter
		notAfter = &na
	}
	for _, t := range sel.targets {
		i := store.CertDeliveryItem{ID: s.newID(), TenantID: d.TenantID, DeliveryID: d.ID, HostID: t.host.ID, Name: d.Name,
			CertificateID: d.CertificateID, State: store.DeliveryPending, Attempts: 1, NotAfter: notAfter, CreatedAt: now, UpdatedAt: now}
		if t.agent != nil {
			i.AgentID = t.agent.ID
		}
		hc, has := installed[t.host.ID]
		switch reason := unsupportedReason(t); {
		case reason != "":
			s.finish(&i, store.DeliveryUnsupported, reason)
			n.Audit = append(n.Audit, s.itemRow(audit.CertDeliveryUnsupported, system, audit.OutcomeRefused, i, nil))
		case has && hc.RevokedAt == nil && hc.NotAfter != nil && notAfter != nil && hc.NotAfter.After(*notAfter):
			// Automatic deployments never replace a later-expiring certificate.
			s.finish(&i, store.DeliverySuperseded, store.ReasonOlderThanInstalled)
			n.Audit = append(n.Audit, s.itemRow(audit.CertDeliverySuperseded, system, audit.OutcomeOK, i, nil))
		}
		n.Items = append(n.Items, i)
	}
	return n
}

// expiry is min(now + ttl, notAfter); a zero notAfter is ignored.
func expiry(now time.Time, ttl time.Duration, notAfter time.Time) time.Time {
	e := now.Add(ttl)
	if !notAfter.IsZero() && notAfter.Before(e) {
		return notAfter
	}
	return e
}

// Get returns a delivery of the tenant with its items.
func (s *Service) Get(ctx context.Context, tenantID, id string) (DeliveryView, error) {
	if !s.cfg.Enabled {
		return DeliveryView{}, ErrDisabled
	}
	if !uuidRE.MatchString(id) {
		return DeliveryView{}, repo.ErrNotFound
	}
	d, items, err := s.repo.GetCertDelivery(ctx, tenantID, id)
	if err != nil {
		return DeliveryView{}, err
	}
	hosts, err := s.repo.ListHosts(ctx, tenantID, store.HostFilter{})
	if err != nil {
		return DeliveryView{}, err
	}
	online, err := s.online(ctx, tenantID)
	if err != nil {
		return DeliveryView{}, err
	}
	names := make(map[string]string, len(hosts))
	for _, h := range hosts {
		names[h.ID] = h.Hostname
	}
	v := DeliveryView{CertDelivery: d, Items: make([]ItemView, 0, len(items))}
	inItems := map[string]bool{}
	for _, i := range items {
		inItems[i.HostID] = true
		v.Items = append(v.Items, ItemView{CertDeliveryItem: i, Hostname: names[i.HostID], AgentOnline: i.AgentID != "" && online[i.AgentID]})
	}
	for _, id := range d.HostIDs {
		if !inItems[id] {
			v.UnknownHostIDs = append(v.UnknownHostIDs, id)
		}
	}
	return v, nil
}

// rearmable states are re-armed by a retry of the same delivery.
func rearmable(state string) bool {
	return state == store.DeliveryFailed || state == store.DeliveryHookFailed || state == store.DeliveryExpired
}

// rearm moves the delivery's failed, hook_failed and expired items with
// attempts left back to pending (a fresh delivery window, the hook re-run
// when it failed) and pushes them; done and queued items are untouched. An
// item whose host meanwhile got another active item for the name stays.
func (s *Service) rearm(ctx context.Context, d store.CertDelivery, actor Actor) error {
	_, items, err := s.repo.GetCertDelivery(ctx, d.TenantID, d.ID)
	if err != nil {
		return err
	}
	var rearmed []store.CertDeliveryItem
	extended := false
	for _, it := range items {
		if !rearmable(it.State) || it.Attempts >= store.MaxItemAttempts {
			continue
		}
		if !extended {
			var na time.Time
			if it.NotAfter != nil {
				na = *it.NotAfter
			}
			if err := s.repo.ExtendCertDelivery(ctx, d.TenantID, d.ID, expiry(s.now(), s.cfg.PendingTTL, na)); err != nil {
				return err
			}
			extended = true
		}
		i, changed, err := s.update(ctx, d.TenantID, it.ID, func(cur *store.CertDeliveryItem) (repo.CertItemChange, error) {
			if !rearmable(cur.State) || cur.Attempts >= store.MaxItemAttempts {
				return repo.CertItemChange{}, errNoChange
			}
			now := s.now()
			cur.RerunHook = cur.State == store.DeliveryHookFailed
			cur.State, cur.Reason, cur.Attempts, cur.Fetches = store.DeliveryPending, "", cur.Attempts+1, 0
			cur.FingerprintSHA256, cur.HookExitCode, cur.Detail = "", nil, ""
			cur.UpdatedAt, cur.DeliveredAt, cur.FetchedAt, cur.FinishedAt = now, nil, nil, nil
			return repo.CertItemChange{Audit: []store.AuditRow{s.itemRow(audit.CertDeliveryRearmed, actor, audit.OutcomeOK, *cur,
				map[string]any{"attempts": cur.Attempts})}}, nil
		})
		if errors.Is(err, repo.ErrConflict) {
			continue
		}
		if err != nil {
			return err
		}
		if changed {
			rearmed = append(rearmed, i)
		}
	}
	if len(rearmed) == 0 {
		return nil
	}
	online, err := s.online(ctx, d.TenantID)
	if err != nil {
		return nil // pushed when the agents connect
	}
	s.push(ctx, rearmed, online)
	return nil
}
