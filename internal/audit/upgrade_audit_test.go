package audit

import (
	"testing"
	"time"
)

func TestUpgradeVocabulary(t *testing.T) {
	for _, name := range []string{
		"agent_upgrade_requested", "agent_upgrade_cancelled", "agent_upgrade_delivered", "agent_upgrade_started",
		"agent_upgrade_installing", "agent_upgrade_succeeded", "agent_upgrade_failed", "agent_upgrade_rolled_back",
		"agent_upgrade_expired", "agent_upgrade_refused", "upgrade_policy_updated", "upgrade_policy_paused",
		"upgrade_policy_resumed", "agent_release_imported",
	} {
		if !Known(name) {
			t.Errorf("%s not in the vocabulary", name)
		}
	}
	e := Event{TenantID: "t1", EventType: UpgradePolicyUpdated, ActorKind: ActorUser, ActorID: "u", SubjectKind: SubjectUpgradePolicy, SubjectID: "t1", Outcome: OutcomeOK}
	if err := Validate(e); err != nil {
		t.Fatalf("upgrade_policy subject: %v", err)
	}
}

func TestPlatformTenantOnlyForReleaseImports(t *testing.T) {
	e := Event{TenantID: PlatformTenant, EventType: AgentReleaseImported, ActorKind: ActorSystem, ActorID: "inventorysvc",
		SubjectKind: SubjectRelease, SubjectID: "4.4.0", Outcome: OutcomeOK}
	if err := Validate(e); err != nil {
		t.Fatalf("release import in platform scope: %v", err)
	}
	e.EventType = AgentUpgradeRequested
	if err := Validate(e); err == nil {
		t.Fatal("platform tenant accepted for a tenant event")
	}
}

func TestRowForTransactionalWrites(t *testing.T) {
	now := time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)
	row, err := Row(Event{TenantID: "t1", EventType: AgentUpgradeFailed, ActorKind: ActorAgent, ActorID: "a1", SubjectKind: SubjectAgent,
		SubjectID: "a1", Outcome: OutcomeError, Reason: "checksum_mismatch",
		Details: map[string]any{"request_id": "r1", "from_version": "4.4.0", "to_version": "4.5.0", "credential": "x"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if row.ID == "" || !row.At.Equal(now) || row.Action != "agent_upgrade_failed" || row.Reason != "checksum_mismatch" ||
		row.Detail["request_id"] != "r1" || row.Detail["to_version"] != "4.5.0" {
		t.Fatalf("row = %+v", row)
	}
	if _, ok := row.Detail["credential"]; ok {
		t.Fatal("guarded key kept")
	}
	if _, err := Row(Event{EventType: "nope"}, now); err == nil {
		t.Fatal("invalid event accepted")
	}
}
