package releases

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// signer writes signed release bundles with an ephemeral key.
type signer struct {
	keyID string
	priv  ed25519.PrivateKey
	keys  agentrelease.Keyring
}

func newSigner(t testing.TB) signer {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return signer{keyID: "test-key", priv: priv, keys: agentrelease.Keyring{"test-key": pub}}
}

var platforms = []agentrelease.Platform{
	{OS: "linux", Arch: "amd64", InstallType: "deb"}, {OS: "linux", Arch: "amd64", InstallType: "rpm"},
	{OS: "linux", Arch: "amd64", InstallType: "binary"}, {OS: "windows", Arch: "amd64", InstallType: "binary"},
}

func content(version string, p agentrelease.Platform, size int) []byte {
	seed := []byte(version + p.String())
	return bytes.Repeat(seed, size/len(seed)+1)[:size]
}

// bundle writes <dir>/<version>/ with manifest, signature and artifacts
// (size bytes each); mutate may change the manifest before signing.
func (s signer) bundle(t testing.TB, dir, version string, size int, mutate func(*agentrelease.Manifest)) string {
	t.Helper()
	d := filepath.Join(dir, version)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	m := agentrelease.Manifest{Schema: 1, Version: version, CreatedAt: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC), KeyID: s.keyID}
	for i, p := range platforms {
		data := content(version, p, size)
		name := fmt.Sprintf("artifact-%d-%s", i, p.InstallType)
		if err := os.WriteFile(filepath.Join(d, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		m.Artifacts = append(m.Artifacts, agentrelease.Artifact{OS: p.OS, Arch: p.Arch, InstallType: p.InstallType, File: name,
			Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	if mutate != nil {
		mutate(&m)
	}
	b := m.Encode()
	if err := os.WriteFile(filepath.Join(d, agentrelease.ManifestFile), b, 0o644); err != nil {
		t.Fatal(err)
	}
	sig := agentrelease.EncodeSignature(ed25519.Sign(s.priv, b))
	if err := os.WriteFile(filepath.Join(d, agentrelease.SignatureFile), []byte(sig+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return d
}

// recorder captures audit events.
type recorder struct{ events []audit.Event }

func (r *recorder) Record(_ context.Context, e audit.Event) error {
	if err := audit.Validate(e); err != nil {
		return err
	}
	r.events = append(r.events, e)
	return nil
}

func (r *recorder) last() audit.Event { return r.events[len(r.events)-1] }

func newService(t testing.TB, s signer, mem *memstore.Mem, bundleDir string) (*Service, *recorder) {
	t.Helper()
	rec := &recorder{}
	svc := New(mem, s.keys, rec, slog.New(slog.DiscardHandler), Config{BundleDir: bundleDir, KeepVersions: 2, ChunkBytes: 64})
	return svc, rec
}

func TestSeedBundlesIdempotent(t *testing.T) {
	s := newSigner(t)
	dir := t.TempDir()
	s.bundle(t, dir, "4.4.0", 300, nil)
	s.bundle(t, dir, "4.5.0", 150, nil)
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("not a release"), 0o644); err != nil {
		t.Fatal(err)
	}
	mem := memstore.New()
	svc, rec := newService(t, s, mem, dir)
	if n := svc.SeedBundles(context.Background()); n != 2 {
		t.Fatalf("seeded = %d", n)
	}
	if svc.CurrentVersion(context.Background()) != "4.5.0" {
		t.Fatalf("current = %q", svc.CurrentVersion(context.Background()))
	}
	rels, err := svc.Releases(context.Background())
	if err != nil || len(rels) != 2 || rels[0].Version != "4.5.0" || len(rels[0].Artifacts) != 4 || rels[0].Source != store.ReleaseBundled {
		t.Fatalf("releases = %+v %v", rels, err)
	}
	if e := rec.last(); e.EventType != audit.AgentReleaseImported || e.TenantID != audit.PlatformTenant || e.Outcome != audit.OutcomeOK ||
		e.Details["source"] != store.ReleaseBundled || e.Details["key_id"] != "test-key" {
		t.Fatalf("audit = %+v", e)
	}
	// A second replica (or restart) seeding the same bundle stores nothing new.
	before := len(rec.events)
	svc2, rec2 := newService(t, s, mem, dir)
	if n := svc2.SeedBundles(context.Background()); n != 2 {
		t.Fatalf("reseeded = %d", n)
	}
	if len(rec2.events) != 0 || len(rec.events) != before {
		t.Fatalf("an unchanged bundle must not be re-imported: %+v", rec2.events)
	}
	if svc2.CurrentVersion(context.Background()) != "4.5.0" {
		t.Fatal("current version after reseed")
	}
}

func TestImportRefusals(t *testing.T) {
	s := newSigner(t)
	other := newSigner(t)
	other.keyID = "other-key"
	cases := map[string]func(t *testing.T, dir string) string{
		"invalid signature": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			sig, _ := os.ReadFile(filepath.Join(d, agentrelease.SignatureFile))
			sig[3] ^= 1
			_ = os.WriteFile(filepath.Join(d, agentrelease.SignatureFile), sig, 0o644)
			return d
		},
		"unknown key": func(t *testing.T, dir string) string { return other.bundle(t, dir, "4.4.0", 100, nil) },
		"missing artifact": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			_ = os.Remove(filepath.Join(d, "artifact-2-binary"))
			return d
		},
		"size mismatch": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			f, _ := os.OpenFile(filepath.Join(d, "artifact-0-deb"), os.O_APPEND|os.O_WRONLY, 0o644)
			_, _ = f.Write([]byte("x"))
			_ = f.Close()
			return d
		},
		"sha mismatch": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			b, _ := os.ReadFile(filepath.Join(d, "artifact-1-rpm"))
			b[5] ^= 1
			_ = os.WriteFile(filepath.Join(d, "artifact-1-rpm"), b, 0o644)
			return d
		},
		"symlinked artifact": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			target := filepath.Join(dir, "elsewhere")
			b, _ := os.ReadFile(filepath.Join(d, "artifact-3-binary"))
			_ = os.WriteFile(target, b, 0o644)
			_ = os.Remove(filepath.Join(d, "artifact-3-binary"))
			_ = os.Symlink(target, filepath.Join(d, "artifact-3-binary"))
			return d
		},
		"symlinked manifest": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			_ = os.Rename(filepath.Join(d, agentrelease.ManifestFile), filepath.Join(dir, "m.json"))
			_ = os.Symlink(filepath.Join(dir, "m.json"), filepath.Join(d, agentrelease.ManifestFile))
			return d
		},
		"symlinked release dir": func(t *testing.T, dir string) string {
			d := s.bundle(t, filepath.Join(dir, "real"), "4.4.0", 100, nil)
			link := filepath.Join(dir, "4.4.0")
			_ = os.Symlink(d, link)
			return link
		},
		"oversized manifest": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			_ = os.WriteFile(filepath.Join(d, agentrelease.ManifestFile), bytes.Repeat([]byte(" "), agentrelease.MaxManifestBytes+1), 0o644)
			return d
		},
		"missing signature": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			_ = os.Remove(filepath.Join(d, agentrelease.SignatureFile))
			return d
		},
		"directory named after another version": func(t *testing.T, dir string) string {
			d := s.bundle(t, dir, "4.4.0", 100, nil)
			renamed := filepath.Join(dir, "4.9.9")
			_ = os.Rename(d, renamed)
			return renamed
		},
	}
	for name, build := range cases {
		t.Run(name, func(t *testing.T) {
			mem := memstore.New()
			svc, rec := newService(t, s, mem, "")
			d := build(t, t.TempDir())
			if _, _, err := svc.ImportDir(context.Background(), d, store.ReleaseBundled); err == nil {
				t.Fatal("bundle accepted")
			}
			if rels, _ := mem.ListAgentReleases(context.Background()); len(rels) != 0 {
				t.Fatalf("refused bundle stored: %+v", rels)
			}
			if len(rec.events) != 1 || rec.last().Outcome != audit.OutcomeRefused || rec.last().EventType != audit.AgentReleaseImported {
				t.Fatalf("refusal not audited: %+v", rec.events)
			}
		})
	}
}

