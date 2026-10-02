package audit

import (
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

// CertItemCancelledRow is the audit row of an item the inventory cancelled
// itself (host deletion, agent revocation) inside the storage transaction.
// Its detail carries identifiers only, so it needs no validation.
func CertItemCancelledRow(i store.CertDeliveryItem, now time.Time) store.AuditRow {
	return store.AuditRow{ID: store.NewID(), TenantID: i.TenantID, At: now, ActorKind: ActorSystem, ActorID: SystemActor,
		Action: string(CertDeliveryCancelled), SubjectKind: SubjectCertDelivery, SubjectID: i.ID, Outcome: OutcomeOK,
		Reason: i.Reason, Detail: guardMap(CertItemDetail(i), true)}
}
