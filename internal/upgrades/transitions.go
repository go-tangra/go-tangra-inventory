package upgrades

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// transitions is the request state machine (data-model §1.4). Agents may
// report failed (busy, downgrade refused, unsupported install, ...) and
// succeeded (already on the target) before downloading, and downloading
// before the delivery was recorded.
var transitions = map[string]map[string]bool{
	store.UpgradePending: {store.UpgradeDelivered: true, store.UpgradeDownloading: true, store.UpgradeSucceeded: true,
		store.UpgradeFailed: true, store.UpgradeCancelled: true, store.UpgradeExpired: true},
	store.UpgradeDelivered: {store.UpgradeDownloading: true, store.UpgradeSucceeded: true, store.UpgradeFailed: true,
		store.UpgradeCancelled: true, store.UpgradeExpired: true},
	store.UpgradeDownloading: {store.UpgradeInstalling: true, store.UpgradeFailed: true},
	store.UpgradeInstalling:  {store.UpgradeSucceeded: true, store.UpgradeFailed: true, store.UpgradeRolledBack: true},
}

func canTransition(from, to string) bool { return transitions[from][to] }

var reportEvents = map[string]struct {
	t       audit.EventType
	outcome string
}{
	StateDownloading: {audit.AgentUpgradeStarted, audit.OutcomeOK},
	StateInstalling:  {audit.AgentUpgradeInstalling, audit.OutcomeOK},
	StateSucceeded:   {audit.AgentUpgradeSucceeded, audit.OutcomeOK},
	StateFailed:      {audit.AgentUpgradeFailed, audit.OutcomeError},
	StateRolledBack:  {audit.AgentUpgradeRolledBack, audit.OutcomeError},
}

// Report is an agent's progress report.
type Report struct {
	RequestID   string
	State       string
	FromVersion string
	ToVersion   string
	Reason      string
	Detail      string
}

// errIgnored rolls back a report that does not apply (terminal request,
// invalid transition, succeeded without running the target).
var errIgnored = errors.New("upgrades: report ignored")

// Report applies an agent's progress report to its own request. Unknown
// states or reasons and oversized details are ErrInvalid; another agent's
// or tenant's request is repo.ErrNotFound (audited as refused); a report
// that does not fit the state machine is ignored (accepted false).
// succeeded is accepted only when the agent runs the target version.
func (s *Service) Report(ctx context.Context, a store.Agent, r Report) (bool, error) {
	ev, known := reportEvents[r.State]
	if !known || r.RequestID == "" || (r.Reason != "" && !ValidReason(r.Reason)) || !cleanDetail(r.Detail) {
		return false, fmt.Errorf("%w: report", ErrInvalid)
	}
	if _, err := s.ownRequest(ctx, a, r.RequestID, "report"); err != nil {
		return false, err
	}
	agent := Actor{Kind: ActorAgent, ID: a.ID}
	u, err := s.repo.UpdateAgentUpgrade(ctx, a.TenantID, r.RequestID, func(cur *store.AgentUpgrade) ([]store.AuditRow, error) {
		if !canTransition(cur.State, r.State) {
			return nil, errIgnored
		}
		if r.State == StateSucceeded && (r.ToVersion != cur.TargetVersion || a.AgentVersion != cur.TargetVersion) {
			return nil, errIgnored
		}
		now := s.now()
		extra := map[string]any{}
		cur.State, cur.UpdatedAt = r.State, now
		switch r.State {
		case StateDownloading:
			cur.StartedAt = &now
			cur.Attempts++
		case StateSucceeded, StateFailed, StateRolledBack:
			cur.FinishedAt = &now
			cur.Reason = r.Reason
			if r.State == StateSucceeded {
				start := cur.CreatedAt
				if cur.StartedAt != nil {
					start = *cur.StartedAt
				}
				extra["duration_seconds"] = int64(now.Sub(start) / time.Second)
			}
		}
		row, err := s.row(*cur, ev.t, agent, ev.outcome, r.Reason, extra)
		return []store.AuditRow{row}, err
	})
	if errors.Is(err, errIgnored) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	s.committed(ctx, u)
	if (u.State == store.UpgradeFailed || u.State == store.UpgradeRolledBack) && s.finished != nil {
		s.finished(ctx, u)
	}
	return true, nil
}

// ownRequest returns request id when it belongs to agent a; otherwise the
// attempt is audited as refused and repo.ErrNotFound returned (no oracle for
// other agents' or tenants' request ids).
func (s *Service) ownRequest(ctx context.Context, a store.Agent, id, what string) (store.AgentUpgrade, error) {
	u, err := s.repo.GetAgentUpgrade(ctx, a.TenantID, id)
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return store.AgentUpgrade{}, err
	}
	if err == nil && u.AgentID == a.ID {
		return u, nil
	}
	row, rerr := audit.Row(audit.Event{TenantID: a.TenantID, EventType: audit.AgentUpgradeRefused, ActorKind: ActorAgent, ActorID: a.ID,
		SubjectKind: audit.SubjectAgent, SubjectID: a.ID, Outcome: audit.OutcomeRefused, Reason: "foreign_request",
		Details: map[string]any{"request_id": id, "operation": what}}, s.now())
	if rerr == nil {
		_ = s.repo.AppendAudit(ctx, row)
	}
	return store.AgentUpgrade{}, repo.ErrNotFound
}