func TestImportConflictAndStoreErrors(t *testing.T) {
	s := newSigner(t)
	mem := memstore.New()
	svc, rec := newService(t, s, mem, "")
	d := s.bundle(t, t.TempDir(), "4.4.0", 100, nil)
	if _, imported, err := svc.ImportDir(context.Background(), d, store.ReleaseImport); err != nil || !imported {
		t.Fatalf("import = %v %v", imported, err)
	}
	// Same version, different content: conflict, refused.
	d2 := s.bundle(t, t.TempDir(), "4.4.0", 101, nil)
	if _, _, err := svc.ImportDir(context.Background(), d2, store.ReleaseImport); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("conflict = %v", err)
	}
	if rec.last().Outcome != audit.OutcomeRefused {
		t.Fatal("conflict not audited as refused")
	}
	mem.FailNext("ImportAgentRelease")
	d3 := s.bundle(t, t.TempDir(), "4.6.0", 100, nil)
	if _, _, err := svc.ImportDir(context.Background(), d3, store.ReleaseImport); err == nil {
		t.Fatal("store error swallowed")
	}
	// Seeding logs and skips a broken bundle, keeps going.
	dir := t.TempDir()
	s.bundle(t, dir, "4.7.0", 100, nil)
	bad := s.bundle(t, dir, "4.8.0", 100, nil)
	_ = os.Remove(filepath.Join(bad, agentrelease.SignatureFile))
	svc2, _ := newService(t, s, mem, dir)
	if n := svc2.SeedBundles(context.Background()); n != 1 || svc2.CurrentVersion(context.Background()) != "4.7.0" {
		t.Fatalf("seeded = %d current = %q", n, svc2.CurrentVersion(context.Background()))
	}
	// Missing bundle directory: nothing seeded, no error.
	svc3, _ := newService(t, s, mem, filepath.Join(dir, "absent"))
	if n := svc3.SeedBundles(context.Background()); n != 0 {
		t.Fatalf("absent dir seeded %d", n)
	}
	svc4, _ := newService(t, s, mem, "")
	if n := svc4.SeedBundles(context.Background()); n != 0 {
		t.Fatalf("no dir seeded %d", n)
	}
}

