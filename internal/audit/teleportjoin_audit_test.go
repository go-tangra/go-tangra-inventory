package audit

import (
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestTeleportJoinVocabulary (feature 038): every Teleport join event is
// known, the teleport_join subject is valid and its details are held to the
// strict rule (no PEM, no value over 256 bytes, no token/secret keys).
func TestTeleportJoinVocabulary(t *testing.T) {
	for _, name := range []string{
		"teleport_join_created", "teleport_join_superseded", "teleport_join_delivered", "teleport_join_fetched",
		"teleport_join_installing", "teleport_join_joined", "teleport_join_failed", "teleport_join_unsupported",
		"teleport_join_expired", "teleport_join_cancelled", "teleport_join_refused",
	} {
		if !Known(name) {
			t.Errorf("%s not in the vocabulary", name)
		}
	}
	e := Event{TenantID: "t1", EventType: TeleportJoinFetched, ActorKind: ActorAgent, ActorID: "a1",
		SubjectKind: SubjectTeleportJoin, SubjectID: "i1", Outcome: OutcomeOK, Details: map[string]any{"cluster_id": "c1"}}
	if err := Validate(e); err != nil {
		t.Fatalf("teleport_join subject: %v", err)
	}
	e.Details = map[string]any{"detail": strings.Repeat("d", 257)}
	if err := Validate(e); err == nil {
		t.Fatal("over-long teleport detail accepted")
	}
}

func TestTeleportItemRow(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	i := store.TeleportJoinItem{ID: "i1", TenantID: "t1", Source: "teleport", EnrollmentID: "e1", HostID: "h1", AgentID: "a1",
		ClusterID: "c1", State: store.TeleportJoined, NodeHostUUID: "5b0c7a4e-2f61-4c1e-9d3a-0e4f6a7b8c9d"}
	r := TeleportItemRow(TeleportJoinJoined, ActorAgent, "a1", OutcomeOK, i,
		map[string]any{"fetches": 1, "join_token": "secret-value", "note": "-----BEGIN PRIVATE KEY-----"}, now)
	if r.Action != "teleport_join_joined" || r.SubjectKind != "teleport_join" || r.SubjectID != "i1" || r.Reason != "" ||
		r.TenantID != "t1" || !r.At.Equal(now) || r.Outcome != OutcomeOK {
		t.Fatalf("row = %+v", r)
	}
	for k, want := range map[string]any{"enrollment_id": "e1", "item_id": "i1", "host_id": "h1", "agent_id": "a1", "source": "teleport",
		"cluster_id": "c1", "state": "joined", "reason": "", "fetches": 1, "node_host_uuid": i.NodeHostUUID} {
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
	i.NodeHostUUID = ""
	if r := TeleportItemRow(TeleportJoinFailed, ActorSystem, SystemActor, OutcomeError, i, nil, now); r.Detail["node_host_uuid"] != nil {
		t.Fatalf("empty node uuid recorded: %+v", r.Detail)
	}
}
