package moduledelivery

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// OnConnect returns the MODULE commands to send to agent a, which just
// opened its command stream with the capabilities it announced: its active
// items, oldest first, at most store.MaxReplayPerConnect; pending ones are
// recorded as delivered. Delivered and fetched items are sent again (the
// agent deduplicates by item id and attempt). An agent that no longer
// announces module.v1 gets nothing: its pending and delivered items become
// unsupported with the capability as reason.
func (s *Service) OnConnect(ctx context.Context, a store.Agent) ([]registry.Command, error) {
	if !s.cfg.Enabled {
		return nil, nil
	}
	items, err := s.repo.ListActiveModuleItemsForAgent(ctx, a.TenantID, a.ID, store.MaxReplayPerConnect)
	if err != nil {
		return nil, err
	}
	if c := Capability(true, &a, false); c != CapabilityEnabled {
		for _, it := range items {
			_, _, _ = s.update(ctx, a.TenantID, it.ID, func(cur *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
				if cur.State != store.DeliveryPending && cur.State != store.DeliveryDelivered {
					return nil, errNoChange
				}
				s.finish(cur, store.DeliveryUnsupported, c)
				return []store.AuditRow{s.itemRow(audit.ModuleDeliveryUnsupported, system, audit.OutcomeRefused, *cur, nil)}, nil
			})
		}
		return nil, nil
	}
	cmds := make([]registry.Command, 0, len(items))
	for _, it := range items {
		if it.State == store.DeliveryPending {
			s.markDelivered(ctx, it)
		}
		cmds = append(cmds, command(it))
	}
	return cmds, nil
}
