package teleportjoin

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// sweepBatch bounds the items one Sweep run handles.
const sweepBatch = 500

// Sweep fails fetched and installing items whose agent did not report
// within the report timeout (failed/report_timeout) and expires pending,
// delivered and fetched items past their expiry (expired). An installing
// item is not expired: its agent is joining with material it already
// holds. Each item is re-checked under its row lock, so replicas sweeping
// at the same time change it once.
func (s *Service) Sweep(ctx context.Context) (expired, failed int, err error) {
	if !s.cfg.Enabled {
		return 0, 0, nil
	}
	now := s.now()
	reportBefore := now.Add(-s.cfg.ReportTimeout)
	stale, err := s.repo.ListStaleTeleportItems(ctx, now, reportBefore, sweepBatch)
	if err != nil {
		return 0, 0, err
	}
	for _, st := range stale {
		i, changed, err := s.update(ctx, st.TenantID, st.ID, func(cur *store.TeleportJoinItem) ([]store.AuditRow, error) {
			var t audit.EventType
			switch {
			case (cur.State == store.DeliveryFetched || cur.State == store.TeleportInstalling) && cur.UpdatedAt.Before(reportBefore):
				s.finish(cur, store.DeliveryFailed, store.ReasonReportTimeout)
				t = audit.TeleportJoinFailed
			case fetchable(cur.State) && cur.ExpiresAt.Before(now):
				s.finish(cur, store.DeliveryExpired, "")
				t = audit.TeleportJoinExpired
			default:
				return nil, errNoChange
			}
			return []store.AuditRow{s.itemRow(t, system, audit.OutcomeError, *cur, nil)}, nil
		})
		switch {
		case err != nil:
			return expired, failed, err
		case !changed:
		case i.State == store.DeliveryExpired:
			expired++
		default:
			failed++
		}
	}
	return expired, failed, nil
}
