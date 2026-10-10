package moduledelivery

import (
	"context"
	"errors"
	"net/netip"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Module delivery capability of a host (ListModuleTargets).
const (
	CapabilityEnabled          = "enabled"
	CapabilityDisabledOnHost   = store.ReasonDisabledOnHost
	CapabilityUpgradeRequired  = store.ReasonUpgradeRequired
	CapabilityNotSupported     = store.ReasonNotSupportedPlatform
	CapabilityNoAgent          = store.ReasonNoAgent
	CapabilityAmbiguousAgent   = store.ReasonAmbiguousAgent
	CapabilityDisabledOnServer = "disabled_on_server"
)

// FirstModuleVersion is the first agent release that can announce module.v1.
const FirstModuleVersion = "4.8.0"

// Target list bounds (ListModuleTargetsRequest).
const (
	DefaultTargetLimit = 50
	MaxTargetLimit     = 200
	MaxTargetQuery     = 64
	MaxTargetIPs       = 16
)

// Capability derives an agent's module capability (nil: the host has no
// agent; ambiguous: more than one non-revoked agent claims the host).
func Capability(enabled bool, a *store.Agent, ambiguous bool) string {
	switch {
	case !enabled:
		return CapabilityDisabledOnServer
	case a == nil:
		return CapabilityNoAgent
	case ambiguous:
		return CapabilityAmbiguousAgent
	case a.OS == "windows":
		return CapabilityNotSupported
	case a.HasCapability(store.CapModuleV1):
		return CapabilityEnabled
	}
	if c, ok := agentrelease.Compare(a.AgentVersion, FirstModuleVersion); ok && c >= 0 {
		return CapabilityDisabledOnHost
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

// eligibility is the agent of host h and why it cannot receive a module
// now ("" = it can).
func (s *Service) eligibility(ctx context.Context, h store.Host) (*store.Agent, string, error) {
	agents, err := s.repo.ListAgents(ctx, h.TenantID)
	if err != nil {
		return nil, "", err
	}
	agentOf, ambiguous := hostAgents(agents)
	a := agentOf[h.ID]
	if h.Status == store.HostRetired {
		return a, store.ReasonRetired, nil
	}
	if c := Capability(true, a, ambiguous[h.ID]); c != CapabilityEnabled {
		return a, c, nil
	}
	return a, "", nil
}

// Target is one host of ListModuleTargets.
type Target struct {
	HostID      string
	Hostname    string
	OSName      string
	AgentOnline bool
	Capability  string
	IPAddresses []string
}

// ListTargets lists the tenant's non-retired hosts whose hostname contains
// query (case-insensitive), by hostname, at most limit (0: 50), with their
// capability and the addresses of their latest snapshot.
func (s *Service) ListTargets(ctx context.Context, tenantID, query string, limit int) ([]Target, bool, error) {
	if !s.cfg.Enabled {
		return nil, false, ErrDisabled
	}
	switch {
	case utf8.RuneCountInString(query) > MaxTargetQuery || !utf8.ValidString(query):
		return nil, false, invalid("query")
	case limit < 0 || limit > MaxTargetLimit:
		return nil, false, invalid("limit")
	case limit == 0:
		limit = DefaultTargetLimit
	}
	hosts, err := s.repo.ListHosts(ctx, tenantID, store.HostFilter{Hostname: query})
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
	live := hosts[:0:0]
	for _, h := range hosts {
		if h.Status != store.HostRetired {
			live = append(live, h)
		}
	}
	sort.Slice(live, func(i, j int) bool {
		if live[i].Hostname != live[j].Hostname {
			return live[i].Hostname < live[j].Hostname
		}
		return live[i].ID < live[j].ID
	})
	truncated := len(live) > limit
	if truncated {
		live = live[:limit]
	}
	out := make([]Target, 0, len(live))
	for _, h := range live {
		a := agentOf[h.ID]
		t := Target{HostID: h.ID, Hostname: h.Hostname, OSName: h.OSName, AgentOnline: a != nil && online[a.ID],
			Capability: Capability(true, a, ambiguous[h.ID]), IPAddresses: []string{}}
		snap, err := s.repo.GetLatestForHost(ctx, tenantID, h.ID)
		switch {
		case err == nil:
			t.IPAddresses = snapshotIPs(snap.Payload)
		case !errors.Is(err, repo.ErrNotFound):
			return nil, false, err
		}
		out = append(out, t)
	}
	return out, truncated, nil
}

// snapshotIPs lists a host's reported addresses for the wizard's pre-fill:
// the primary IPv4 and IPv6 first, then every interface address, without
// prefix, without loopback, link-local, multicast or unspecified ones,
// deduplicated, at most MaxTargetIPs.
func snapshotIPs(inv store.Inventory) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(raw string) {
		if len(out) == MaxTargetIPs {
			return
		}
		raw, _, _ = strings.Cut(strings.TrimSpace(raw), "/")
		a, err := netip.ParseAddr(raw)
		if err != nil || a.Zone() != "" || a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
			a.IsMulticast() || a.IsUnspecified() {
			return
		}
		s := a.Unmap().String()
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(inv.PrimaryIPv4)
	add(inv.PrimaryIPv6)
	for _, n := range inv.Networks {
		if n.Type == store.IfaceLoopback {
			continue
		}
		for _, a := range n.Addresses {
			add(a.Address)
		}
		for _, a := range n.IPAddresses {
			add(a)
		}
	}
	return out
}
