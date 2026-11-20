package teleportjoin

import (
	"context"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Teleport join capability of a host (ListTeleportJoinTargets).
const (
	CapabilityEnabled          = "enabled"
	CapabilityUpgradeRequired  = store.ReasonUpgradeRequired
	CapabilityNotSupported     = store.ReasonNotSupportedPlatform
	CapabilityNoAgent          = store.ReasonNoAgent
	CapabilityAmbiguousAgent   = store.ReasonAmbiguousAgent
	CapabilityRetired          = store.ReasonRetired
	CapabilityDisabledOnServer = "disabled_on_server"
)

// Target list bounds (ListTeleportJoinTargetsRequest).
const (
	DefaultTargetLimit = 50
	MaxTargetLimit     = 200
	MaxTargetQuery     = 64
	MaxTargetHosts     = 200
)

// Capability derives an agent's Teleport join capability (nil: the host has
// no agent; ambiguous: more than one non-revoked agent claims the host).
// The version gate is the teleport.v1 capability: only agent releases that
// know COMMAND_TYPE_TELEPORT_JOIN announce it (on Linux, whether or not
// the local configuration enables it), so any other Linux agent needs an
// upgrade first. Agents that reported another platform cannot join.
func Capability(enabled bool, a *store.Agent, ambiguous bool) string {
	switch {
	case !enabled:
		return CapabilityDisabledOnServer
	case a == nil:
		return CapabilityNoAgent
	case ambiguous:
		return CapabilityAmbiguousAgent
	case a.OS != "" && a.OS != "linux":
		return CapabilityNotSupported
	case a.HasCapability(store.CapTeleportV1):
		return CapabilityEnabled
	}
	return CapabilityUpgradeRequired
}

// hostAgents maps each host to its newest non-revoked agent and reports the
// hosts claimed by more than one. An agent binds itself to a host by the
// identity it reports, so a second agent on a host may be an impostor:
// nothing is delivered there until an operator revokes one of them.
func hostAgents(agents []store.Agent) (map[string]*store.Agent, map[string]bool) {
	agentOf := map[string]*store.Agent{}
	ambiguous := map[string]bool{}
	for k := range agents {
		a := &agents[k]
		if a.HostID == "" {
			continue
		}
		if cur, ok := agentOf[a.HostID]; ok {
			ambiguous[a.HostID] = true
			if !a.LastSeen.After(cur.LastSeen) {
				continue
			}
		}
		agentOf[a.HostID] = a
	}
	return agentOf, ambiguous
}

// hostCapability is the capability of host h (retired hosts first).
func hostCapability(h store.Host, a *store.Agent, ambiguous bool) string {
	if h.Status == store.HostRetired {
		return CapabilityRetired
	}
	return Capability(true, a, ambiguous)
}

// eligibility is the agent of host h and why it cannot join now ("" = it
// can).
func (s *Service) eligibility(ctx context.Context, h store.Host) (*store.Agent, string, error) {
	agents, err := s.repo.ListAgents(ctx, h.TenantID)
	if err != nil {
		return nil, "", err
	}
	agentOf, ambiguous := hostAgents(agents)
	a := agentOf[h.ID]
	if c := hostCapability(h, a, ambiguous[h.ID]); c != CapabilityEnabled {
		return a, c, nil
	}
	return a, "", nil
}

// Target is one host of ListTeleportJoinTargets: the facts the teleport
// module turns into node labels.
type Target struct {
	HostID       string
	Hostname     string
	OSName       string
	OSVersion    string
	OSArch       string
	Manufacturer string
	Model        string
	Tags         map[string]string
	AgentOnline  bool
	Capability   string
}

// TargetQuery selects the hosts of ListTargets: either the non-retired
// hosts whose hostname contains Query (case-insensitive), at most Limit
// (0: 50), or exactly HostIDs (at most 200; unknown ids omitted, retired
// hosts included with capability retired).
type TargetQuery struct {
	Query   string
	Limit   int
	HostIDs []string
}

// check validates q.
func (q TargetQuery) check() error {
	switch {
	case utf8.RuneCountInString(q.Query) > MaxTargetQuery || !utf8.ValidString(q.Query):
		return invalid("query")
	case q.Limit < 0 || q.Limit > MaxTargetLimit:
		return invalid("limit")
	case len(q.HostIDs) > MaxTargetHosts:
		return invalid("host_ids")
	case len(q.HostIDs) > 0 && q.Query != "":
		return invalid("query")
	}
	for _, id := range q.HostIDs {
		if !uuidRE.MatchString(id) {
			return invalid("host_ids")
		}
	}
	return nil
}

// ListTargets lists the hosts of q by hostname with their facts, agent
// state and capability; truncated reports a query that matched more than
// its limit.
func (s *Service) ListTargets(ctx context.Context, tenantID string, q TargetQuery) ([]Target, bool, error) {
	if !s.cfg.Enabled {
		return nil, false, ErrDisabled
	}
	if err := q.check(); err != nil {
		return nil, false, err
	}
	limit := q.Limit
	if limit == 0 {
		limit = DefaultTargetLimit
	}
	hosts, err := s.targetHosts(ctx, tenantID, q)
	if err != nil {
		return nil, false, err
	}
	agents, err := s.repo.ListAgents(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	conns, err := s.reg.ListConnected(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	online := make(map[string]bool, len(conns))
	for _, c := range conns {
		online[c.AgentID] = true
	}
	agentOf, ambiguous := hostAgents(agents)
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i].Hostname != hosts[j].Hostname {
			return hosts[i].Hostname < hosts[j].Hostname
		}
		return hosts[i].ID < hosts[j].ID
	})
	truncated := len(q.HostIDs) == 0 && len(hosts) > limit
	if truncated {
		hosts = hosts[:limit]
	}
	out := make([]Target, 0, len(hosts))
	for _, h := range hosts {
		a := agentOf[h.ID]
		tags := make(map[string]string, len(h.Tags))
		for k, v := range h.Tags {
			tags[k] = v
		}
		out = append(out, Target{HostID: h.ID, Hostname: h.Hostname, OSName: h.OSName, OSVersion: h.OSVersion, OSArch: h.OSArch,
			Manufacturer: h.Manufacturer, Model: h.Model, Tags: tags, AgentOnline: a != nil && online[a.ID],
			Capability: hostCapability(h, a, ambiguous[h.ID])})
	}
	return out, truncated, nil
}

// targetHosts reads the hosts q selects.
func (s *Service) targetHosts(ctx context.Context, tenantID string, q TargetQuery) ([]store.Host, error) {
	if len(q.HostIDs) == 0 {
		hosts, err := s.repo.ListHosts(ctx, tenantID, store.HostFilter{Hostname: q.Query})
		if err != nil {
			return nil, err
		}
		live := hosts[:0:0]
		for _, h := range hosts {
			if h.Status != store.HostRetired {
				live = append(live, h)
			}
		}
		return live, nil
	}
	var out []store.Host
	seen := map[string]bool{}
	for _, id := range q.HostIDs {
		id = strings.ToLower(id)
		if seen[id] {
			continue
		}
		seen[id] = true
		h, err := s.repo.GetHost(ctx, tenantID, id)
		if errors.Is(err, repo.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}
