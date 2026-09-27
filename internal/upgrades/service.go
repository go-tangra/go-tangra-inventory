package upgrades

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// MaxBatch bounds one upgrade request (explicit agent ids).
const MaxBatch = 1000

// Errors.
var (
	ErrInvalid        = errors.New("upgrades: invalid request")
	ErrNoRelease      = errors.New("upgrades: no agent release available")
	ErrNotCancellable = errors.New("upgrades: request is not cancellable")
	ErrNotActive      = errors.New("upgrades: request is not active")
	ErrUnknownVersion = errors.New("upgrades: version is not a stored release")
)

// Skip reasons of a batch request (contracts/inventory-http.md).
const (
	SkipUpToDate  = "up_to_date"
	SkipActive    = "upgrade_active"
	SkipManual    = "manual_upgrade_required"
	SkipUnsup     = "unsupported"
	SkipNoRelease = "no_release_for_platform"
	SkipNotFound  = "not_found"
)

// Report states an agent sends.
const (
	StateDownloading = store.UpgradeDownloading
	StateInstalling  = store.UpgradeInstalling
	StateSucceeded   = store.UpgradeSucceeded
	StateFailed      = store.UpgradeFailed
	StateRolledBack  = store.UpgradeRolledBack
)

// Reasons the server sets itself.
const (
	ReasonStartTimeout       = "start_timeout"
	ReasonExpired            = "expired"
	ReasonUnsupportedInstall = "unsupported_install"
)

// agentReasons is the closed set of reasons an agent may report.
var agentReasons = map[string]bool{
	"signature_invalid": true, "unknown_key": true, "checksum_mismatch": true, "size_mismatch": true, "platform_mismatch": true,
	"version_mismatch": true, "downgrade_refused": true, "disk_full": true, "download_failed": true, "install_failed": true,
	ReasonStartTimeout: true, ReasonUnsupportedInstall: true, "busy": true, "package_db_mismatch": true, "cancelled_locally": true,
}

// ValidReason reports whether r is an agent reason code.
func ValidReason(r string) bool { return agentReasons[r] }

var capabilityRE = regexp.MustCompile(`^[a-z0-9.]{1,32}$`)

// ValidCapability reports whether c is a well-formed capability token.
func ValidCapability(c string) bool { return capabilityRE.MatchString(c) }

// Actor kinds of a request.
const (
	ActorUser   = audit.ActorUser
	ActorSystem = audit.ActorSystem
	ActorAgent  = audit.ActorAgent
)

// Actor is who asked for an upgrade.
type Actor struct {
	Kind string // user | system | agent
	ID   string
}

// Repo is the storage the service needs.
type Repo interface {
	repo.UpgradeStore
	GetAgent(ctx context.Context, tenantID, id string) (store.Agent, error)
	ListHosts(ctx context.Context, tenantID string, f store.HostFilter) ([]store.Host, error)
	AppendAudit(ctx context.Context, row store.AuditRow) error
}

// Releases is what the service needs to know about stored releases.
type Releases interface {
	CurrentVersion(ctx context.Context) string
	HasArtifact(ctx context.Context, version string, p agentrelease.Platform) bool
	Releases(ctx context.Context) ([]store.AgentRelease, error)
}

// Registry delivers commands to connected agents and lists them.
type Registry interface {
	Deliver(ctx context.Context, agentID string, cmd registry.Command) (bool, error)
	ListConnected(ctx context.Context, tenantID string) ([]registry.ConnectedAgent, error)
}

// Publisher emits content-free realtime events.
type Publisher interface {
	Publish(ctx context.Context, tenantID, eventType string, payload any)
}

// Realtime event types.
const (
	EventUpgrade = "inventory.agent.upgrade"
	EventPolicy  = "inventory.agent.upgrade_policy"
)

// Metrics counts upgrade transitions (by state and reason).
type Metrics interface {
	Transition(state, reason string)
}

