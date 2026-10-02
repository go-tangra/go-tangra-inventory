package certdelivery

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Errors. Messages are stable reason strings and never carry material or
// identifiers of other tenants.
var (
	// ErrDisabled: cert_delivery.enabled is false.
	ErrDisabled = errors.New("certificate delivery is disabled")
	// ErrCertificateNotFound / Revoked / Expired: lcm refuses the
	// certificate of a new delivery.
	ErrCertificateNotFound = errors.New("certificate_not_found")
	ErrCertificateRevoked  = errors.New("certificate_revoked")
	ErrCertificateExpired  = errors.New("certificate_expired")
	// ErrLCMUnavailable: lcm could not be asked (retryable; the item is
	// unchanged).
	ErrLCMUnavailable = errors.New("lcm_unavailable")
	// ErrNotCancellable: the item is no longer active.
	ErrNotCancellable = errors.New("not_cancellable")
	// ErrTooManyHosts: the selection resolves to more than
	// store.MaxDeliveryHosts hosts.
	ErrTooManyHosts = &InvalidError{Field: "selector: too_many_hosts"}
)

// InvalidError is a request field failing validation (InvalidArgument).
type InvalidError struct{ Field string }

func (e *InvalidError) Error() string { return "invalid " + e.Field }

func invalid(field string) error { return &InvalidError{Field: field} }

// ItemFailedError reports that the server failed the item while serving a
// fetch (lcm refused the certificate or the material is unusable); Reason
// is the item's closed reason code.
type ItemFailedError struct{ Reason string }

func (e *ItemFailedError) Error() string { return "certificate delivery failed: " + e.Reason }

// Repo is the storage the service needs.
type Repo interface {
	repo.CertDeliveryStore
	ListHosts(ctx context.Context, tenantID string, f store.HostFilter) ([]store.Host, error)
	ListAgents(ctx context.Context, tenantID string) ([]store.Agent, error)
	AppendAudit(ctx context.Context, row store.AuditRow) error
}

// LCM downloads certificates (internal/lcmclient).
type LCM interface {
	Download(ctx context.Context, tenantID, certID string, includeKey bool) (lcmclient.Bundle, error)
}

// Registry pushes commands to connected agents and lists them.
type Registry interface {
	Deliver(ctx context.Context, agentID string, cmd registry.Command) (bool, error)
	ListConnected(ctx context.Context, tenantID string) ([]registry.ConnectedAgent, error)
}

// Publisher emits content-free realtime events.
type Publisher interface {
	Publish(ctx context.Context, tenantID, eventType string, payload any)
}

// Metrics observes deliveries: item transitions (by state and reason),
// refused agent or service requests (by reason) and lcm downloads (latency
// and outcome).
type Metrics interface {
	Transition(state, reason string)
	Refused(reason string)
	LCM(d time.Duration, outcome string)
}

// observer forwards to the installed Metrics (none: nothing).
type observer struct{ m Metrics }

func (o observer) transition(state, reason string) {
	if o.m != nil {
		o.m.Transition(state, reason)
	}
}

func (o observer) refused(reason string) {
	if o.m != nil {
		o.m.Refused(reason)
	}
}

func (o observer) lcm(d time.Duration, outcome string) {
	if o.m != nil {
		o.m.LCM(d, outcome)
	}
}

// Config tunes the relay (cert_delivery section).
type Config struct {
	Enabled       bool
	PendingTTL    time.Duration // items wait at most this long for their agent
	ReportTimeout time.Duration // fetched items fail without a report after it
	InstanceID    string        // recorded on cert_delivery_delivered rows
}

// Actor is who caused a transition.
type Actor struct {
	Kind string // service | user | agent | system
	ID   string
}

var system = Actor{Kind: audit.ActorSystem, ID: audit.SystemActor}

// refusalWindow throttles cert_delivery_refused rows per actor and reason.
const refusalWindow = 10 * time.Second

// Service is the certificate delivery relay.
type Service struct {
	repo    Repo
	lcm     LCM
	reg     Registry
	pub     Publisher
	cfg     Config
	metrics observer
	now     func() time.Time
	newID   func() string

	mu      sync.Mutex
	refused map[string]refusal // tenant + actor id + reason -> last audited refusal
}

// New builds the service. lcm may be nil while the relay is disabled.
func New(r Repo, lcm LCM, reg Registry, pub Publisher, cfg Config) *Service {
	return &Service{repo: r, lcm: lcm, reg: reg, pub: pub, cfg: cfg, now: utcNow, newID: store.NewID,
		refused: map[string]refusal{}}
}

func utcNow() time.Time { return time.Now().UTC() }

// SetMetrics installs the observer (nil: none).
func (s *Service) SetMetrics(m Metrics) { s.metrics = observer{m} }

// Enabled reports whether the relay is switched on.
func (s *Service) Enabled() bool { return s.cfg.Enabled }

var (
	uuidRE        = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	fingerprintRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
	serialRE      = regexp.MustCompile(`^[0-9a-fA-F:]{0,128}$`)
)

// itemRow is the audit row of an item transition.
func (s *Service) itemRow(t audit.EventType, actor Actor, outcome string, i store.CertDeliveryItem, extra map[string]any) store.AuditRow {
	return audit.CertItemRow(t, actor.Kind, actor.ID, outcome, i, extra, s.now())
}

