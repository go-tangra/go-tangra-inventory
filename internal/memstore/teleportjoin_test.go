package memstore

import (
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repotest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestTeleportJoinContract runs the shared storage contract (also run by
// the repodb integration suite) against the in-memory store (feature 038).
func TestTeleportJoinContract(t *testing.T) {
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
	repotest.TeleportJoinContract(t, open, audits)
}

func TestTeleportJoinFailNext(t *testing.T) {
	tid := repotest.TenantA
	calls := map[string]func(m *Mem) error{
		"CreateTeleportItem": func(m *Mem) error {
			_, e := m.CreateTeleportItem(ctx(), repo.NewTeleportItem{Item: store.TeleportJoinItem{ID: "i", TenantID: tid}})
			return e
		},
		"GetTeleportItem":             func(m *Mem) error { _, e := m.GetTeleportItem(ctx(), tid, "i"); return e },
		"GetTeleportItemByEnrollment": func(m *Mem) error { _, e := m.GetTeleportItemByEnrollment(ctx(), tid, "teleport", "e"); return e },
		"UpdateTeleportItem": func(m *Mem) error {
			_, e := m.UpdateTeleportItem(ctx(), tid, "i", func(*store.TeleportJoinItem) ([]store.AuditRow, error) { return nil, nil })
			return e
		},
		"ListActiveTeleportItemsForAgent": func(m *Mem) error { _, e := m.ListActiveTeleportItemsForAgent(ctx(), tid, "a", 0); return e },
		"ListStaleTeleportItems":          func(m *Mem) error { _, e := m.ListStaleTeleportItems(ctx(), time.Now(), time.Now(), 0); return e },
		"PurgeTeleportItems":              func(m *Mem) error { _, e := m.PurgeTeleportItems(ctx(), time.Now()); return e },
	}
	for name, call := range calls {
		m := New()
		m.FailNext(name)
		if err := call(m); err == nil {
			t.Errorf("%s: injected failure not returned", name)
		}
	}
}
