//go:build integration

package repodb_test

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	proof "github.com/go-tangra/go-tangra-inventory/sdk/v4/pkg/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestAutoEnrollDB: keys and the switch are stored per tenant (RLS) with
// canonical networks; 50 concurrent enrollments against a key limited to 10
// accept exactly 10; a nonce is accepted once; deleting a key drops its
// nonces; the agent row records how it enrolled.
func TestAutoEnrollDB(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, appDSN, 16)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := repodb.New(st)
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	svc := autoenroll.New(db, env)
	const tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	const other = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	actor := autoenroll.Actor{Kind: "user", ID: "u1"}

	if s, err := svc.Settings(ctx, tenant); err != nil || s.Enabled {
		t.Fatalf("default %+v %v", s, err)
	}
	if _, err := svc.SetEnabled(ctx, tenant, actor, true); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SetEnabled(ctx, tenant, actor, true); err != nil { // upsert
		t.Fatal(err)
	}
	k, secret, err := svc.CreateKey(ctx, tenant, actor, autoenroll.KeyInput{Name: "lab", AllowedCIDRs: []string{"10.0.0.0/8", "2001:db8::/32"}, MaxEnrollments: 10})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.CreateKey(ctx, tenant, actor, autoenroll.KeyInput{Name: "lab", AllowedCIDRs: []string{"10.0.0.0/8"}}); !errors.Is(err, autoenroll.ErrConflict) {
		t.Fatalf("duplicate name %v", err)
	}
	keys, err := svc.Keys(ctx, tenant)
	if err != nil || len(keys) != 1 || keys[0].AllowedCIDRs[1] != "2001:db8::/32" || keys[0].SecretSealed != nil {
		t.Fatalf("keys %+v %v", keys, err)
	}
	if ok, _ := svc.Keys(ctx, other); len(ok) != 0 {
		t.Fatal("RLS: other tenant sees the key")
	}

	var accepted, rejected atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := store.Identity{Hostname: "h", HardwareUUID: "hw", MachineID: "m"}
			p, _ := proof.NewProof(secret, k.KeyID, proof.Identity{HardwareUUID: "hw", MachineID: "m", Hostname: "h"}, time.Now())
			_, _, err := svc.Enroll(ctx, p, id, "4.4.0", netip.MustParseAddr("10.0.0.9"))
			switch {
			case err == nil:
				accepted.Add(1)
			case errors.Is(err, autoenroll.ErrRejected):
				rejected.Add(1)
			default:
				t.Errorf("enroll %d: %v", i, err)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != 10 || rejected.Load() != 40 {
		t.Fatalf("accepted %d rejected %d", accepted.Load(), rejected.Load())
	}
	keys, _ = svc.Keys(ctx, tenant)
	if keys[0].Enrollments != 10 || keys[0].LastUsedIP != "10.0.0.9" || keys[0].LastUsedAt == nil {
		t.Fatalf("counters %+v", keys[0])
	}

	// Replay: raise the limit, then send the same proof twice.
	max := 0
	if _, err := svc.UpdateKey(ctx, tenant, actor, k.ID, autoenroll.KeyPatch{MaxEnrollments: &max}); err != nil {
		t.Fatal(err)
	}
	id := store.Identity{Hostname: "v6", HardwareUUID: "hw6"}
	p, _ := proof.NewProof(secret, k.KeyID, proof.Identity{HardwareUUID: "hw6", Hostname: "v6"}, time.Now())
	agentID, _, err := svc.Enroll(ctx, p, id, "4.4.0", netip.MustParseAddr("2001:db8::7"))
	if err != nil {
		t.Fatalf("v6 enroll %v", err)
	}
	if _, _, err := svc.Enroll(ctx, p, id, "4.4.0", netip.MustParseAddr("2001:db8::7")); !errors.Is(err, autoenroll.ErrRejected) {
		t.Fatalf("replay accepted: %v", err)
	}
	a, err := db.GetAgent(ctx, tenant, agentID)
	if err != nil || a.EnrolledVia != store.EnrolledViaAuto || a.AutoEnrollKeyID != k.KeyID {
		t.Fatalf("agent %+v %v", a, err)
	}
	// Tenant switch off: refused even with a fresh proof.
	if _, err := svc.SetEnabled(ctx, tenant, actor, false); err != nil {
		t.Fatal(err)
	}
	p2, _ := proof.NewProof(secret, k.KeyID, proof.Identity{HardwareUUID: "hw6", Hostname: "v6"}, time.Now())
	if _, _, err := svc.Enroll(ctx, p2, id, "", netip.MustParseAddr("10.1.1.1")); !errors.Is(err, autoenroll.ErrRejected) {
		t.Fatal("enrolled with the switch off")
	}
	// Rotate + delete; audit rows were written in the same transactions.
	if _, _, err := svc.RotateKey(ctx, tenant, actor, k.ID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteKey(ctx, other, actor, k.ID); !errors.Is(err, autoenroll.ErrNotFound) {
		t.Fatalf("cross-tenant delete %v", err)
	}
	if err := svc.DeleteKey(ctx, tenant, actor, k.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.LookupAutoEnrollKey(ctx, k.KeyID); err == nil {
		t.Fatal("deleted key still found")
	}
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	rs, err := conn.Query(ctx, `SELECT action, count(*) FROM inventory_audit_events WHERE tenant_id=$1 GROUP BY action`, tenant)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for rs.Next() {
		var a string
		var n int
		if err := rs.Scan(&a, &n); err != nil {
			t.Fatal(err)
		}
		seen[a] = n
	}
	rs.Close()
	if seen["agent_auto_enrolled"] != 11 || seen["auto_enroll_key_created"] != 1 || seen["auto_enroll_key_deleted"] != 1 || seen["auto_enroll_key_rotated"] != 1 || seen["auto_enroll_refused"] == 0 {
		t.Fatalf("audit actions %v", seen)
	}
}