// Config tunes the lifecycle.
type Config struct {
	RequestTTL      time.Duration // pending/delivered requests expire after it
	ProgressTimeout time.Duration // downloading/installing requests fail without a report
}

// Service is the upgrade request lifecycle.
type Service struct {
	repo     Repo
	rel      Releases
	reg      Registry
	pub      Publisher
	cfg      Config
	metrics  Metrics
	finished func(context.Context, store.AgentUpgrade)
	now      func() time.Time
	newID    func() string
}

// New builds the service.
func New(r Repo, rel Releases, reg Registry, pub Publisher, cfg Config) *Service {
	return &Service{repo: r, rel: rel, reg: reg, pub: pub, cfg: cfg, now: utcNow, newID: store.NewID}
}

func utcNow() time.Time { return time.Now().UTC() }

// SetMetrics installs the transition counter (nil = none).
func (s *Service) SetMetrics(m Metrics) { s.metrics = m }

// OnFinished registers a hook run after a request reaches failed or
// rolled_back (the automatic upgrade policy pauses on those).
func (s *Service) OnFinished(fn func(context.Context, store.AgentUpgrade)) { s.finished = fn }

// Target is the version the tenant's agents should run: the policy pin or
// the platform current version.
func (s *Service) Target(ctx context.Context, tenantID string) (version string, pinned bool, err error) {
	p, _, err := s.repo.GetUpgradePolicy(ctx, tenantID)
	if err != nil {
		return "", false, err
	}
	if p.TargetVersion != "" {
		return p.TargetVersion, true, nil
	}
	return s.rel.CurrentVersion(ctx), false, nil
}

func platformOf(a store.Agent) agentrelease.Platform {
	return agentrelease.Platform{OS: a.OS, Arch: a.Arch, InstallType: a.InstallType}
}

// Skip is an agent a batch request left out, with the reason.
type Skip struct {
	AgentID string `json:"agent_id"`
	Reason  string `json:"reason"`
}

// BatchResult is the outcome of a batch request.
type BatchResult struct {
	TargetVersion string               `json:"target_version"`
	Created       []store.AgentUpgrade `json:"created"`
	Skipped       []Skip               `json:"skipped"`
}

// Request creates upgrade requests for the given agents or (allOutdated)
// every outdated, upgrade-capable agent of the tenant, to the tenant target
// (a pin lower than an agent's version allows that downgrade: setting a pin
// needs agentupgrades:manage). Each request is audited and delivered to
// online agents at once; offline agents get it when they connect.
func (s *Service) Request(ctx context.Context, tenantID string, actor Actor, agentIDs []string, allOutdated bool) (BatchResult, error) {
	return s.request(ctx, tenantID, actor, store.OriginUser, agentIDs, allOutdated)
}

func (s *Service) request(ctx context.Context, tenantID string, actor Actor, origin string, agentIDs []string, allOutdated bool) (BatchResult, error) {
	if (len(agentIDs) == 0) == !allOutdated || len(agentIDs) > MaxBatch {
		return BatchResult{}, fmt.Errorf("%w: give 1-%d agent ids or all_outdated", ErrInvalid, MaxBatch)
	}
	target, pinned, err := s.Target(ctx, tenantID)
	if err != nil {
		return BatchResult{}, err
	}
	if target == "" {
		return BatchResult{}, ErrNoRelease
	}
	agents, err := s.repo.ListAgents(ctx, tenantID)
	if err != nil {
		return BatchResult{}, err
	}
	latest, err := s.repo.LatestAgentUpgrades(ctx, tenantID)
	if err != nil {
		return BatchResult{}, err
	}
	byID := map[string]store.Agent{}
	for _, a := range agents {
		byID[a.ID] = a
	}
	res := BatchResult{TargetVersion: target, Created: []store.AgentUpgrade{}, Skipped: []Skip{}}
	if allOutdated {
		for _, a := range agents {
			agentIDs = append(agentIDs, a.ID)
		}
	}
	for _, id := range agentIDs {
		a, ok := byID[id]
		if !ok {
			res.Skipped = append(res.Skipped, Skip{id, SkipNotFound})
			continue
		}
		allowDowngrade, reason := s.eligible(ctx, a, latest[id], target, pinned, allOutdated)
		if reason != "" {
			res.Skipped = append(res.Skipped, Skip{id, reason})
			continue
		}
		u, err := s.create(ctx, a, actor, origin, target, allowDowngrade)
		if errors.Is(err, repo.ErrConflict) {
			res.Skipped = append(res.Skipped, Skip{id, SkipActive})
			continue
		}
		if err != nil {
			return res, err
		}
		res.Created = append(res.Created, s.deliver(ctx, u))
	}
	return res, nil
}

