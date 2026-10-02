package certdelivery

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Report is an agent's result for one item.
type Report struct {
	ItemID       string
	State        string // installed | unchanged | failed | hook_failed
	Serial       string
	Fingerprint  string // sha256 of the leaf written on disk, lowercase hex
	Reason       string // agent reason code
	HookExitCode int    // -1 = hook not run; 0..255; 256 = timeout
	Detail       string // <= 256 bytes after sanitising; never hook output
}

var reportEvents = map[string]struct {
	t       audit.EventType
	outcome string
}{
	store.DeliveryInstalled:  {audit.CertDeliveryInstalled, audit.OutcomeOK},
	store.DeliveryUnchanged:  {audit.CertDeliveryUnchanged, audit.OutcomeOK},
	store.DeliveryFailed:     {audit.CertDeliveryFailed, audit.OutcomeError},
	store.DeliveryHookFailed: {audit.CertDeliveryHookFailed, audit.OutcomeError},
}

var agentReasons = func() map[string]bool {
	m := map[string]bool{}
	for _, r := range store.AgentDeliveryReasons {
		m[r] = true
	}
	return m
}()

// sanitize keeps at most store.MaxDetailBytes of printable text (control
// characters become spaces); text carrying PEM is dropped entirely.
func sanitize(d string) string {
	if strings.Contains(d, "-----BEGIN") {
		return ""
	}
	d = strings.ToValidUTF8(d, "?")
	d = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, d)
	for len(d) > store.MaxDetailBytes {
		_, size := utf8.DecodeLastRuneInString(d)
		d = d[:len(d)-size]
	}
	return d
}

// checkReport validates the closed-set fields of an agent report.
func checkReport(r Report) error {
	_, known := reportEvents[r.State]
	switch {
	case !uuidRE.MatchString(r.ItemID):
		return invalid("item_id")
	case !known:
		return invalid("state")
	case r.Reason != "" && !agentReasons[r.Reason]:
		return invalid("reason")
	case r.Fingerprint != "" && !fingerprintRE.MatchString(r.Fingerprint):
		return invalid("fingerprint_sha256")
	case !serialRE.MatchString(r.Serial):
		return invalid("serial")
	case r.HookExitCode < -1 || r.HookExitCode > 256:
		return invalid("hook_exit_code")
	}
	return nil
}

// errIgnored rolls back a report for an item that is no longer active;
// errRepeated one that repeats the report the item already holds.
var (
	errIgnored  = errors.New("certdelivery: report ignored")
	errRepeated = errors.New("certdelivery: report repeated")
)

// Report records agent a's result for its own item (data-model §1.4):
// installed and unchanged require the fingerprint served at fetch (else the
// item fails with fingerprint_mismatch); every accepted report updates the
// host's certificate under the name in the same transaction. A repeated
// identical report is accepted without a change (idempotent); any other
// report for an item that is no longer active is ignored (accepted false).
func (s *Service) Report(ctx context.Context, a store.Agent, r Report) (bool, error) {
	if !s.cfg.Enabled {
		return false, ErrDisabled
	}
	if err := checkReport(r); err != nil {
		return false, err
	}
	it, err := s.ownItem(ctx, a, r.ItemID, false)
	if err != nil {
		return false, err
	}
	d, _, err := s.repo.GetCertDelivery(ctx, a.TenantID, it.DeliveryID)
	if err != nil {
		return false, err
	}
	prev, err := s.repo.GetHostCertificate(ctx, a.TenantID, it.HostID, it.Name)
	hasPrev := err == nil
	if err != nil && !errors.Is(err, repo.ErrNotFound) {
		return false, err
	}
	detail := sanitize(r.Detail)
	_, _, err = s.update(ctx, a.TenantID, it.ID, func(cur *store.CertDeliveryItem) (repo.CertItemChange, error) {
		if !cur.Active() {
			if cur.State == r.State && cur.FingerprintSHA256 == r.Fingerprint && (cur.Reason == r.Reason || r.Reason == "") {
				return repo.CertItemChange{}, errRepeated // the agent repeats a report whose answer it lost
			}
			return repo.CertItemChange{}, errIgnored
		}
		state, reason := r.State, r.Reason
		done := state == store.DeliveryInstalled || state == store.DeliveryUnchanged
		if done && (cur.FingerprintSHA256 == "" || r.Fingerprint != cur.FingerprintSHA256) {
			state, reason = store.DeliveryFailed, store.ReasonFingerprintMismatch
			done = false
		}
		if state == store.DeliveryHookFailed && reason == "" {
			reason = store.ReasonHookFailed
		}
		code := r.HookExitCode
		s.finish(cur, state, reason)
		cur.HookExitCode, cur.Detail = &code, detail
		now := *cur.FinishedAt

		hc := store.HostCertificate{HostID: cur.HostID, Name: cur.Name, CertificateID: cur.CertificateID,
			ConfigurationID: d.ConfigurationID, State: state, Reason: reason, HookExitCode: &code, LastItemID: cur.ID, UpdatedAt: now}
		if hasPrev {
			hc.CertificateID, hc.ConfigurationID, hc.CommonName, hc.Serial = prev.CertificateID, prev.ConfigurationID, prev.CommonName, prev.Serial
			hc.FingerprintSHA256, hc.NotAfter, hc.LastDeliveredAt, hc.RevokedAt = prev.FingerprintSHA256, prev.NotAfter, prev.LastDeliveredAt, prev.RevokedAt
		}
		renewal := hasPrev && prev.FingerprintSHA256 != "" && prev.FingerprintSHA256 != cur.FingerprintSHA256
		if done {
			if hc.CertificateID != cur.CertificateID {
				hc.CommonName, hc.RevokedAt = "", nil
			}
			hc.CertificateID, hc.ConfigurationID, hc.Serial = cur.CertificateID, d.ConfigurationID, cur.Serial
			hc.FingerprintSHA256, hc.NotAfter, hc.LastDeliveredAt = cur.FingerprintSHA256, cur.NotAfter, &now
		}
		ev := reportEvents[state]
		extra := map[string]any{"hook_exit_code": code, "serial": cur.Serial, "fingerprint_sha256": cur.FingerprintSHA256}
		if state == store.DeliveryInstalled {
			extra["is_renewal"] = renewal
		}
		if reason == store.ReasonFingerprintMismatch {
			extra["reported_fingerprint_sha256"] = r.Fingerprint
		}
		return repo.CertItemChange{HostCert: &hc, Audit: []store.AuditRow{s.itemRow(ev.t, agentActor(a), ev.outcome, *cur, extra)}}, nil
	})
	switch {
	case errors.Is(err, errRepeated):
		return true, nil
	case errors.Is(err, errIgnored):
		return false, nil
	}
	return err == nil, err
}