// committed counts a committed transition and publishes its event.
func (s *Service) committed(ctx context.Context, i store.CertDeliveryItem) {
	s.metrics.transition(i.State, i.Reason)
	s.pub.Publish(ctx, i.TenantID, events.CertificateDelivery, events.CertificateDeliveryPayload(i.HostID, i.ID, i.State))
}

// refusal is the throttle state of one actor and reason.
type refusal struct {
	at         time.Time // last audited refusal
	suppressed int       // refusals since then that were not audited
}

// refuse records a refused request: counted always, audited at most once
// per actor and reason within refusalWindow; the next audited row carries
// the number of refusals suppressed in between. A subject id that is not a
// uuid (attacker-chosen text) is not recorded.
func (s *Service) refuse(ctx context.Context, tenantID string, actor Actor, subjectID, reason string) {
	s.metrics.refused(reason)
	now := s.now()
	key := tenantID + "\x00" + actor.ID + "\x00" + reason
	s.mu.Lock()
	last, seen := s.refused[key]
	if seen && now.Sub(last.at) < refusalWindow {
		last.suppressed++
		s.refused[key] = last
		s.mu.Unlock()
		return
	}
	s.refused[key] = refusal{at: now}
	for k, r := range s.refused { // keep the map small
		// A suppressed count is kept a while for the next row; the map
		// stays bounded by the actors refused within that time.
		if age := now.Sub(r.at); age >= refusalWindow && (r.suppressed == 0 || age >= 6*refusalWindow) {
			delete(s.refused, k)
		}
	}
	s.mu.Unlock()
	if !uuidRE.MatchString(subjectID) {
		subjectID = ""
	}
	details := map[string]any{"reason": reason}
	if last.suppressed > 0 {
		details["suppressed"] = last.suppressed
	}
	row := audit.SafeRow(audit.Event{TenantID: tenantID, EventType: audit.CertDeliveryRefused, ActorKind: actor.Kind, ActorID: actor.ID,
		SubjectKind: audit.SubjectCertDelivery, SubjectID: subjectID, Outcome: audit.OutcomeRefused, Reason: reason,
		Details: details}, now)
	_ = s.repo.AppendAudit(ctx, row)
}

// Refuse records a refused mesh request (source not allowed) for tenantID.
func (s *Service) Refuse(ctx context.Context, tenantID string, actor Actor, reason string) {
	s.refuse(ctx, tenantID, actor, "", reason)
}

// command is the CERTIFICATE command of an item (identifiers only).
func command(i store.CertDeliveryItem) registry.Command {
	return registry.Command{ID: i.ID, Type: registry.CommandCertificate,
		Certificate: &registry.CertificatePayload{ItemID: i.ID, Name: i.Name, Attempt: i.Attempts}}
}

// errNoChange rolls back an UpdateCertItem whose re-check found nothing to do.
var errNoChange = errors.New("certdelivery: no change")

// update applies fn to item id and, when it changed, records the commit.
// changed is false (nil error) when fn returned errNoChange.
func (s *Service) update(ctx context.Context, tenantID, id string, fn func(*store.CertDeliveryItem) (repo.CertItemChange, error)) (store.CertDeliveryItem, bool, error) {
	i, err := s.repo.UpdateCertItem(ctx, tenantID, id, fn)
	if errors.Is(err, errNoChange) {
		return i, false, nil
	}
	if err != nil {
		return i, false, err
	}
	s.committed(ctx, i)
	return i, true, nil
}

// finish sets a terminal state on cur.
func (s *Service) finish(cur *store.CertDeliveryItem, state, reason string) {
	now := s.now()
	cur.State, cur.Reason, cur.UpdatedAt, cur.FinishedAt = state, reason, now, &now
}

// online returns the set of the tenant's connected agent ids.
func (s *Service) online(ctx context.Context, tenantID string) (map[string]bool, error) {
	conns, err := s.reg.ListConnected(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(conns))
	for _, c := range conns {
		out[c.AgentID] = true
	}
	return out, nil
}

// push sends the CERTIFICATE command of every pending item whose agent is
// online and records the delivery; failures leave the item pending (it is
// replayed when the agent connects).
func (s *Service) push(ctx context.Context, items []store.CertDeliveryItem, online map[string]bool) {
	for _, i := range items {
		if i.State != store.DeliveryPending || !online[i.AgentID] {
			continue
		}
		if ok, err := s.reg.Deliver(ctx, i.AgentID, command(i)); err == nil && ok {
			_, _, _ = s.markDelivered(ctx, i)
		}
	}
}

// markDelivered moves a pending item to delivered (audited once).
func (s *Service) markDelivered(ctx context.Context, i store.CertDeliveryItem) (store.CertDeliveryItem, bool, error) {
	return s.update(ctx, i.TenantID, i.ID, func(cur *store.CertDeliveryItem) (repo.CertItemChange, error) {
		if cur.State != store.DeliveryPending {
			return repo.CertItemChange{}, errNoChange
		}
		now := s.now()
		cur.State, cur.UpdatedAt, cur.DeliveredAt = store.DeliveryDelivered, now, &now
		return repo.CertItemChange{Audit: []store.AuditRow{s.itemRow(audit.CertDeliveryDelivered, system, audit.OutcomeOK, *cur,
			map[string]any{"instance": s.cfg.InstanceID})}}, nil
	})
}
