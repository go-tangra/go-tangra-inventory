package certdelivery

import (
	"context"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Verification statuses (VerifyHostCertificates).
const (
	StatusMatch    = "match"
	StatusMismatch = "mismatch"
	StatusFailed   = "failed"
	StatusPending  = "pending"
	StatusMissing  = "missing"
	StatusRevoked  = "revoked"
)

// HostStatus is the verification of one host.
type HostStatus struct {
	HostID          string
	Hostname        string
	Status          string
	Fingerprint     string // last reported
	Serial          string
	Reason          string
	LastDeliveredAt *time.Time
}

// hostStatus applies the order of contracts/inventory-grpc.md §1: match,
// revoked, pending, failed, mismatch, missing.
func hostStatus(hc store.HostCertificate, has, active bool, expected string) string {
	switch {
	case has && hc.RevokedAt == nil && hc.FingerprintSHA256 == expected &&
		(hc.State == store.DeliveryInstalled || hc.State == store.DeliveryUnchanged):
		return StatusMatch
	case has && hc.RevokedAt != nil:
		return StatusRevoked
	case active:
		return StatusPending
	case has && (hc.State == store.DeliveryFailed || hc.State == store.DeliveryHookFailed):
		return StatusFailed
	case has && hc.FingerprintSHA256 != "":
		return StatusMismatch
	}
	return StatusMissing
}

// Verify compares what the selected hosts reported under name with the
// expected fingerprint.
func (s *Service) Verify(ctx context.Context, tenantID string, ids, tags []string, name, fingerprint string) (hosts []HostStatus, matched int, err error) {
	if !s.cfg.Enabled {
		return nil, 0, ErrDisabled
	}
	if ids, err = checkSelector(ids, tags); err != nil {
		return nil, 0, err
	}
	switch {
	case !certmaterial.ValidName(name):
		return nil, 0, invalid("name")
	case !fingerprintRE.MatchString(fingerprint):
		return nil, 0, invalid("expected_fingerprint_sha256")
	}
	sel, err := s.resolve(ctx, tenantID, ids, tags)
	if err != nil {
		return nil, 0, err
	}
	if len(sel.targets) > store.MaxDeliveryHosts {
		return nil, 0, ErrTooManyHosts
	}
	hcs, err := s.repo.ListHostCertificatesByName(ctx, tenantID, name)
	if err != nil {
		return nil, 0, err
	}
	actives, err := s.repo.ListActiveCertItemsByName(ctx, tenantID, name)
	if err != nil {
		return nil, 0, err
	}
	byHost := map[string]store.HostCertificate{}
	for _, hc := range hcs {
		byHost[hc.HostID] = hc
	}
	active := map[string]bool{}
	for _, i := range actives {
		active[i.HostID] = true
	}
	hosts = make([]HostStatus, 0, len(sel.targets))
	for _, t := range sel.targets {
		hc, has := byHost[t.host.ID]
		st := HostStatus{HostID: t.host.ID, Hostname: t.host.Hostname, Status: hostStatus(hc, has, active[t.host.ID], fingerprint),
			Fingerprint: hc.FingerprintSHA256, Serial: hc.Serial, Reason: hc.Reason, LastDeliveredAt: hc.LastDeliveredAt}
		if st.Status == StatusMatch {
			matched++
		}
		hosts = append(hosts, st)
	}
	return hosts, matched, nil
}
