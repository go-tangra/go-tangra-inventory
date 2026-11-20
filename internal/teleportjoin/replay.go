package teleportjoin

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// OnConnect returns the TELEPORT_JOIN commands to send to agent a, which
// just opened its command stream with the capabilities it announced: its
// pending, delivered and fetched items, oldest first, at most
// store.MaxReplayPerConnect; pending ones are recorded as delivered.
// Delivered and fetched items are sent again (the agent deduplicates by
// item id and attempt; a restarted agent fetches fresh material within the
// fetch budget). Installing items are not replayed: the agent already
// holds their material, and a silent one fails at the report timeout. An
// agent that no longer announces teleport.v1 gets nothing: its pending and
// delivered items become unsupported with the capability as reason.
func (s *Service) OnConnect(ctx context.Context, a store.Agent) ([]registry.Command, error) {
	if !s.cfg.Enabled {
		return nil, nil
	}
	items, err := s.repo.ListActiveTeleportItemsForAgent(ctx, a.TenantID, a.ID, store.MaxReplayPerConnect)
	if err != nil {
		return nil, err
	}
	if c := Capability(true, &a, false); c != CapabilityEnabled {
		for _, it := range items {
			_, _, _ = s.update(ctx, a.TenantID, it.ID, func(cur *store.TeleportJoinItem) ([]store.AuditRow, error) {
				if cur.State != store.DeliveryPending && cur.State != store.DeliveryDelivered {
					return nil, errNoChange
				}
				s.finish(cur, store.DeliveryUnsupported, c)
				return []store.AuditRow{s.itemRow(audit.TeleportJoinUnsupported, system, audit.OutcomeRefused, *cur, nil)}, nil
			})
		}
		return nil, nil
	}
	cmds := make([]registry.Command, 0, len(items))
	for _, it := range items {
		switch it.State {
		case store.TeleportInstalling:
			continue
		case store.DeliveryPending:
			s.markDelivered(ctx, it)
		}
		cmds = append(cmds, command(it))
	}
	return cmds, nil
}
