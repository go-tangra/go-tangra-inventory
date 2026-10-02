package certdelivery

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// sweepBatch bounds the items one Sweep run handles.
const sweepBatch = 500

// Sweep fails fetched items whose agent did not report within the report
// timeout (failed/no_report) and expires active items whose delivery window
// passed (expired). Each item is re-checked under its row lock, so replicas
// sweeping at the same time change it once.
func (s *Service) Sweep(ctx context.Context) (expired, failed int, err error) {
	if !s.cfg.Enabled {
		return 0, 0, nil
	}
	now := s.now()
	reportBefore := now.Add(-s.cfg.ReportTimeout)
	stale, err := s.repo.ListStaleCertItems(ctx, now, reportBefore, sweepBatch)
	if err != nil {
		return 0, 0, err
	}
	for _, st := range stale {
		i, changed, err := s.update(ctx, st.Item.TenantID, st.Item.ID, func(cur *store.CertDeliveryItem) (repo.CertItemChange, error) {
			var t audit.EventType
			switch {
			case cur.State == store.DeliveryFetched && cur.UpdatedAt.Before(reportBefore):
				s.finish(cur, store.DeliveryFailed, store.ReasonNoReport)
				t = audit.CertDeliveryFailed
			case cur.Active() && st.ExpiresAt.Before(now):
				s.finish(cur, store.DeliveryExpired, store.ReasonExpired)
				t = audit.CertDeliveryExpired
			default:
				return repo.CertItemChange{}, errNoChange
			}
			return repo.CertItemChange{Audit: []store.AuditRow{s.itemRow(t, system, audit.OutcomeError, *cur, nil)}}, nil
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
