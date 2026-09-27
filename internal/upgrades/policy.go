package upgrades

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// PolicyActorID is the system actor of every policy-created request and pause.
const PolicyActorID = "upgrade-policy"

var hhmmRE = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ValidatePolicy checks a policy's editable fields: HH:MM window, an IANA
// timezone (not "Local"), 1-100 concurrent upgrades and an empty or
// self-upgrading release target version.
func ValidatePolicy(p store.AgentUpgradePolicy) error {
	switch {
	case !hhmmRE.MatchString(p.WindowStart) || !hhmmRE.MatchString(p.WindowEnd):
		return fmt.Errorf("%w: window must be HH:MM", ErrInvalid)
	case p.MaxConcurrent < 1 || p.MaxConcurrent > 100:
		return fmt.Errorf("%w: max_concurrent must be 1-100", ErrInvalid)
	case p.TargetVersion != "" && (!agentrelease.IsRelease(p.TargetVersion) || agentrelease.BelowFloor(p.TargetVersion)):
		return fmt.Errorf("%w: target_version must be a release version of at least %s", ErrInvalid, agentrelease.Floor)
	}
	if _, err := location(p.Timezone); err != nil {
		return err
	}
	return nil
}

func location(tz string) (*time.Location, error) {
	if tz == "" || tz == "Local" || len(tz) > 64 {
		return nil, fmt.Errorf("%w: timezone must be an IANA name", ErrInvalid)
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return nil, fmt.Errorf("%w: unknown timezone", ErrInvalid)
	}
	return loc, nil
}

func minutes(hhmm string) int {
	return int(hhmm[0]-'0')*600 + int(hhmm[1]-'0')*60 + int(hhmm[3]-'0')*10 + int(hhmm[4]-'0')
}

// InWindow reports whether now falls in the policy's maintenance window in
// its timezone: [start, end), wrapping past midnight when end < start, the
// whole day when start = end. An invalid window or timezone is never open.
func InWindow(now time.Time, p store.AgentUpgradePolicy) bool {
	if !hhmmRE.MatchString(p.WindowStart) || !hhmmRE.MatchString(p.WindowEnd) {
		return false
	}
	loc, err := location(p.Timezone)
	if err != nil {
		return false
	}
	l := now.In(loc)
	cur := l.Hour()*60 + l.Minute()
	start, end := minutes(p.WindowStart), minutes(p.WindowEnd)
	switch {
	case start == end:
		return true
	case start < end:
		return cur >= start && cur < end
	default:
		return cur >= start || cur < end
	}
}

// Candidate is an agent the automatic planner considers.
type Candidate struct {
	Agent     store.Agent
	Online    bool
	Last      store.AgentUpgrade // the agent's latest request (zero: none)
	Available bool               // the target release has an artifact for its platform
}

// PlanAuto selects the agents the policy upgrades now: nothing when the
// policy is off, paused, outside its window or there is no target; else the
// online, upgrade-capable agents with a supported platform, no active
// request and an available artifact whose version is older than target (or
// newer, when the target is a pin), skipping agents whose last request for
// this target already failed. Oldest version first, then agent id, at most
// max_concurrent - active.
func PlanAuto(now time.Time, p store.AgentUpgradePolicy, target string, pinned bool, cands []Candidate, active int) []string {
	capacity := p.MaxConcurrent - active
	if !p.Enabled || p.Paused || target == "" || capacity <= 0 || !InWindow(now, p) {
		return nil
	}
	var pick []Candidate
	for _, c := range cands {
		if !c.Online || !c.Available || !c.Agent.HasCapability(store.CapUpgradeV1) || !platformOf(c.Agent).Valid() || c.Last.Active() {
			continue
		}
		if c.Last.State == store.UpgradeFailed && c.Last.Reason == ReasonUnsupportedInstall {
			continue
		}
		if (c.Last.State == store.UpgradeFailed || c.Last.State == store.UpgradeRolledBack) && c.Last.TargetVersion == target {
			continue
		}
		cmp, ok := agentrelease.Compare(c.Agent.AgentVersion, target)
		if !ok || cmp == 0 || (cmp > 0 && !pinned) {
			continue
		}
		pick = append(pick, c)
	}
	sort.SliceStable(pick, func(i, j int) bool {
		if c, _ := agentrelease.Compare(pick[i].Agent.AgentVersion, pick[j].Agent.AgentVersion); c != 0 {
			return c < 0
		}
		return pick[i].Agent.ID < pick[j].Agent.ID
	})
	if len(pick) > capacity {
		pick = pick[:capacity]
	}
	var out []string
	for _, c := range pick {
		out = append(out, c.Agent.ID)
	}
	return out
}

// PolicyInput is the editable part of a policy (PUT /agents/upgrade-policy).
type PolicyInput struct {
	Enabled       bool   `json:"enabled"`
	WindowStart   string `json:"window_start"`
	WindowEnd     string `json:"window_end"`
	Timezone      string `json:"timezone"`
	MaxConcurrent int    `json:"max_concurrent"`
	TargetVersion string `json:"target_version"`
}

// GetPolicy returns the tenant's policy (defaults when none is stored).
func (s *Service) GetPolicy(ctx context.Context, tenantID string) (store.AgentUpgradePolicy, error) {
	p, _, err := s.repo.GetUpgradePolicy(ctx, tenantID)
	return p, err
}

