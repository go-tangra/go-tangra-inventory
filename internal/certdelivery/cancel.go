package certdelivery

import (
	"context"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Cancel cancels an active item on behalf of a user (cancelled_by_user);
// ErrNotCancellable once it is terminal, repo.ErrNotFound for another
// tenant's or a missing item.
func (s *Service) Cancel(ctx context.Context, tenantID string, actor Actor, itemID string) (store.CertDeliveryItem, error) {
	if !s.cfg.Enabled {
		return store.CertDeliveryItem{}, ErrDisabled
	}
	if !uuidRE.MatchString(itemID) {
		return store.CertDeliveryItem{}, repo.ErrNotFound
	}
	i, err := s.repo.UpdateCertItem(ctx, tenantID, itemID, func(cur *store.CertDeliveryItem) (repo.CertItemChange, error) {
		if !cur.Active() {
			return repo.CertItemChange{}, ErrNotCancellable
		}
		s.finish(cur, store.DeliveryCancelled, store.ReasonCancelledByUser)
		return repo.CertItemChange{Audit: []store.AuditRow{s.itemRow(audit.CertDeliveryCancelled, actor, audit.OutcomeOK, *cur, nil)}}, nil
	})
	if err != nil {
		return store.CertDeliveryItem{}, err
	}
	s.committed(ctx, i)
	return i, nil
}

// List pages a tenant's items (reads work while the relay is disabled).
func (s *Service) List(ctx context.Context, tenantID string, f repo.CertItemFilter, req listquery.Request) ([]store.CertDeliveryItem, int, listquery.Request, error) {
	return s.repo.ListCertItemsPage(ctx, tenantID, f, req)
}

// HostCertificates lists a host's current certificates (reads work while
// the relay is disabled).
func (s *Service) HostCertificates(ctx context.Context, tenantID, hostID string) ([]store.HostCertificate, error) {
	return s.repo.ListHostCertificates(ctx, tenantID, hostID)
}