// eligible decides whether agent a gets a request for target.
func (s *Service) eligible(ctx context.Context, a store.Agent, last store.AgentUpgrade, target string, pinned, automatic bool) (allowDowngrade bool, skip string) {
	switch {
	case last.Active():
		return false, SkipActive
	case !a.HasCapability(store.CapUpgradeV1):
		return false, SkipManual
	case !platformOf(a).Valid():
		return false, SkipUnsup
	case last.State == store.UpgradeFailed && last.Reason == ReasonUnsupportedInstall:
		return false, SkipUnsup
	}
	c, ok := agentrelease.Compare(a.AgentVersion, target)
	switch {
	case ok && c == 0:
		return false, SkipUpToDate
	case ok && c > 0 && !pinned:
		return false, SkipUpToDate
	case !ok && automatic:
		return false, SkipUpToDate // dev or unparsable builds are never outdated automatically
	case !s.rel.HasArtifact(ctx, target, platformOf(a)):
		return false, SkipNoRelease
	}
	return ok && c > 0, ""
}

// create inserts a pending request with its audit row.
func (s *Service) create(ctx context.Context, a store.Agent, actor Actor, origin, target string, allowDowngrade bool) (store.AgentUpgrade, error) {
	now := s.now()
	u := store.AgentUpgrade{ID: s.newID(), TenantID: a.TenantID, AgentID: a.ID, HostID: a.HostID, FromVersion: a.AgentVersion,
		TargetVersion: target, AllowDowngrade: allowDowngrade, State: store.UpgradePending, Origin: origin, RequestedBy: actor.ID,
		CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(s.cfg.RequestTTL)}
	row, err := s.row(u, audit.AgentUpgradeRequested, actor, audit.OutcomeOK, "", map[string]any{"allow_downgrade": allowDowngrade})
	if err != nil {
		return store.AgentUpgrade{}, err
	}
	if err := s.repo.CreateAgentUpgrade(ctx, u, row); err != nil {
		return store.AgentUpgrade{}, err
	}
	s.committed(ctx, u)
	return u, nil
}

// row builds the transactional audit row of a transition.
func (s *Service) row(u store.AgentUpgrade, t audit.EventType, actor Actor, outcome, reason string, extra map[string]any) (store.AuditRow, error) {
	d := map[string]any{"agent_id": u.AgentID, "host_id": u.HostID, "from_version": u.FromVersion, "to_version": u.TargetVersion,
		"request_id": u.ID, "origin": u.Origin}
	for k, v := range extra {
		d[k] = v
	}
	return audit.Row(audit.Event{TenantID: u.TenantID, EventType: t, ActorKind: actor.Kind, ActorID: actor.ID,
		SubjectKind: audit.SubjectAgent, SubjectID: u.AgentID, Outcome: outcome, Reason: reason, Details: d}, s.now())
}

// committed publishes the realtime event and counts the transition.
func (s *Service) committed(ctx context.Context, u store.AgentUpgrade) {
	if s.metrics != nil {
		s.metrics.Transition(u.State, u.Reason)
	}
	s.pub.Publish(ctx, u.TenantID, EventUpgrade, map[string]any{"agent_id": u.AgentID, "upgrade_id": u.ID, "state": u.State})
}