// UpdatePolicy validates and stores the tenant's policy; a pinned target must
// be a stored release. The change is audited with before/after values (no
// row when nothing changed). Enabling creates no requests: the scheduler does,
// inside the window.
func (s *Service) UpdatePolicy(ctx context.Context, tenantID string, actor Actor, in PolicyInput) (store.AgentUpgradePolicy, error) {
	want := store.AgentUpgradePolicy{Enabled: in.Enabled, WindowStart: in.WindowStart, WindowEnd: in.WindowEnd, Timezone: in.Timezone,
		MaxConcurrent: in.MaxConcurrent, TargetVersion: in.TargetVersion}
	if err := ValidatePolicy(want); err != nil {
		return store.AgentUpgradePolicy{}, err
	}
	if in.TargetVersion != "" {
		rels, err := s.rel.Releases(ctx)
		if err != nil {
			return store.AgentUpgradePolicy{}, err
		}
		found := false
		for _, r := range rels {
			found = found || r.Version == in.TargetVersion
		}
		if !found {
			return store.AgentUpgradePolicy{}, ErrUnknownVersion
		}
	}
	p, err := s.repo.UpdateUpgradePolicy(ctx, tenantID, func(p *store.AgentUpgradePolicy) ([]store.AuditRow, error) {
		changes := map[string]any{}
		diff := func(field string, before, after any) {
			if before != after {
				changes[field] = map[string]any{"before": before, "after": after}
			}
		}
		diff("enabled", p.Enabled, want.Enabled)
		diff("window_start", p.WindowStart, want.WindowStart)
		diff("window_end", p.WindowEnd, want.WindowEnd)
		diff("timezone", p.Timezone, want.Timezone)
		diff("max_concurrent", p.MaxConcurrent, want.MaxConcurrent)
		diff("target_version", p.TargetVersion, want.TargetVersion)
		if len(changes) == 0 {
			return nil, nil
		}
		p.Enabled, p.WindowStart, p.WindowEnd, p.Timezone = want.Enabled, want.WindowStart, want.WindowEnd, want.Timezone
		p.MaxConcurrent, p.TargetVersion = want.MaxConcurrent, want.TargetVersion
		p.UpdatedBy, p.UpdatedAt = actor.ID, s.now()
		row, err := s.policyRow(tenantID, audit.UpgradePolicyUpdated, actor, audit.OutcomeOK, "", map[string]any{"changes": changes})
		return []store.AuditRow{row}, err
	})
	if err != nil {
		return store.AgentUpgradePolicy{}, err
	}
	s.pub.Publish(ctx, tenantID, EventPolicy, map[string]any{"paused": p.Paused})
	return p, nil
}

// ResumePolicy clears a pause (audited); resuming an unpaused policy is a no-op.
func (s *Service) ResumePolicy(ctx context.Context, tenantID string, actor Actor) (store.AgentUpgradePolicy, error) {
	resumed := false
	p, err := s.repo.UpdateUpgradePolicy(ctx, tenantID, func(p *store.AgentUpgradePolicy) ([]store.AuditRow, error) {
		if !p.Paused {
			return nil, nil
		}
		p.Paused, p.PausedReason = false, ""
		p.UpdatedBy, p.UpdatedAt = actor.ID, s.now()
		resumed = true
		row, err := s.policyRow(tenantID, audit.UpgradePolicyResumed, actor, audit.OutcomeOK, "", nil)
		return []store.AuditRow{row}, err
	})
	if err != nil {
		return store.AgentUpgradePolicy{}, err
	}
	if resumed {
		s.pub.Publish(ctx, tenantID, EventPolicy, map[string]any{"paused": false})
	}
	return p, nil
}

// PauseOnFailure is the OnFinished hook of the automatic policy: a
// policy-created request ending failed or rolled back pauses the tenant's
// policy (audited once) until an administrator resumes it.
func (s *Service) PauseOnFailure(ctx context.Context, u store.AgentUpgrade) {
	if u.Origin != store.OriginPolicy || (u.State != store.UpgradeFailed && u.State != store.UpgradeRolledBack) {
		return
	}
	paused := false
	_, err := s.repo.UpdateUpgradePolicy(ctx, u.TenantID, func(p *store.AgentUpgradePolicy) ([]store.AuditRow, error) {
		if p.Paused {
			return nil, nil
		}
		p.Paused, p.PausedReason = true, u.State+":"+u.ID
		p.UpdatedBy, p.UpdatedAt = PolicyActorID, s.now()
		paused = true
		row, err := s.policyRow(u.TenantID, audit.UpgradePolicyPaused, Actor{Kind: ActorSystem, ID: PolicyActorID}, audit.OutcomeError, u.Reason,
			map[string]any{"request_id": u.ID, "agent_id": u.AgentID, "to_version": u.TargetVersion})
		return []store.AuditRow{row}, err
	})
	if err == nil && paused {
		s.pub.Publish(ctx, u.TenantID, EventPolicy, map[string]any{"paused": true})
	}
}

func (s *Service) policyRow(tenantID string, t audit.EventType, actor Actor, outcome, reason string, details map[string]any) (store.AuditRow, error) {
	return audit.Row(audit.Event{TenantID: tenantID, EventType: t, ActorKind: actor.Kind, ActorID: actor.ID,
		SubjectKind: audit.SubjectUpgradePolicy, SubjectID: tenantID, Outcome: outcome, Reason: reason, Details: details}, s.now())
}
