package audit

import (
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// SystemActor is the actor id of transitions the inventory makes itself.
const SystemActor = "inventory"

// CertItemDetail is the common detail of a certificate delivery item row
// (contracts/audit-events.md): identifiers, state and reason only.
func CertItemDetail(i store.CertDeliveryItem) map[string]any {
	return map[string]any{"delivery_id": i.DeliveryID, "item_id": i.ID, "host_id": i.HostID, "agent_id": i.AgentID,
		"name": i.Name, "certificate_id": i.CertificateID, "state": i.State, "reason": i.Reason}
}

// SafeRow builds the audit row of e at now for callers whose event type,
// actor, subject and outcome are constants (certificate delivery
// transitions written inside a storage transaction): the detail guard is
// applied and, as defence in depth (SC-003), detail values carrying PEM are
// dropped instead of failing the transition.
func SafeRow(e Event, now time.Time) store.AuditRow {
	d := guard(e)
	for k, v := range d {
		if s, ok := v.(string); ok && strings.Contains(s, pemMarker) {
			delete(d, k)
		}
	}
	return store.AuditRow{ID: store.NewID(), TenantID: e.TenantID, At: now, ActorKind: e.ActorKind, ActorID: e.ActorID,
		Action: string(e.EventType), SubjectKind: e.SubjectKind, SubjectID: e.SubjectID, Outcome: e.Outcome,
		Reason: e.Reason, Detail: d}
}

// CertItemRow is the audit row of a delivery item transition: the common
// item detail plus extra.
func CertItemRow(t EventType, actorKind, actorID, outcome string, i store.CertDeliveryItem, extra map[string]any, now time.Time) store.AuditRow {
	d := CertItemDetail(i)
	for k, v := range extra {
		d[k] = v
	}
	return SafeRow(Event{TenantID: i.TenantID, EventType: t, ActorKind: actorKind, ActorID: actorID, SubjectKind: SubjectCertDelivery,
		SubjectID: i.ID, Outcome: outcome, Reason: i.Reason, Details: d}, now)
}

// CertItemCancelledRow is the audit row of an item the inventory cancelled
// itself (host deletion, agent revocation) inside the storage transaction.
func CertItemCancelledRow(i store.CertDeliveryItem, now time.Time) store.AuditRow {
	return CertItemRow(CertDeliveryCancelled, ActorSystem, SystemActor, OutcomeOK, i, nil, now)
}
