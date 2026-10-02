package certdelivery

import (
	"context"
	"sort"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Agent certificate capability (data-model §1.5, PreviewCertificateTargets
// and the agent list).
const (
	CapabilityEnabled          = "enabled"
	CapabilityDisabledOnHost   = "disabled_on_host"
	CapabilityUpgradeRequired  = "upgrade_required"
	CapabilityNotSupported     = "not_supported_platform"
	CapabilityNoAgent          = "no_agent"
	CapabilityDisabledOnServer = "disabled_on_server"
)

// FirstCertVersion is the first agent release that can announce cert.v1.
const FirstCertVersion = "4.7.0"

// Capability derives an agent's certificate capability (nil: the host has no
// agent); first match of data-model §1.5.
func Capability(enabled bool, a *store.Agent) string {
	switch {
	case !enabled:
		return CapabilityDisabledOnServer
	case a == nil:
		return CapabilityNoAgent
	case a.OS == "windows":
		return CapabilityNotSupported
	case a.HasCapability(store.CapCertV1):
		return CapabilityEnabled
	}
	if c, ok := agentrelease.Compare(a.AgentVersion, FirstCertVersion); ok && c >= 0 {
		return CapabilityDisabledOnHost
	}
	return CapabilityUpgradeRequired
}

// target is one resolved host with its agent.
type target struct {
	host     store.Host
	agent    *store.Agent
	online   bool
	explicit bool
}

// selection is a resolved host selector.
type selection struct {
	targets []target
	unknown []string // requested ids not found in the tenant
}

// checkSelector validates a host selector and returns the deduplicated ids.
func checkSelector(ids, tags []string) ([]string, error) {
	if len(ids) == 0 && len(tags) == 0 {
		return nil, invalid("selector")
	}
	if len(ids) > store.MaxDeliveryHosts {
		return nil, invalid("host_ids")
	}
	if len(tags) > store.MaxHostTags {
		return nil, invalid("host_tags")
	}
	seen := map[string]bool{}
	var out []string
	for _, id := range ids {
		if !uuidRE.MatchString(id) {
			return nil, invalid("host_ids")
		}
		id = strings.ToLower(id)
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, t := range tags {
		if !certmaterial.ValidTag(t) {
			return nil, invalid("host_tags")
		}
	}
	return out, nil
}

// matchTag reports whether tags satisfy one selector ("key" or "key=value").
func matchTag(tags map[string]string, sel string) bool {
	k, v, withValue := strings.Cut(sel, "=")
	got, ok := tags[k]
	return ok && (!withValue || got == v)
}

// resolve resolves explicit ids and tag selectors (all must match) within
// the tenant: explicit hosts first in request order, then non-retired tag
// matches by hostname. Ids not in the tenant are reported as unknown.
func (s *Service) resolve(ctx context.Context, tenantID string, ids, tags []string) (selection, error) {
	hosts, err := s.repo.ListHosts(ctx, tenantID, store.HostFilter{})
	if err != nil {
		return selection{}, err
	}
	agents, err := s.repo.ListAgents(ctx, tenantID)
	if err != nil {
		return selection{}, err
	}
	online, err := s.online(ctx, tenantID)
	if err != nil {
		return selection{}, err
	}
	agentOf := map[string]*store.Agent{}
	for k := range agents {
		a := &agents[k]
		if cur, ok := agentOf[a.HostID]; a.HostID != "" && (!ok || a.LastSeen.After(cur.LastSeen)) {
			agentOf[a.HostID] = a
		}
	}
	byID := make(map[string]store.Host, len(hosts))
	for _, h := range hosts {
		byID[strings.ToLower(h.ID)] = h
	}
	var sel selection
	seen := map[string]bool{}
	add := func(h store.Host, explicit bool) {
		seen[h.ID] = true
		a := agentOf[h.ID]
		sel.targets = append(sel.targets, target{host: h, agent: a, online: a != nil && online[a.ID], explicit: explicit})
	}
	for _, id := range ids {
		h, ok := byID[id]
		if !ok {
			sel.unknown = append(sel.unknown, id)
			continue
		}
		add(h, true)
	}
	if len(tags) > 0 {
		sort.Slice(hosts, func(i, j int) bool {
			if hosts[i].Hostname != hosts[j].Hostname {
				return hosts[i].Hostname < hosts[j].Hostname
			}
			return hosts[i].ID < hosts[j].ID
		})
	hostLoop:
		for _, h := range hosts {
			if seen[h.ID] || h.Status == store.HostRetired {
				continue
			}
			for _, t := range tags {
				if !matchTag(h.Tags, t) {
					continue hostLoop
				}
			}
			add(h, false)
		}
	}
	return sel, nil
}

// TargetHost is one host of a preview.
type TargetHost struct {
	HostID      string
	Hostname    string
	OSName      string
	Tags        map[string]string
	AgentOnline bool
	Capability  string
}

// Preview is the resolution of a selector without any write.
type Preview struct {
	Hosts          []TargetHost
	UnknownHostIDs []string
	Truncated      bool // the selection exceeded store.MaxDeliveryHosts
}

// Preview resolves a selector like CreateCertificateDelivery would.
func (s *Service) Preview(ctx context.Context, tenantID string, ids, tags []string) (Preview, error) {
	if !s.cfg.Enabled {
		return Preview{}, ErrDisabled
	}
	ids, err := checkSelector(ids, tags)
	if err != nil {
		return Preview{}, err
	}
	sel, err := s.resolve(ctx, tenantID, ids, tags)
	if err != nil {
		return Preview{}, err
	}
	p := Preview{UnknownHostIDs: sel.unknown, Hosts: []TargetHost{}}
	for _, t := range sel.targets {
		if len(p.Hosts) == store.MaxDeliveryHosts {
			p.Truncated = true
			break
		}
		p.Hosts = append(p.Hosts, TargetHost{HostID: t.host.ID, Hostname: t.host.Hostname, OSName: t.host.OSName, Tags: t.host.Tags,
			AgentOnline: t.online, Capability: Capability(true, t.agent)})
	}
	return p, nil
}
