package upgrades

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Negative security tests (T108, research.md STRIDE).

func refusals(f *fixture) []store.AuditRow {
	var out []store.AuditRow
	for _, r := range f.mem.AuditRows() {
		if r.Action == "agent_upgrade_refused" {
			out = append(out, r)
		}
	}
	return out
}

// Forged request ids: another agent's, another tenant's or a made-up id is
// NotFound for reports and downloads (no oracle) and audited as refused.
func TestSecurityForgedRequestIDs(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enroll(t, "victim", "4.4.0", deb, store.CapUpgradeV1)
	attacker := f.enroll(t, "attacker", "4.4.0", deb, store.CapUpgradeV1)
	res, _ := f.svc.Request(ctx, tenant, user, []string{"victim"}, false)
	victimReq := res.Created[0].ID

	// Another tenant's agent with the same request id.
	foreign := store.Agent{ID: "foreign", TenantID: "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c99", AgentVersion: "4.4.0", OS: "linux", Arch: "amd64", InstallType: "deb"}
	for _, a := range []store.Agent{attacker, foreign} {
		for _, id := range []string{victimReq, "00000000-0000-7000-8000-999999999999"} {
			if _, err := f.svc.AuthorizeDownload(ctx, a, id, "4.5.0"); !errors.Is(err, repo.ErrNotFound) {
				t.Errorf("download %s by %s = %v", id, a.ID, err)
			}
			if _, err := f.svc.Report(ctx, a, Report{RequestID: id, State: StateFailed, Reason: "install_failed"}); !errors.Is(err, repo.ErrNotFound) {
				t.Errorf("report %s by %s = %v", id, a.ID, err)
			}
		}
	}
	if got := f.upgrade(t, victimReq); got.State != store.UpgradePending || got.Reason != "" {
		t.Fatalf("victim request changed: %+v", got)
	}
	ref := refusals(f)
	if len(ref) != 8 {
		t.Fatalf("refusals = %d, want 8", len(ref))
	}
	for _, r := range ref {
		if r.Outcome != audit.OutcomeRefused {
			t.Fatalf("refusal row = %+v", r)
		}
	}
	// Without a request id only the agent's own current version (rollback package) is served.
	if v, err := f.svc.AuthorizeDownload(ctx, attacker, "", "4.4.0"); err != nil || v != "4.4.0" {
		t.Fatalf("own version = %q %v", v, err)
	}
	if _, err := f.svc.AuthorizeDownload(ctx, attacker, "", "4.5.0"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("other version without request = %v", err)
	}
}

// A report replayed from another agent's connection cannot move the request;
// a replay by the owner after the terminal state is ignored.
func TestSecurityReplayedReport(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	other := f.enroll(t, "a2", "4.4.0", deb, store.CapUpgradeV1)
	res, _ := f.svc.Request(ctx, tenant, user, []string{"a1"}, false)
	id := res.Created[0].ID
	report(t, f, "a1", id, StateDownloading, "")
	report(t, f, "a1", id, StateInstalling, "")
	report(t, f, "a1", id, StateFailed, "install_failed")
	replay := Report{RequestID: id, State: StateFailed, Reason: "install_failed"}
	if _, err := f.svc.Report(ctx, other, replay); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("replay by another agent = %v", err)
	}
	if ok := report(t, f, "a1", id, StateFailed, "install_failed"); ok {
		t.Fatal("replayed terminal report accepted")
	}
	if ok := report(t, f, "a1", id, StateDownloading, ""); ok {
		t.Fatal("report after the terminal state accepted")
	}
}

// succeeded is accepted only when the reporting connection runs the target.
func TestSecuritySucceededWithWrongVersion(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	a := f.enroll(t, "a1", "4.4.0", deb, store.CapUpgradeV1)
	res, _ := f.svc.Request(ctx, tenant, user, []string{"a1"}, false)
	id := res.Created[0].ID
	report(t, f, "a1", id, StateDownloading, "")
	report(t, f, "a1", id, StateInstalling, "")
	// Claims the target version but still connects as 4.4.0.
	if ok, err := f.svc.Report(ctx, a, Report{RequestID: id, State: StateSucceeded, FromVersion: "4.4.0", ToVersion: "4.5.0"}); ok || err != nil {
		t.Fatalf("succeeded from 4.4.0 = %v %v", ok, err)
	}
	// Runs 4.5.0 but claims another target.
	a.AgentVersion = "4.5.0"
	if ok, err := f.svc.Report(ctx, a, Report{RequestID: id, State: StateSucceeded, FromVersion: "4.4.0", ToVersion: "4.6.0"}); ok || err != nil {
		t.Fatalf("succeeded with another target = %v %v", ok, err)
	}
	if got := f.upgrade(t, id); got.State != store.UpgradeInstalling {
		t.Fatalf("request = %+v", got)
	}
	// Unknown reason codes and oversized details are rejected outright.
	for _, r := range []Report{
		{RequestID: id, State: StateFailed, Reason: "rm -rf /"},
		{RequestID: id, State: StateFailed, Reason: "install_failed", Detail: strings.Repeat("x", 300)},
		{RequestID: id, State: "hacked"},
	} {
		if _, err := f.svc.Report(ctx, a, r); !errors.Is(err, ErrInvalid) {
			t.Errorf("report %+v = %v", r, err)
		}
	}
}

