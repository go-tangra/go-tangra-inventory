package teleportjoin

import (
	"context"
	"errors"
	"log/slog"
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

// Errors. Messages are stable reason strings and never carry join material
// or identifiers of other tenants.
var (
	// ErrDisabled: teleport_join.enabled is false. The message is the
	// capability token the teleport module shows (disabled_on_server).
	ErrDisabled = errors.New("disabled_on_server")
	// ErrSourceUnavailable: the source could not render the material now
	// (unreachable, timed out or malformed); the fetch is counted and the
	// agent retries.
	ErrSourceUnavailable = errors.New("source_unavailable")
)

// InvalidError is a request field failing validation (InvalidArgument).
type InvalidError struct{ Field string }

func (e *InvalidError) Error() string { return "invalid " + e.Field }

func invalid(field string) error { return &InvalidError{Field: field} }

// IneligibleError refuses an enrollment of a host that cannot join now
// (FailedPrecondition). Its message is exactly the reason token (no_agent,
// ambiguous_agent, not_supported_platform, upgrade_required, retired).
type IneligibleError struct{ Reason string }

func (e *IneligibleError) Error() string { return e.Reason }

// Repo is the storage the service needs.
type Repo interface {
	repo.TeleportJoinStore
	GetHost(ctx context.Context, tenantID, id string) (store.Host, error)
	ListHosts(ctx context.Context, tenantID string, f store.HostFilter) ([]store.Host, error)
	ListAgents(ctx context.Context, tenantID string) ([]store.Agent, error)
	AppendAudit(ctx context.Context, row store.AuditRow) error
}

// Registry pushes commands to connected agents and tells who is online.
type Registry interface {
	Deliver(ctx context.Context, agentID string, cmd registry.Command) (bool, error)
	ListConnected(ctx context.Context, tenantID string) ([]registry.ConnectedAgent, error)
	IsOnline(ctx context.Context, agentID string) (bool, error)
}

// Source renders join material (invv1.TeleportJoinSourceClient over the
// mesh).
type Source interface {
	RenderTeleportJoin(ctx context.Context, in *invv1.RenderTeleportJoinRequest, opts ...grpc.CallOption) (*invv1.TeleportJoinMaterial, error)
}

// Metrics observes enrollments: item transitions (state, reason), refused
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

// Config tunes the relay (teleport_join section).
type Config struct {
	Enabled       bool
	ReportTimeout time.Duration // fetched and installing items fail without a report after it
	SourceTimeout time.Duration // bound of one RenderTeleportJoin call
	SourceService string        // the source's mesh service name, for logs
	InstanceID    string        // recorded on teleport_join_delivered rows
}

// Actor is who caused a transition.
type Actor struct {
	Kind string // service | agent | system
	ID   string
}

var system = Actor{Kind: audit.ActorSystem, ID: audit.SystemActor}

// refusalWindow throttles teleport_join_refused rows per actor and reason.
const refusalWindow = 10 * time.Second

// Service is the Teleport join relay.
type Service struct {
	repo    Repo
	src     Source
	reg     Registry
	cfg     Config
	metrics observer
	log     *slog.Logger
	now     func() time.Time
	newID   func() string

	mu      sync.Mutex
	refused map[string]refusal // tenant + actor id + reason -> last audited refusal
}

// New builds the service. src may be nil while the relay is disabled.
func New(r Repo, src Source, reg Registry, cfg Config) *Service {
	return &Service{repo: r, src: src, reg: reg, cfg: cfg, now: utcNow, newID: store.NewID, refused: map[string]refusal{},
		log: slog.New(slog.DiscardHandler)}
}

// SetLogger installs the logger (nil keeps the current one; default: none).
func (s *Service) SetLogger(l *slog.Logger) {
	if l != nil {
		s.log = l
	}
}

func utcNow() time.Time { return time.Now().UTC() }

// SetMetrics installs the observer (nil: none).
func (s *Service) SetMetrics(m Metrics) { s.metrics = observer{m} }

// Enabled reports whether the relay is switched on.
func (s *Service) Enabled() bool { return s.cfg.Enabled }

var (
	uuidRE   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	sourceRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
)

// itemRow is the audit row of an item transition.
func (s *Service) itemRow(t audit.EventType, actor Actor, outcome string, i store.TeleportJoinItem, extra map[string]any) store.AuditRow {
	return audit.TeleportItemRow(t, actor.Kind, actor.ID, outcome, i, extra, s.now())
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
	row := audit.SafeRow(audit.Event{TenantID: tenantID, EventType: audit.TeleportJoinRefused, ActorKind: actor.Kind, ActorID: actor.ID,
		SubjectKind: audit.SubjectTeleportJoin, SubjectID: subjectID, Outcome: audit.OutcomeRefused, Reason: reason,
		Details: details}, now)
	_ = s.repo.AppendAudit(ctx, row)
}

// Refuse records a refused mesh request (source not allowed) for tenantID.
func (s *Service) Refuse(ctx context.Context, tenantID string, actor Actor, reason string) {
	s.refuse(ctx, tenantID, actor, "", reason)
}

// command is the TELEPORT_JOIN command of an item (identifiers only).
func command(i store.TeleportJoinItem) registry.Command {
	return registry.Command{ID: i.ID, Type: registry.CommandTeleportJoin,
		TeleportJoin: &registry.TeleportJoinPayload{ItemID: i.ID, Attempt: i.Attempt}}
}

// errNoChange rolls back an UpdateTeleportItem whose re-check found
// nothing to do.
var errNoChange = errors.New("teleportjoin: no change")

// update applies fn to item id and, when it changed, counts the
// transition. changed is false (nil error) when fn returned errNoChange.
func (s *Service) update(ctx context.Context, tenantID, id string, fn func(*store.TeleportJoinItem) ([]store.AuditRow, error)) (store.TeleportJoinItem, bool, error) {
	i, err := s.repo.UpdateTeleportItem(ctx, tenantID, id, fn)
	if errors.Is(err, errNoChange) {
		return i, false, nil
	}
	if err != nil {
		return i, false, err
	}
	s.metrics.transition(i.State, i.Reason)
	return i, true, nil
}

// finish sets a (terminal) state on cur.
func (s *Service) finish(cur *store.TeleportJoinItem, state, reason string) {
	cur.State, cur.Reason, cur.UpdatedAt = state, reason, s.now()
}

// markDelivered moves a pending item to delivered (audited once).
func (s *Service) markDelivered(ctx context.Context, i store.TeleportJoinItem) {
	_, _, _ = s.update(ctx, i.TenantID, i.ID, func(cur *store.TeleportJoinItem) ([]store.AuditRow, error) {
		if cur.State != store.DeliveryPending {
			return nil, errNoChange
		}
		now := s.now()
		cur.State, cur.UpdatedAt, cur.DeliveredAt = store.DeliveryDelivered, now, &now
		return []store.AuditRow{s.itemRow(audit.TeleportJoinDelivered, system, audit.OutcomeOK, *cur,
			map[string]any{"instance": s.cfg.InstanceID})}, nil
	})
}

// push sends the TELEPORT_JOIN command of a pending item to its online
// agent and records the delivery; a failure leaves it pending (replayed on
// connect).
func (s *Service) push(ctx context.Context, i store.TeleportJoinItem) {
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

// fetchable reports whether an item may still be fetched: pending,
// delivered or fetched (an installing item has its material already).
func fetchable(state string) bool { return store.CertDeliveryActive(state) }