func TestVerifyingReaderRejectsChangedFile(t *testing.T) {
	// The file changes between verification and storage: the import aborts.
	s := newSigner(t)
	mem := memstore.New()
	svc, _ := newService(t, s, mem, "")
	d := s.bundle(t, t.TempDir(), "4.4.0", 100, nil)
	svc.beforeStore = func() {
		b, _ := os.ReadFile(filepath.Join(d, "artifact-0-deb"))
		b[0] ^= 1
		_ = os.WriteFile(filepath.Join(d, "artifact-0-deb"), b, 0o644)
	}
	if _, _, err := svc.ImportDir(context.Background(), d, store.ReleaseImport); err == nil {
		t.Fatal("file changed after verification was stored")
	}
	if rels, _ := mem.ListAgentReleases(context.Background()); len(rels) != 0 {
		t.Fatal("partial release stored")
	}
	svc.beforeStore = func() { _ = os.Remove(filepath.Join(d, "artifact-1-rpm")) }
	if _, _, err := svc.ImportDir(context.Background(), d, store.ReleaseImport); err == nil {
		t.Fatal("file removed after verification was stored")
	}
}

func TestCurrentVersionWithoutBundle(t *testing.T) {
	s := newSigner(t)
	mem := memstore.New()
	svc, _ := newService(t, s, mem, "")
	if v := svc.CurrentVersion(context.Background()); v != "" {
		t.Fatalf("no release: %q", v)
	}
	for _, v := range []string{"4.4.0", "4.10.0", "4.9.0"} {
		if _, _, err := svc.ImportDir(context.Background(), s.bundle(t, t.TempDir(), v, 50, nil), store.ReleaseImport); err != nil {
			t.Fatal(err)
		}
	}
	if v := svc.CurrentVersion(context.Background()); v != "4.10.0" {
		t.Fatalf("newest stored = %q", v)
	}
	mem.FailNext("ListAgentReleases")
	if v := svc.CurrentVersion(context.Background()); v != "" {
		t.Fatalf("store error = %q", v)
	}
}

