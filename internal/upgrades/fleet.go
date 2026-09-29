package upgrades

import (
	"context"
	"sort"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Fleet upgrade states (data-model §1.5).
const (
	FleetUpToDate    = "up_to_date"
	FleetAvailable   = "available"
	FleetPending     = "pending"
	FleetInProgress  = "in_progress"
	FleetFailed      = "failed"
	FleetRolledBack  = "rolled_back"
	FleetManual      = "manual_upgrade_required"
	FleetUnsupported = "unsupported"
)

// recentFailure is how long a failed or rolled-back upgrade shows on the agent.
const recentFailure = 7 * 24 * time.Hour

// FleetEntry is one enrolled agent in the fleet view. The legacy keys of
// GET /agents (agent_id, host_id, version, connected_at) are kept.
type FleetEntry struct {
	AgentID        string     `json:"agent_id"`
	TenantID       string     `json:"tenant_id"`
	HostID         string     `json:"host_id,omitempty"`
	Hostname       string     `json:"hostname,omitempty"`
	Version        string     `json:"version"`
	OS             string     `json:"os"`
	Arch           string     `json:"arch"`
	InstallType    string     `json:"install_type"`
	Online         bool       `json:"online"`
	ConnectedAt    *time.Time `json:"connected_at,omitempty"`
	LastSeen       time.Time  `json:"last_seen"`
	TargetVersion  string     `json:"target_version"`
	UpgradeState   string     `json:"upgrade_state"`
	UpgradeReason  string     `json:"upgrade_reason,omitempty"`
	UpgradeID      string     `json:"upgrade_id,omitempty"`
	StateChangedAt time.Time  `json:"state_changed_at"`
	// How the agent enrolled (feature 029): token | auto (+ key id).
	EnrolledVia     string `json:"enrolled_via"`
	AutoEnrollKeyID string `json:"auto_enroll_key_id,omitempty"`
}

// FleetFilter constrains Fleet. Cursor is the last agent id of the previous
// page (agents are ordered by id).
type FleetFilter struct {
	State    string
	Outdated bool
	Cursor   string
	Limit    int // <= 0: all
}

// Fleet lists every enrolled, non-revoked agent of the tenant (online or
// not) with its upgrade state, and the tenant target version. A registry
// failure degrades to everyone offline.
func (s *Service) Fleet(ctx context.Context, tenantID string, f FleetFilter) ([]FleetEntry, string, error) {
	target, _, err := s.Target(ctx, tenantID)
	if err != nil {
		return nil, "", err
	}
	agents, err := s.repo.ListAgents(ctx, tenantID)
	if err != nil {
		return nil, "", err
	}
	latest, err := s.repo.LatestAgentUpgrades(ctx, tenantID)
	if err != nil {
		return nil, "", err
	}
	hosts, err := s.repo.ListHosts(ctx, tenantID, store.HostFilter{})
	if err != nil {
		return nil, "", err
	}
	hostnames := map[string]string{}
	for _, h := range hosts {
		hostnames[h.ID] = h.Hostname
	}
	online := map[string]time.Time{}
	if conns, err := s.reg.ListConnected(ctx, tenantID); err == nil {
		for _, c := range conns {
			online[c.AgentID] = c.ConnectedAt
		}
	}
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })
	now := s.now()
	out := []FleetEntry{}
	for _, a := range agents {
		if f.Cursor != "" && a.ID <= f.Cursor {
			continue
		}
		e := entry(a, latest[a.ID], target, now)
		e.Hostname = hostnames[a.HostID]
		if at, ok := online[a.ID]; ok {
			e.Online, e.ConnectedAt = true, &at
		}
		if f.State != "" && e.UpgradeState != f.State {
			continue
		}
		if f.Outdated {
			if c, ok := agentrelease.Compare(a.AgentVersion, target); !ok || c >= 0 {
				continue
			}
		}
		out = append(out, e)
		if f.Limit > 0 && len(out) == f.Limit {
			break
		}
	}
	return out, target, nil
}

// entry derives the fleet state of one agent (first matching rule wins).
func entry(a store.Agent, last store.AgentUpgrade, target string, now time.Time) FleetEntry {
	e := FleetEntry{AgentID: a.ID, TenantID: a.TenantID, HostID: a.HostID, Version: a.AgentVersion, OS: a.OS, Arch: a.Arch,
		InstallType: a.InstallType, LastSeen: a.LastSeen, TargetVersion: target, StateChangedAt: a.LastSeen, UpgradeID: last.ID,
		EnrolledVia: a.EnrolledVia, AutoEnrollKeyID: a.AutoEnrollKeyID}
	if e.EnrolledVia == "" {
		e.EnrolledVia = store.EnrolledViaToken
	}
	if last.ID != "" {
		e.StateChangedAt = last.UpdatedAt
	}
	c, comparable := agentrelease.Compare(a.AgentVersion, target)
	outdated := comparable && c < 0
	recent := now.Sub(last.UpdatedAt) <= recentFailure
	switch {
	case last.State == store.UpgradePending || last.State == store.UpgradeDelivered:
		e.UpgradeState = FleetPending
	case last.State == store.UpgradeDownloading || last.State == store.UpgradeInstalling:
		e.UpgradeState = FleetInProgress
	case !a.HasCapability(store.CapUpgradeV1) && outdated:
		e.UpgradeState = FleetManual
	case last.State == store.UpgradeFailed && last.Reason == ReasonUnsupportedInstall:
		e.UpgradeState, e.UpgradeReason = FleetUnsupported, last.Reason
	case last.State == store.UpgradeFailed && recent && outdated:
		e.UpgradeState, e.UpgradeReason = FleetFailed, last.Reason
	case last.State == store.UpgradeRolledBack && recent:
		e.UpgradeState, e.UpgradeReason = FleetRolledBack, last.Reason
	case outdated:
		e.UpgradeState = FleetAvailable
	default:
		e.UpgradeState = FleetUpToDate
	}
	return e
}

// Agent returns one agent's fleet entry and its recent requests (<= 20).
func (s *Service) Agent(ctx context.Context, tenantID, agentID string) (FleetEntry, []store.AgentUpgrade, error) {
	entries, _, err := s.Fleet(ctx, tenantID, FleetFilter{})
	if err != nil {
		return FleetEntry{}, nil, err
	}
	for _, e := range entries {
		if e.AgentID != agentID {
			continue
		}
		recent, err := s.repo.ListAgentUpgrades(ctx, tenantID, repo.UpgradeFilter{AgentID: agentID, Limit: 20})
		if err != nil {
			return FleetEntry{}, nil, err
		}
		return e, recent, nil
	}
	return FleetEntry{}, nil, repo.ErrNotFound
}
