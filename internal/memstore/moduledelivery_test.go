package memstore

import (
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestModuleDeliveryContract runs the shared storage contract (also run by
// the repodb integration suite) against the in-memory store (feature 037).
func TestModuleDeliveryContract(t *testing.T) {
	var cur *Mem
	open := func(t *testing.T) repo.Store {
		cur = New()
		return cur
	}
	audits := func(t *testing.T, tenantID, action, subjectID string) int {
		n := 0
		for _, r := range cur.AuditRows() {
			if r.TenantID == tenantID && r.Action == action && r.SubjectID == subjectID {
				n++
			}
		}
		return n
	}
	repotest.ModuleDeliveryContract(t, open, audits)
}

func TestModuleDeliveryFailNext(t *testing.T) {
	tid := repotest.TenantA
	calls := map[string]func(m *Mem) error{
		"CreateModuleItem": func(m *Mem) error {
			_, e := m.CreateModuleItem(ctx(), repo.NewModuleItem{Item: store.ModuleDeliveryItem{ID: "i", TenantID: tid}})
			return e
		},
		"GetModuleItem":           func(m *Mem) error { _, e := m.GetModuleItem(ctx(), tid, "i"); return e },
		"GetModuleItemByDelivery": func(m *Mem) error { _, e := m.GetModuleItemByDelivery(ctx(), tid, "gateway", "d"); return e },
		"UpdateModuleItem": func(m *Mem) error {
			_, e := m.UpdateModuleItem(ctx(), tid, "i", func(*store.ModuleDeliveryItem) ([]store.AuditRow, error) { return nil, nil })
			return e
		},
		"ListActiveModuleItemsForAgent": func(m *Mem) error { _, e := m.ListActiveModuleItemsForAgent(ctx(), tid, "a", 0); return e },
		"ListStaleModuleItems":          func(m *Mem) error { _, e := m.ListStaleModuleItems(ctx(), time.Now(), time.Now(), 0); return e },
		"PurgeModuleItems":              func(m *Mem) error { _, e := m.PurgeModuleItems(ctx(), time.Now()); return e },
	}
	for name, call := range calls {
		m := New()
		m.FailNext(name)
		if err := call(m); err == nil {
			t.Errorf("%s: injected failure not returned", name)
		}
	}
}