func TestRetention(t *testing.T) {
	s := newSigner(t)
	mem := memstore.New()
	dir := t.TempDir()
	for _, v := range []string{"4.4.0", "4.5.0", "4.6.0", "4.7.0", "4.8.0"} {
		s.bundle(t, dir, v, 50, nil)
	}
	svc, _ := newService(t, s, mem, "")
	for _, v := range []string{"4.4.0", "4.5.0", "4.6.0", "4.7.0", "4.8.0"} {
		if _, _, err := svc.ImportDir(context.Background(), filepath.Join(dir, v), store.ReleaseImport); err != nil {
			t.Fatal(err)
		}
	}
	// Bundled (platform current) 4.6.0; a tenant pins 4.4.0; an active
	// request targets 4.5.0.
	svc.bundled = []string{"4.6.0"}
	if _, err := mem.UpdateUpgradePolicy(context.Background(), "t1", func(p *store.AgentUpgradePolicy) ([]store.AuditRow, error) {
		p.TargetVersion = "4.4.0"
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := mem.CreateAgentUpgrade(context.Background(), store.AgentUpgrade{ID: "u1", TenantID: "t1", AgentID: "a1", TargetVersion: "4.5.0",
		State: store.UpgradePending}, store.AuditRow{}); err != nil {
		t.Fatal(err)
	}
	deleted, err := svc.Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// keep_versions 2 keeps 4.8.0 and 4.7.0; 4.6.0, 4.5.0, 4.4.0 are protected.
	if len(deleted) != 0 {
		t.Fatalf("deleted = %v", deleted)
	}
	svc.bundled = []string{"4.8.0"}
	_ = mem.CreateAgentUpgrade // request stays active
	if _, err := mem.UpdateUpgradePolicy(context.Background(), "t1", func(p *store.AgentUpgradePolicy) ([]store.AuditRow, error) {
		p.TargetVersion = ""
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	deleted, err = svc.Prune(context.Background())
	if err != nil || strings.Join(deleted, ",") != "4.6.0,4.4.0" {
		t.Fatalf("deleted = %v %v", deleted, err)
	}
	mem.FailNext("ProtectedAgentVersions")
	if _, err := svc.Prune(context.Background()); err == nil {
		t.Fatal("store error swallowed")
	}
	mem.FailNext("ListAgentReleases")
	if _, err := svc.Prune(context.Background()); err == nil {
		t.Fatal("store error swallowed")
	}
	mem.FailNext("DeleteAgentRelease")
	svc.cfg.KeepVersions = 1
	if _, err := svc.Prune(context.Background()); err == nil {
		t.Fatal("delete error swallowed")
	}
}

func TestArtifactStream(t *testing.T) {
	s := newSigner(t)
	mem := memstore.New()
	svc, _ := newService(t, s, mem, "")
	if _, _, err := svc.ImportDir(context.Background(), s.bundle(t, t.TempDir(), "4.4.0", 200, nil), store.ReleaseImport); err != nil {
		t.Fatal(err)
	}
	p := platforms[2]
	st, err := svc.OpenArtifact(context.Background(), "4.4.0", p)
	if err != nil {
		t.Fatal(err)
	}
	h := st.Header()
	m, err := s.keys.Verify(h.Manifest, h.Signature, h.KeyID)
	if err != nil {
		t.Fatalf("header does not verify: %v", err)
	}
	if h.Artifact.File != "artifact-2-binary" || h.Artifact.Size != 200 {
		t.Fatalf("header = %+v", h.Artifact)
	}
	var got []byte
	var offsets []int64
	for {
		off, chunk, err := st.Next(context.Background())
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		offsets = append(offsets, off)
		got = append(got, chunk...)
	}
	a, _ := m.Artifact(p)
	v := agentrelease.NewArtifactVerifier(a)
	_, _ = v.Write(got)
	if err := v.Finish(); err != nil {
		t.Fatalf("streamed artifact: %v", err)
	}
	if len(offsets) != 4 || offsets[0] != 0 || offsets[1] != 64 || offsets[3] != 192 {
		t.Fatalf("offsets = %v", offsets)
	}
	if _, err := svc.OpenArtifact(context.Background(), "9.9.9", p); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("unknown version = %v", err)
	}
	if _, err := svc.OpenArtifact(context.Background(), "4.4.0", agentrelease.Platform{OS: "linux", Arch: "arm64", InstallType: "deb"}); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("missing platform = %v", err)
	}
	if !svc.HasArtifact(context.Background(), "4.4.0", p) || svc.HasArtifact(context.Background(), "4.4.0", agentrelease.Platform{OS: "linux", Arch: "arm64", InstallType: "deb"}) {
		t.Fatal("HasArtifact")
	}
	// A chunk read error surfaces; a missing chunk before the declared size
	// is an error, not a short artifact.
	st, _ = svc.OpenArtifact(context.Background(), "4.4.0", p)
	mem.FailNext("ReadArtifactChunk")
	if _, _, err := st.Next(context.Background()); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("read error = %v", err)
	}
	short := &Stream{svc: svc, header: Header{Artifact: store.AgentArtifact{Version: "4.4.0", OS: "linux", Arch: "amd64", InstallType: "binary", Size: 10_000}}}
	for i := 0; i < 4; i++ {
		_, _, _ = short.Next(context.Background())
	}
	if _, _, err := short.Next(context.Background()); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("truncated stored artifact = %v", err)
	}
	mem.FailNext("GetAgentRelease")
	if _, err := svc.OpenArtifact(context.Background(), "4.4.0", p); err == nil {
		t.Fatal("store error swallowed")
	}
}

func TestReleaseAndInfo(t *testing.T) {
	s := newSigner(t)
	mem := memstore.New()
	svc, _ := newService(t, s, mem, "")
	if _, _, err := svc.ImportDir(context.Background(), s.bundle(t, t.TempDir(), "4.4.0", 50, nil), store.ReleaseImport); err != nil {
		t.Fatal(err)
	}
	r, err := svc.Release(context.Background(), "4.4.0")
	if err != nil || r.KeyID != "test-key" || len(r.Signature) != ed25519.SignatureSize {
		t.Fatalf("release = %+v %v", r, err)
	}
	mem.FailNext("ListAgentReleases")
	if _, err := svc.Releases(context.Background()); err == nil {
		t.Fatal("store error swallowed")
	}
	if svc.KeyIDs()[0] != "test-key" {
		t.Fatal("key ids")
	}
}