// Sweep expires requests no agent picked up within the request TTL and
// fails running ones without a report within the progress timeout.
func (s *Service) Sweep(ctx context.Context) (expired, failed int, err error) {
	now := s.now()
	stale, err := s.repo.ListStaleUpgrades(ctx, now, now.Add(-s.cfg.ProgressTimeout), 500)
	if err != nil {
		return 0, 0, err
	}
	for _, st := range stale {
		changed := false
		u, err := s.repo.UpdateAgentUpgrade(ctx, st.TenantID, st.ID, func(cur *store.AgentUpgrade) ([]store.AuditRow, error) {
			if !sweepOne(cur, now, now.Add(-s.cfg.ProgressTimeout)) {
				return nil, nil
			}
			changed = true
			t, outcome := audit.AgentUpgradeFailed, audit.OutcomeError
			if cur.State == store.UpgradeExpired {
				t = audit.AgentUpgradeExpired
			}
			row, err := s.row(*cur, t, system, outcome, cur.Reason, nil)
			return []store.AuditRow{row}, err
		})
		if err != nil {
			return expired, failed, err
		}
		if !changed {
			continue
		}
		s.committed(ctx, u)
		if u.State == store.UpgradeExpired {
			expired++
			continue
		}
		failed++
		if s.finished != nil {
			s.finished(ctx, u)
		}
	}
	return expired, failed, nil
}

// sweepOne applies the expiry or progress timeout to u (re-checked under
// the row lock); it reports whether u changed.
func sweepOne(u *store.AgentUpgrade, now, progressBefore time.Time) bool {
	switch {
	case (u.State == store.UpgradePending || u.State == store.UpgradeDelivered) && u.ExpiresAt.Before(now):
		u.State, u.Reason = store.UpgradeExpired, ReasonExpired
	case (u.State == store.UpgradeDownloading || u.State == store.UpgradeInstalling) && u.UpdatedAt.Before(progressBefore):
		u.State, u.Reason = store.UpgradeFailed, ReasonStartTimeout
	default:
		return false
	}
	u.UpdatedAt, u.FinishedAt = now, &now
	return true
}

// Check reasons (CheckAgentUpdateResponse.reason).
const (
	ReasonUpToDate      = "up_to_date"
	ReasonNoRelease     = "no_release_for_platform"
	ReasonActive        = "upgrade_active"
	ReasonNotComparable = "not_comparable"
)

// CheckResult answers an agent's update check.
type CheckResult struct {
	Available      bool
	TargetVersion  string
	RequestID      string
	Reason         string
	AllowDowngrade bool
}

// Check tells an agent (CLI `update`) whether an upgrade is available for
// its version and platform; with apply it creates the request (origin agent,
// audited) when one is available and none is active.
func (s *Service) Check(ctx context.Context, a store.Agent, current string, p agentrelease.Platform, apply bool) (CheckResult, error) {
	if !p.Valid() {
		return CheckResult{}, fmt.Errorf("%w: platform", ErrInvalid)
	}
	target, pinned, err := s.Target(ctx, a.TenantID)
	if err != nil {
		return CheckResult{}, err
	}
	list, err := s.repo.ListAgentUpgrades(ctx, a.TenantID, repo.UpgradeFilter{AgentID: a.ID, Limit: 1})
	if err != nil {
		return CheckResult{}, err
	}
	if len(list) == 1 && list[0].Active() {
		u := list[0]
		return CheckResult{Available: true, TargetVersion: u.TargetVersion, RequestID: u.ID, Reason: ReasonActive, AllowDowngrade: u.AllowDowngrade}, nil
	}
	res := CheckResult{TargetVersion: target}
	c, ok := agentrelease.Compare(current, target)
	switch {
	case target == "" || !s.rel.HasArtifact(ctx, target, p):
		res.Reason = ReasonNoRelease
		return res, nil
	case ok && (c == 0 || (c > 0 && !pinned)):
		res.Reason = ReasonUpToDate
		return res, nil
	case !ok:
		res.Reason = ReasonNotComparable
	}
	res.Available, res.AllowDowngrade = true, ok && c > 0
	if !apply {
		return res, nil
	}
	a.AgentVersion = current
	u, err := s.create(ctx, a, Actor{Kind: ActorAgent, ID: a.ID}, store.OriginAgent, target, res.AllowDowngrade)
	if err != nil {
		return CheckResult{}, err
	}
	res.RequestID = u.ID
	return res, nil
}

// AuthorizeDownload decides whether agent a may download version: the
// target of its own active request, or its own current version (the
// rollback package of a package install). It returns the version to serve.
func (s *Service) AuthorizeDownload(ctx context.Context, a store.Agent, requestID, version string) (string, error) {
	if version != "" && version == a.AgentVersion {
		return version, nil
	}
	if requestID == "" {
		return "", repo.ErrNotFound
	}
	u, err := s.ownRequest(ctx, a, requestID, "download")
	if err != nil {
		return "", err
	}
	switch {
	case !u.Active():
		return "", ErrNotActive
	case u.TargetVersion != version:
		return "", repo.ErrNotFound
	}
	return version, nil
}
