package audit

import (
	"strings"
	"testing"
	"time"
)

// TestCertDeliveryVocabulary (T014): every type of
// contracts/audit-events.md is known and the cert_delivery subject is valid.
func TestCertDeliveryVocabulary(t *testing.T) {
	for _, name := range []string{
		"cert_delivery_requested", "cert_delivery_rearmed", "cert_delivery_delivered", "cert_delivery_fetched",
		"cert_delivery_installed", "cert_delivery_unchanged", "cert_delivery_failed", "cert_delivery_hook_failed",
		"cert_delivery_unsupported", "cert_delivery_superseded", "cert_delivery_expired", "cert_delivery_cancelled",
		"cert_delivery_refused", "host_certificate_revoked",
	} {
		if !Known(name) {
			t.Errorf("%s not in the vocabulary", name)
		}
	}
	e := certEvent(map[string]any{"delivery_id": "d1", "item_id": "i1", "state": "fetched", "has_key": true})
	if err := Validate(e); err != nil {
		t.Fatalf("cert_delivery subject: %v", err)
	}
	h := Event{TenantID: "t1", EventType: HostCertificateRevoked, ActorKind: ActorService, ActorID: "deployer",
		SubjectKind: SubjectHost, SubjectID: "h1", Outcome: OutcomeOK, Details: map[string]any{"certificate_id": "c1", "name": "www"}}
	if err := Validate(h); err != nil {
		t.Fatalf("host_certificate_revoked: %v", err)
	}
}

func certEvent(d map[string]any) Event {
	return Event{TenantID: "t1", EventType: CertDeliveryFetched, ActorKind: ActorAgent, ActorID: "a1",
		SubjectKind: SubjectCertDelivery, SubjectID: "i1", Outcome: OutcomeOK, Details: d}
}

// TestCertDeliveryPEMGuard (SC-003, defence in depth): a detail value that
// contains PEM is refused for every event type, at any depth; for
// certificate delivery events a value over the 256-byte cap is refused
// instead of being truncated.
func TestCertDeliveryPEMGuard(t *testing.T) {
	pemValue := "-----BEGIN PRIVATE KEY-----\nMIIB\n-----END PRIVATE KEY-----"
	for name, d := range map[string]map[string]any{
		"top level":     {"detail": pemValue},
		"nested":        {"x": map[string]any{"y": pemValue}},
		"in list":       {"x": []any{"ok", pemValue}},
		"cert block":    {"note": "prefix -----BEGIN CERTIFICATE----- suffix"},
		"too long":      {"detail": strings.Repeat("d", 257)},
		"nested length": {"x": map[string]any{"y": strings.Repeat("d", 300)}},
	} {
		if err := Validate(certEvent(d)); err == nil {
			t.Errorf("%s accepted", name)
		}
		if _, err := Row(certEvent(d), time.Now()); err == nil {
			t.Errorf("%s: Row accepted", name)
		}
	}
	// PEM is refused for every event type; long values of other events keep
	// the existing truncation.
	other := Event{TenantID: "t1", EventType: HostUpdated, ActorKind: ActorUser, ActorID: "u", SubjectKind: SubjectHost, SubjectID: "h",
		Outcome: OutcomeOK, Details: map[string]any{"note": pemValue}}
	if err := Validate(other); err == nil {
		t.Fatal("PEM accepted in a host event")
	}
	other.Details = map[string]any{"note": strings.Repeat("n", 300)}
	if row, err := Row(other, time.Now()); err != nil || len(row.Detail["note"].(string)) != 256 {
		t.Fatalf("truncation of other events changed: %v", err)
	}
	if err := Validate(certEvent(map[string]any{"detail": strings.Repeat("d", 256)})); err != nil {
		t.Fatalf("256 bytes refused: %v", err)
	}
}

// TestCertDeliverySerialKept: the certificate serial is public identity and
// kept for certificate delivery events, while the hardware-serial guard
// still drops serial keys of every other subject.
func TestCertDeliverySerialKept(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	row, err := Row(certEvent(map[string]any{"serial": "4f3a", "fingerprint_sha256": "9c1d", "credential": "x", "system_serial": "s"}), now)
	if err != nil {
		t.Fatal(err)
	}
	if row.Detail["serial"] != "4f3a" || row.Detail["fingerprint_sha256"] != "9c1d" {
		t.Fatalf("certificate identity dropped: %v", row.Detail)
	}
	for _, k := range []string{"credential", "system_serial"} {
		if _, ok := row.Detail[k]; ok {
			t.Fatalf("guarded key %s kept", k)
		}
	}
	host := Event{TenantID: "t1", EventType: HostUpdated, ActorKind: ActorUser, ActorID: "u", SubjectKind: SubjectHost, SubjectID: "h",
		Outcome: OutcomeOK, Details: map[string]any{"serial": "hw-123"}}
	if row, _ := Row(host, now); row.Detail["serial"] != nil {
		t.Fatal("hardware serial kept for a host event")
	}
}