// command is the UPGRADE command of a request.
func command(u store.AgentUpgrade) registry.Command {
	return registry.Command{ID: u.ID, Type: registry.CommandUpgrade,
		Upgrade: &registry.UpgradePayload{RequestID: u.ID, TargetVersion: u.TargetVersion, AllowDowngrade: u.AllowDowngrade}}
}

var system = Actor{Kind: ActorSystem, ID: "inventory"}

// deliver pushes the command to an online agent and records the delivery;
// failures leave the request pending (delivered on the next connect).
func (s *Service) deliver(ctx context.Context, u store.AgentUpgrade) store.AgentUpgrade {
	ok, err := s.reg.Deliver(ctx, u.AgentID, command(u))
	if err != nil || !ok {
		return u
	}
	if d, err := s.markDelivered(ctx, u); err == nil {
		return d
	}
	return u
}

func (s *Service) markDelivered(ctx context.Context, u store.AgentUpgrade) (store.AgentUpgrade, error) {
	changed := false
	d, err := s.repo.UpdateAgentUpgrade(ctx, u.TenantID, u.ID, func(cur *store.AgentUpgrade) ([]store.AuditRow, error) {
		if cur.State != store.UpgradePending {
			return nil, nil
		}
		now := s.now()
		cur.State, cur.UpdatedAt, cur.DeliveredAt = store.UpgradeDelivered, now, &now
		changed = true
		row, err := s.row(*cur, audit.AgentUpgradeDelivered, system, audit.OutcomeOK, "", nil)
		return []store.AuditRow{row}, err
	})
	if err == nil && changed {
		s.committed(ctx, d)
	}
	return d, err
}

// OnConnect returns the UPGRADE commands to send to an agent that just
// opened its command stream (its pending or delivered request) and records
// pending ones as delivered. Agents deduplicate by request id.
func (s *Service) OnConnect(ctx context.Context, a store.Agent) ([]registry.Command, error) {
	list, err := s.repo.ListAgentUpgrades(ctx, a.TenantID, repo.UpgradeFilter{AgentID: a.ID, Limit: 1})
	if err != nil {
		return nil, err
	}
	var cmds []registry.Command
	for _, u := range list {
		if u.State != store.UpgradePending && u.State != store.UpgradeDelivered {
			continue
		}
		if d, err := s.markDelivered(ctx, u); err == nil {
			u = d
		}
		cmds = append(cmds, command(u))
	}
	return cmds, nil
}

// Cancel cancels a pending or delivered request.
func (s *Service) Cancel(ctx context.Context, tenantID string, actor Actor, id string) (store.AgentUpgrade, error) {
	u, err := s.repo.UpdateAgentUpgrade(ctx, tenantID, id, func(cur *store.AgentUpgrade) ([]store.AuditRow, error) {
		if cur.State != store.UpgradePending && cur.State != store.UpgradeDelivered {
			return nil, ErrNotCancellable
		}
		now := s.now()
		cur.State, cur.UpdatedAt, cur.FinishedAt = store.UpgradeCancelled, now, &now
		row, err := s.row(*cur, audit.AgentUpgradeCancelled, actor, audit.OutcomeOK, "", nil)
		return []store.AuditRow{row}, err
	})
	if err != nil {
		return store.AgentUpgrade{}, err
	}
	s.committed(ctx, u)
	return u, nil
}

// List lists a tenant's requests (newest first).
func (s *Service) List(ctx context.Context, tenantID string, f repo.UpgradeFilter) ([]store.AgentUpgrade, error) {
	return s.repo.ListAgentUpgrades(ctx, tenantID, f)
}

// cleanDetail reports whether an agent's free-text detail is acceptable.
func cleanDetail(d string) bool {
	if len(d) > 256 || !utf8.ValidString(d) {
		return false
	}
	for _, r := range d {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
