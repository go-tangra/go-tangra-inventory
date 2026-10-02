package certdelivery

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// MarkRevoked handles a revocation forwarded by a mesh source: active items
// of the certificate are cancelled (certificate_revoked) and the hosts
// holding it are flagged (revoked_at); files on hosts are never removed.
// Idempotent: a repeated call cancels and flags nothing.
func (s *Service) MarkRevoked(ctx context.Context, tenantID string, actor Actor, certificateID string) (cancelled, flagged int, err error) {
	if !s.cfg.Enabled {
		return 0, 0, ErrDisabled
	}
	if !validRef(certificateID, 1) {
		return 0, 0, invalid("certificate_id")
	}
	items, err := s.repo.CancelCertItems(ctx, tenantID, repo.CertCancelScope{CertificateID: certificateID}, store.ReasonCertificateRevoked,
		func(i store.CertDeliveryItem) store.AuditRow {
			return s.itemRow(audit.CertDeliveryCancelled, actor, audit.OutcomeOK, i, nil)
		})
	if err != nil {
		return 0, 0, err
	}
	for _, i := range items {
		s.committed(ctx, i)
	}
	hcs, err := s.repo.RevokeHostCertificates(ctx, tenantID, certificateID, s.now(), func(h store.HostCertificate) store.AuditRow {
		return audit.SafeRow(audit.Event{TenantID: tenantID, EventType: audit.HostCertificateRevoked, ActorKind: actor.Kind, ActorID: actor.ID,
			SubjectKind: audit.SubjectHost, SubjectID: h.HostID, Outcome: audit.OutcomeOK,
			Details: map[string]any{"certificate_id": certificateID, "name": h.Name}}, s.now())
	})
	if err != nil {
		return len(items), 0, err
	}
	return len(items), len(hcs), nil
}