// A request storm stays bounded: more than MaxBatch ids are refused before
// any work, MaxBatch unknown ids create nothing.
func TestSecurityRequestStormBounded(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	ids := make([]string, MaxBatch+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("00000000-0000-7000-8000-%012d", i)
	}
	if _, err := f.svc.Request(ctx, tenant, user, ids, false); !errors.Is(err, ErrInvalid) {
		t.Fatalf("%d ids = %v", len(ids), err)
	}
	start := time.Now()
	res, err := f.svc.Request(ctx, tenant, user, ids[:MaxBatch], false)
	if err != nil || len(res.Created) != 0 || len(res.Skipped) != MaxBatch {
		t.Fatalf("storm = %d created, %d skipped, %v", len(res.Created), len(res.Skipped), err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("1000 unknown ids took %s", time.Since(start))
	}
	if len(f.mem.AuditRows()) != 0 {
		t.Fatal("skipped ids wrote audit rows")
	}
}

type memAudit struct{ f *fixture }

func (a memAudit) Record(ctx context.Context, e audit.Event) error {
	row, err := audit.Row(e, t0)
	if err != nil {
		return err
	}
	return a.f.mem.AppendAudit(ctx, row)
}

// signedBundle writes <dir>/<version> with the given manifest mutation and
// artifact content.
func signedBundle(t *testing.T, dir, version string, priv ed25519.PrivateKey, data []byte, mut func(*agentrelease.Manifest, *[]byte)) string {
	t.Helper()
	d := filepath.Join(dir, version)
	_ = os.MkdirAll(d, 0o755)
	_ = os.WriteFile(filepath.Join(d, "a.deb"), data, 0o644)
	sum := sha256.Sum256(data)
	m := agentrelease.Manifest{Schema: 1, Version: version, CreatedAt: t0, KeyID: "k",
		Artifacts: []agentrelease.Artifact{{OS: "linux", Arch: "amd64", InstallType: "deb", File: "a.deb", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
	b := m.Encode()
	if mut != nil {
		mut(&m, &b)
	}
	_ = os.WriteFile(filepath.Join(d, agentrelease.ManifestFile), b, 0o644)
	_ = os.WriteFile(filepath.Join(d, agentrelease.SignatureFile), []byte(agentrelease.EncodeSignature(ed25519.Sign(priv, b))), 0o644)
	return d
}

// Bundles with an oversized manifest, an artifact above 150 MiB or an
// artifact larger than signed are refused (and audited), even when signed.
func TestSecurityHostileBundles(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	rel := releases.New(f.mem, agentrelease.Keyring{"k": pub}, memAudit{f}, slog.New(slog.DiscardHandler), releases.Config{KeepVersions: 5, ChunkBytes: 65536})
	dir := t.TempDir()
	cases := map[string]string{
		"oversized manifest": signedBundle(t, dir, "4.6.0", priv, []byte("agent"), func(_ *agentrelease.Manifest, b *[]byte) {
			*b = append((*b)[:len(*b)-1], []byte(`,"pad":"`+strings.Repeat("x", agentrelease.MaxManifestBytes)+`"}`)...)
		}),
		"artifact above 150 MiB": signedBundle(t, dir, "4.6.1", priv, []byte("agent"), func(m *agentrelease.Manifest, b *[]byte) {
			m.Artifacts[0].Size = agentrelease.MaxArtifactSize + 1
			*b = m.Encode()
		}),
		"artifact larger than signed": signedBundle(t, dir, "4.6.2", priv, []byte("agent plus appended payload"), func(m *agentrelease.Manifest, b *[]byte) {
			m.Artifacts[0].Size = 5
			*b = m.Encode()
		}),
	}
	want := map[string]string{"oversized manifest": "larger than", "artifact above 150 MiB": "size", "artifact larger than signed": "size"}
	for name, d := range cases {
		if _, _, err := rel.ImportDir(ctx, d, store.ReleaseImport); err == nil || !strings.Contains(err.Error(), want[name]) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if rels, _ := f.mem.ListAgentReleases(ctx); len(rels) != 0 {
		t.Fatalf("hostile bundle stored: %+v", rels)
	}
	refused := 0
	for _, r := range f.mem.AuditRows() {
		if r.Action == "agent_release_imported" && r.Outcome != audit.OutcomeOK {
			refused++
		}
	}
	if refused != len(cases) {
		t.Fatalf("refused imports audited = %d, want %d", refused, len(cases))
	}
}
