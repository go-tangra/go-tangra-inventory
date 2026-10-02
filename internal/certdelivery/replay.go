package certdelivery

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// agentUnsupported is why agent a cannot install certificates ("" = it can).
func agentUnsupported(a store.Agent) string {
	switch {
	case a.OS == "windows":
		return store.ReasonPlatform
	case !a.HasCapability(store.CapCertV1):
		return store.ReasonNoCapability
	}
	return ""
}

// OnConnect returns the CERTIFICATE commands to send to agent a, which just
// opened its command stream with the capabilities it announced: its active
// items, oldest first, at most store.MaxReplayPerConnect (the rest follow
// on a later connect); pending ones are recorded as delivered. Delivered and
// fetched items are sent again (the agent deduplicates by item id). An agent
// without cert.v1 gets nothing: its pending and delivered items become
// unsupported.
func (s *Service) OnConnect(ctx context.Context, a store.Agent) ([]registry.Command, error) {
	if !s.cfg.Enabled {
		return nil, nil
	}
	items, err := s.repo.ListActiveCertItemsForAgent(ctx, a.TenantID, a.ID, store.MaxReplayPerConnect)
	if err != nil {
		return nil, err
	}
	if reason := agentUnsupported(a); reason != "" {
		for _, it := range items {
			_, _, _ = s.update(ctx, a.TenantID, it.ID, func(cur *store.CertDeliveryItem) (repo.CertItemChange, error) {
				if cur.State != store.DeliveryPending && cur.State != store.DeliveryDelivered {
					return repo.CertItemChange{}, errNoChange
				}
				s.finish(cur, store.DeliveryUnsupported, reason)
				return repo.CertItemChange{Audit: []store.AuditRow{s.itemRow(audit.CertDeliveryUnsupported, system, audit.OutcomeRefused, *cur, nil)}}, nil
			})
		}
		return nil, nil
	}
	cmds := make([]registry.Command, 0, len(items))
	for _, it := range items {
		if it.State == store.DeliveryPending {
			_, _, _ = s.markDelivered(ctx, it)
		}
		cmds = append(cmds, command(it))
	}
	return cmds, nil
}
