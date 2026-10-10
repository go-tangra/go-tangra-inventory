package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestModuleDeliveryVocabulary (feature 037, T011): every module delivery
// event is known, the module_delivery subject is valid and its details are
// held to the strict rule (no PEM, no value over 256 bytes, no
// token/secret keys).
func TestModuleDeliveryVocabulary(t *testing.T) {
	for _, name := range []string{
		"module_delivery_created", "module_delivery_superseded", "module_delivery_delivered", "module_delivery_fetched",
		"module_delivery_installed", "module_delivery_failed", "module_delivery_hook_failed", "module_delivery_unsupported",
		"module_delivery_expired", "module_delivery_refused",
	} {
		if !Known(name) {
			t.Errorf("%s not in the vocabulary", name)
		}
	}
	e := Event{TenantID: "t1", EventType: ModuleDeliveryFetched, ActorKind: ActorAgent, ActorID: "a1",
		SubjectKind: SubjectModuleDelivery, SubjectID: "i1", Outcome: OutcomeOK, Details: map[string]any{"module": "sms-gw"}}
	if err := Validate(e); err != nil {
		t.Fatalf("module_delivery subject: %v", err)
	}
	e.Details = map[string]any{"detail": strings.Repeat("d", 257)}
	if err := Validate(e); err == nil {
		t.Fatal("over-long module detail accepted")
	}
}

func TestModuleItemRow(t *testing.T) {
	now := time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC)
	i := store.ModuleDeliveryItem{ID: "i1", TenantID: "t1", Source: "gateway", DeliveryID: "d1", HostID: "h1", AgentID: "a1",
		Module: "sms-gw", Version: "1.2.0", State: store.DeliveryFailed, Reason: store.ReasonFetchBudget}
	r := ModuleItemRow(ModuleDeliveryFailed, ActorSystem, SystemActor, OutcomeError, i,
		map[string]any{"fetches": 5, "join_token": "secret-value", "note": "-----BEGIN PRIVATE KEY-----"}, now)
	if r.Action != "module_delivery_failed" || r.SubjectKind != "module_delivery" || r.SubjectID != "i1" || r.Reason != "fetch_budget" ||
		r.TenantID != "t1" || !r.At.Equal(now) || r.Outcome != OutcomeError {
		t.Fatalf("row = %+v", r)
	}
	for k, want := range map[string]any{"delivery_id": "d1", "item_id": "i1", "host_id": "h1", "agent_id": "a1", "source": "gateway",
		"module": "sms-gw", "version": "1.2.0", "state": "failed", "reason": "fetch_budget", "fetches": 5} {
		if r.Detail[k] != want {
			t.Errorf("detail[%s] = %v, want %v", k, r.Detail[k], want)
		}
	}
	if _, ok := r.Detail["join_token"]; ok {
		t.Fatal("token key kept")
	}
	if _, ok := r.Detail["note"]; ok {
		t.Fatal("PEM value kept")
	}
}
