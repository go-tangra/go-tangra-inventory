package moduledelivery

import (
	"context"
	"errors"
	"regexp"
	"sync"
	"time"

	"google.golang.org/grpc"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Errors. Messages are stable reason strings and never carry bundle
// material or identifiers of other tenants.
var (
	// ErrDisabled: module_delivery.enabled is false. The message is the
	// capability token the gateway shows (disabled_on_server).
	ErrDisabled = errors.New("disabled_on_server")
	// ErrSourceUnavailable: the source could not render the bundle now
	// (unreachable, timed out or a malformed stream); the fetch is counted
	// and the agent retries.
	ErrSourceUnavailable = errors.New("source_unavailable")
)

// InvalidError is a request field failing validation (InvalidArgument).
type InvalidError struct{ Field string }

func (e *InvalidError) Error() string { return "invalid " + e.Field }

func invalid(field string) error { return &InvalidError{Field: field} }

// IneligibleError refuses a delivery to a host that cannot receive it now
// (FailedPrecondition). Its message is exactly the reason token (no_agent,
// ambiguous_agent, not_supported_platform, upgrade_required,
// disabled_on_host, retired), which the gateway maps to its 409 detail.
type IneligibleError struct{ Reason string }

func (e *IneligibleError) Error() string { return e.Reason }

// Repo is the storage the service needs.
type Repo interface {
	repo.ModuleDeliveryStore
	GetHost(ctx context.Context, tenantID, id string) (store.Host, error)
	ListHosts(ctx context.Context, tenantID string, f store.HostFilter) ([]store.Host, error)
	ListAgents(ctx context.Context, tenantID string) ([]store.Agent, error)
	GetLatestForHost(ctx context.Context, tenantID, hostID string) (store.Snapshot, error)
	AppendAudit(ctx context.Context, row store.AuditRow) error
}

// Registry pushes commands to connected agents and tells who is online.
type Registry interface {
	Deliver(ctx context.Context, agentID string, cmd registry.Command) (bool, error)
	ListConnected(ctx context.Context, tenantID string) ([]registry.ConnectedAgent, error)
	IsOnline(ctx context.Context, agentID string) (bool, error)
}

// Source renders bundles (invv1.ModuleBundleSourceClient over the mesh).
type Source interface {
	RenderModuleBundle(ctx context.Context, in *invv1.RenderModuleBundleRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[invv1.ModuleBundleChunk], error)
}

// Metrics observes deliveries: item transitions (state, reason), refused
// requests (reason) and source renders (latency, outcome).
type Metrics interface {
	Transition(state, reason string)
	Refused(reason string)
	Source(d time.Duration, outcome string)
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

func (o observer) source(d time.Duration, outcome string) {
	if o.m != nil {
		o.m.Source(d, outcome)
	}
}

// Config tunes the relay (module_delivery section).
type Config struct {
	Enabled       bool
	ReportTimeout time.Duration // fetched items fail without a report after it
	SourceTimeout time.Duration // bound of one RenderModuleBundle call
	InstanceID    string        // recorded on module_delivery_delivered rows
}

// Actor is who caused a transition.
type Actor struct {
	Kind string // service | agent | system
	ID   string
}

var system = Actor{Kind: audit.ActorSystem, ID: audit.SystemActor}

// refusalWindow throttles module_delivery_refused rows per actor and reason.
const refusalWindow = 10 * time.Second

// Service is the module delivery relay.
type Service struct {
	repo    Repo
	src     Source
	reg     Registry
	cfg     Config
	metrics observer
	now     func() time.Time
	newID   func() string

	mu      sync.Mutex
	refused map[string]refusal // tenant + actor id + reason -> last audited refusal
}

// New builds the service. src may be nil while the relay is disabled.
func New(r Repo, src Source, reg Registry, cfg Config) *Service {
	return &Service{repo: r, src: src, reg: reg, cfg: cfg, now: utcNow, newID: store.NewID, refused: map[string]refusal{}}
}

func utcNow() time.Time { return time.Now().UTC() }

// SetMetrics installs the observer (nil: none).
func (s *Service) SetMetrics(m Metrics) { s.metrics = observer{m} }

// Enabled reports whether the relay is switched on.
func (s *Service) Enabled() bool { return s.cfg.Enabled }

var (
	uuidRE    = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sourceRE  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	moduleRE  = regexp.MustCompile(store.ModuleNameRE)
	versionRE = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
	sha256RE  = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// ValidModule reports whether name is a module name (store.ModuleNameRE).
func ValidModule(name string) bool { return moduleRE.MatchString(name) }

// ValidVersion reports whether v is a semantic version without "v" of at
// most store.MaxModuleVersionLen bytes.
func ValidVersion(v string) bool {
	return len(v) <= store.MaxModuleVersionLen && versionRE.MatchString(v)
}

// itemRow is the audit row of an item transition.
func (s *Service) itemRow(t audit.EventType, actor Actor, outcome string, i store.ModuleDeliveryItem, extra map[string]any) store.AuditRow {
	return audit.ModuleItemRow(t, actor.Kind, actor.ID, outcome, i, extra, s.now())
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
	row := audit.SafeRow(audit.Event{TenantID: tenantID, EventType: audit.ModuleDeliveryRefused, ActorKind: actor.Kind, ActorID: actor.ID,
		SubjectKind: audit.SubjectModuleDelivery, SubjectID: subjectID, Outcome: audit.OutcomeRefused, Reason: reason,
		Details: details}, now)
	_ = s.repo.AppendAudit(ctx, row)
}

// Refuse records a refused mesh request (source not allowed) for tenantID.
func (s *Service) Refuse(ctx context.Context, tenantID string, actor Actor, reason string) {
	s.refuse(ctx, tenantID, actor, "", reason)
}

// command is the MODULE command of an item (identifiers only).
func command(i store.ModuleDeliveryItem) registry.Command {
	return registry.Command{ID: i.ID, Type: registry.CommandModule,
		Module: &registry.ModulePayload{ItemID: i.ID, Module: i.Module, Attempt: i.Attempt}}
}

// errNoChange rolls back an UpdateModuleItem whose re-check found nothing
// to do.
var errNoChange = errors.New("moduledelivery: no change")

// update applies fn to item id and, when it changed, counts the
// transition. changed is false (nil error) when fn returned errNoChange.
func (s *Service) update(ctx context.Context, tenantID, id string, fn func(*store.ModuleDeliveryItem) ([]store.AuditRow, error)) (store.ModuleDeliveryItem, bool, error) {
	i, err := s.repo.UpdateModuleItem(ctx, tenantID, id, fn)
	if errors.Is(err, errNoChange) {
		return i, false, nil
	}
	if err != nil {
		return i, false, err
	}
	s.metrics.transition(i.State, i.Reason)
	return i, true, nil
}

// finish sets a terminal state on cur.
func (s *Service) finish(cur *store.ModuleDeliveryItem, state, reason string) {
	cur.State, cur.Reason, cur.UpdatedAt = state, reason, s.now()
}

// markDelivered moves a pending item to delivered (audited once).
func (s *Service) markDelivered(ctx context.Context, i store.ModuleDeliveryItem) {
	_, _, _ = s.update(ctx, i.TenantID, i.ID, func(cur *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
		if cur.State != store.DeliveryPending {
			return nil, errNoChange
		}
		now := s.now()
		cur.State, cur.UpdatedAt, cur.DeliveredAt = store.DeliveryDelivered, now, &now
		return []store.AuditRow{s.itemRow(audit.ModuleDeliveryDelivered, system, audit.OutcomeOK, *cur,
			map[string]any{"instance": s.cfg.InstanceID})}, nil
	})
}

// push sends the MODULE command of a pending item to its online agent and
// records the delivery; a failure leaves it pending (replayed on connect).
func (s *Service) push(ctx context.Context, i store.ModuleDeliveryItem) {
	if ok, err := s.reg.Deliver(ctx, i.AgentID, command(i)); err == nil && ok {
		s.markDelivered(ctx, i)
	}
}

// online reports whether agentID has a live connection ("" or an error:
// false).
func (s *Service) online(ctx context.Context, agentID string) bool {
	if agentID == "" {
		return false
	}
	on, err := s.reg.IsOnline(ctx, agentID)
	return err == nil && on
}
