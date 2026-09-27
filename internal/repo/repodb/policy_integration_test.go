//go:build integration

package repodb_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

// TestUpgradePolicyAndScheduler: the policy is stored per tenant (RLS) with
// transactional audit rows; two replicas running the scheduler at once never
// exceed max_concurrent; a failed policy request pauses the policy until it
// is resumed.
func TestUpgradePolicyAndScheduler(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	const tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	const other = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := agentrelease.Keyring{"it-key": pub}
	dir := t.TempDir()
	deb := agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}
	writeBundle(t, dir, "4.5.0", priv, map[agentrelease.Platform]int{deb: 1000})

	reg := registry.NewMemory()
	var svcs []*upgrades.Service
	var db0 *repodb.DB
	for i := 0; i < 2; i++ {
		st, err := store.Open(ctx, appDSN, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		db := repodb.New(st)
		rel := releases.New(db, keys, nopAudit{}, slog.New(slog.DiscardHandler), releases.Config{BundleDir: dir, KeepVersions: 5, ChunkBytes: 1 << 20})
		rel.SeedBundles(ctx)
		svc := upgrades.New(db, rel, reg, events.HubPublisher{}, upgrades.Config{RequestTTL: time.Hour, ProgressTimeout: 15 * time.Minute})
		svc.OnFinished(svc.PauseOnFailure)
		svcs = append(svcs, svc)
		if i == 0 {
			db0 = db
		}
	}
	for i := 1; i <= 5; i++ {
		id := fmt.Sprintf("018f0000-0000-7000-8000-%012d", i)
		if err := db0.CreateAgent(ctx, store.Agent{ID: id, TenantID: tenant, AgentVersion: "4.4.0", CredentialSealed: []byte("sealed"), EnrolledAt: time.Now(), LastSeen: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if err := db0.SetAgentPlatform(ctx, id, "linux", "amd64", "deb", []string{store.CapUpgradeV1}, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, _, err := reg.Register(ctx, registry.ConnectedAgent{AgentID: id, TenantID: tenant, Version: "4.4.0"}); err != nil {
			t.Fatal(err)
		}
	}

	admin := upgrades.Actor{Kind: upgrades.ActorUser, ID: "admin-1"}
	// Whole-day window: the test does not depend on the time of day.
	p, err := svcs[0].UpdatePolicy(ctx, tenant, admin, upgrades.PolicyInput{Enabled: true, WindowStart: "00:00", WindowEnd: "00:00",
		Timezone: "Europe/Sofia", MaxConcurrent: 2, TargetVersion: "4.5.0"})
	if err != nil || !p.Enabled || p.UpdatedBy != "admin-1" {
		t.Fatalf("update = %+v %v", p, err)
	}
	if got, found, err := db0.GetUpgradePolicy(ctx, tenant); err != nil || !found || got.Timezone != "Europe/Sofia" || got.TargetVersion != "4.5.0" {
		t.Fatalf("stored = %+v %v %v", got, found, err)
	}
	if got, found, _ := db0.GetUpgradePolicy(ctx, other); found || got.Enabled {
		t.Fatalf("other tenant sees the policy: %+v", got)
	}
	if pols, err := db0.ListEnabledUpgradePolicies(ctx); err != nil || len(pols) != 1 || pols[0].TenantID != tenant {
		t.Fatalf("enabled = %+v %v", pols, err)
	}

	// Two replicas at once: max_concurrent holds across them.
	var wg sync.WaitGroup
	created := make([]int, 2)
	for i, svc := range svcs {
		wg.Add(1)
		go func(i int, svc *upgrades.Service) {
			defer wg.Done()
			n, err := svc.RunPolicies(ctx)
			if err != nil {
				t.Error(err)
			}
			created[i] = n
		}(i, svc)
	}
	wg.Wait()
	list, err := svcs[0].List(ctx, tenant, repo.UpgradeFilter{})
	if err != nil || created[0]+created[1] != 2 || len(list) != 2 {
		t.Fatalf("created %v, requests %d (%v)", created, len(list), err)
	}
	for _, u := range list {
		if u.Origin != store.OriginPolicy || u.RequestedBy != upgrades.PolicyActorID {
			t.Fatalf("request = %+v", u)
		}
	}
	// Lock held by another replica: skipped.
	acquired, err := db0.TryTenantLock(ctx, upgrades.PolicyLockKey(tenant), func(ctx context.Context) error {
		ok, err := db0.TryTenantLock(ctx, upgrades.PolicyLockKey(tenant), func(context.Context) error { return nil })
		if ok || err != nil {
			return fmt.Errorf("nested lock acquired=%v err=%v", ok, err)
		}
		return nil
	})
	if !acquired || err != nil {
		t.Fatalf("lock = %v %v", acquired, err)
	}

	// A failed policy request pauses the policy (audited in the same transaction).
	a, _ := db0.GetAgent(ctx, tenant, list[0].AgentID)
	for _, state := range []string{upgrades.StateDownloading, upgrades.StateInstalling, upgrades.StateFailed} {
		if _, err := svcs[0].Report(ctx, a, upgrades.Report{RequestID: list[0].ID, State: state, FromVersion: "4.4.0", ToVersion: "4.5.0", Reason: reasonFor(state)}); err != nil {
			t.Fatal(err)
		}
	}
	if got, _, _ := db0.GetUpgradePolicy(ctx, tenant); !got.Paused || got.PausedReason != "failed:"+list[0].ID {
		t.Fatalf("after failure = %+v", got)
	}
	if n, err := svcs[1].RunPolicies(ctx); err != nil || n != 0 {
		t.Fatalf("paused run = %d %v", n, err)
	}
	if _, err := svcs[1].ResumePolicy(ctx, tenant, admin); err != nil {
		t.Fatal(err)
	}
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	var n int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM inventory_audit_events WHERE tenant_id=$1 AND subject_kind='upgrade_policy'
		AND action IN ('upgrade_policy_updated','upgrade_policy_paused','upgrade_policy_resumed')`, tenant).Scan(&n); err != nil || n != 3 {
		t.Fatalf("policy audit rows = %d %v", n, err)
	}
	if n, err := svcs[0].RunPolicies(ctx); err != nil || n != 1 {
		t.Fatalf("after resume = %d %v", n, err)
	}
}

func reasonFor(state string) string {
	if state == upgrades.StateFailed {
		return "install_failed"
	}
	return ""
}
