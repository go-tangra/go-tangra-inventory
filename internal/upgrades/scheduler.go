package upgrades

import (
	"context"
	"errors"
	"fmt"
)

// PolicyLockKey names the cluster-wide lock of a tenant's policy run, so one
// replica plans a tenant at a time.
func PolicyLockKey(tenantID string) string { return "inventory_upgrade_policy:" + tenantID }

// RunPolicies runs every enabled policy once (the scheduler worker calls it
// every minute) and returns how many requests it created. A tenant whose lock
// another replica holds is skipped; the first error is returned after every
// tenant was tried.
func (s *Service) RunPolicies(ctx context.Context) (int, error) {
	pols, err := s.repo.ListEnabledUpgradePolicies(ctx)
	if err != nil {
		return 0, err
	}
	total := 0
	var errs []error
	for _, p := range pols {
		_, err := s.repo.TryTenantLock(ctx, PolicyLockKey(p.TenantID), func(ctx context.Context) error {
			n, err := s.runPolicy(ctx, p.TenantID)
			total += n
			return err
		})
		if err != nil {
			errs = append(errs, fmt.Errorf("tenant %s: %w", p.TenantID, err))
		}
	}
	return total, errors.Join(errs...)
}

// runPolicy plans and creates one tenant's automatic requests (lock held).
func (s *Service) runPolicy(ctx context.Context, tenantID string) (int, error) {
	p, _, err := s.repo.GetUpgradePolicy(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	if !p.Enabled || p.Paused || !InWindow(s.now(), p) {
		return 0, nil
	}
	target, pinned := p.TargetVersion, p.TargetVersion != ""
	if !pinned {
		target = s.rel.CurrentVersion(ctx)
	}
	if target == "" {
		return 0, nil
	}
	agents, err := s.repo.ListAgents(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	latest, err := s.repo.LatestAgentUpgrades(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	conns, err := s.reg.ListConnected(ctx, tenantID)
	if err != nil {
		return 0, err
	}
	online := map[string]bool{}
	for _, c := range conns {
		online[c.AgentID] = true
	}
	active := 0
	cands := make([]Candidate, 0, len(agents))
	for _, a := range agents {
		last := latest[a.ID]
		if last.Active() {
			active++
		}
		cands = append(cands, Candidate{Agent: a, Online: online[a.ID], Last: last, Available: s.rel.HasArtifact(ctx, target, platformOf(a))})
	}
	ids := PlanAuto(s.now(), p, target, pinned, cands, active)
	if len(ids) == 0 {
		return 0, nil
	}
	res, err := s.request(ctx, tenantID, Actor{Kind: ActorSystem, ID: PolicyActorID}, "policy", ids, false)
	return len(res.Created), err
}
