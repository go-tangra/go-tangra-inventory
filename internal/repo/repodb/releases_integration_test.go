//go:build integration

package repodb_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

type nopAudit struct{}

func (nopAudit) Record(context.Context, audit.Event) error { return nil }

// writeBundle writes a signed release <dir>/<version>/ with one artifact per
// platform of the given sizes.
func writeBundle(t *testing.T, dir, version string, priv ed25519.PrivateKey, sizes map[agentrelease.Platform]int) string {
	t.Helper()
	d := filepath.Join(dir, version)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	m := agentrelease.Manifest{Schema: 1, Version: version, CreatedAt: time.Now().UTC().Truncate(time.Second), KeyID: "it-key"}
	for p, size := range sizes {
		data := make([]byte, size)
		_, _ = rand.Read(data)
		name := "agent-" + p.OS + "-" + p.Arch + "-" + p.InstallType
		if err := os.WriteFile(filepath.Join(d, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		m.Artifacts = append(m.Artifacts, agentrelease.Artifact{OS: p.OS, Arch: p.Arch, InstallType: p.InstallType, File: name,
			Size: int64(size), SHA256: hex.EncodeToString(sum[:])})
	}
	b := m.Encode()
	_ = os.WriteFile(filepath.Join(d, agentrelease.ManifestFile), b, 0o644)
	_ = os.WriteFile(filepath.Join(d, agentrelease.SignatureFile), []byte(agentrelease.EncodeSignature(ed25519.Sign(priv, b))), 0o644)
	return d
}

// TestReleasesTwoReplicasAndLargeArtifact: two replicas seeding the same
// bundle concurrently store it once with complete artifacts (advisory lock);
// a 25 MiB artifact round-trips through 1 MiB chunks and verifies.
func TestReleasesTwoReplicasAndLargeArtifact(t *testing.T) {
	adminDSN, appDSN := startDB(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := agentrelease.Keyring{"it-key": pub}
	big := agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}
	dir := t.TempDir()
	writeBundle(t, dir, "4.4.0", priv, map[agentrelease.Platform]int{
		big: 25 << 20,
		{OS: "windows", Arch: "amd64", InstallType: "binary"}: 1<<20 + 17,
	})

	var wg sync.WaitGroup
	var dbs []*repodb.DB
	counts := make([]int, 2)
	for i := 0; i < 2; i++ {
		st, err := store.Open(ctx, appDSN, 4)
		if err != nil {
			t.Fatal(err)
		}
		defer st.Close()
		db := repodb.New(st)
		dbs = append(dbs, db)
		wg.Add(1)
		go func(i int, db *repodb.DB) {
			defer wg.Done()
			svc := releases.New(db, keys, nopAudit{}, slog.New(slog.DiscardHandler), releases.Config{BundleDir: dir, KeepVersions: 5, ChunkBytes: 1 << 20})
			counts[i] = svc.SeedBundles(ctx)
		}(i, db)
	}
	wg.Wait()
	if counts[0] != 1 || counts[1] != 1 {
		t.Fatalf("seed counts = %v", counts)
	}
	rels, err := dbs[0].ListAgentReleases(ctx)
	if err != nil || len(rels) != 1 || len(rels[0].Artifacts) != 2 {
		t.Fatalf("releases = %+v %v", rels, err)
	}
	for _, a := range rels[0].Artifacts {
		if !a.Complete {
			t.Fatalf("incomplete artifact %+v", a)
		}
	}
	svc := releases.New(dbs[1], keys, nopAudit{}, slog.New(slog.DiscardHandler), releases.Config{KeepVersions: 5, ChunkBytes: 1 << 20})
	st, err := svc.OpenArtifact(ctx, "4.4.0", big)
	if err != nil {
		t.Fatal(err)
	}
	h := st.Header()
	m, err := keys.Verify(h.Manifest, h.Signature, h.KeyID)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := m.Artifact(big)
	v := agentrelease.NewArtifactVerifier(a)
	chunks := 0
	for {
		_, data, err := st.Next(ctx)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > 1<<20 {
			t.Fatalf("chunk of %d bytes", len(data))
		}
		chunks++
		if _, err := v.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := v.Finish(); err != nil || chunks != 25 {
		t.Fatalf("round trip: %v after %d chunks", err, chunks)
	}
	// Same version with other content is a conflict; deletion cascades.
	other := t.TempDir()
	writeBundle(t, other, "4.4.0", priv, map[agentrelease.Platform]int{big: 10})
	if _, _, err := svc.ImportDir(ctx, filepath.Join(other, "4.4.0"), store.ReleaseImport); !errors.Is(err, repo.ErrConflict) {
		t.Fatalf("conflict = %v", err)
	}
	if err := dbs[0].DeleteAgentRelease(ctx, "4.4.0"); err != nil {
		t.Fatal(err)
	}
	if _, err := dbs[0].ReadArtifactChunk(ctx, store.AgentArtifact{Version: "4.4.0", OS: "linux", Arch: "amd64", InstallType: "deb"}, 0); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("chunks after delete = %v", err)
	}
	if err := dbs[0].DeleteAgentRelease(ctx, "4.4.0"); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("second delete = %v", err)
	}
	// A reader error aborts the whole import (nothing stored).
	failing := func(store.AgentArtifact) (io.ReadCloser, error) {
		return io.NopCloser(io.MultiReader(bytes.NewReader([]byte("abc")), errReader{})), nil
	}
	if _, err := dbs[0].ImportAgentRelease(ctx, store.AgentRelease{Version: "4.9.0", Manifest: []byte("m"), Signature: bytes.Repeat([]byte{1}, 64),
		KeyID: "it-key", ManifestSHA256: hex.EncodeToString(make([]byte, 32)), Source: store.ReleaseImport,
		Artifacts: []store.AgentArtifact{{OS: "linux", Arch: "amd64", InstallType: "rpm", File: "x.rpm", Size: 3, SHA256: hex.EncodeToString(make([]byte, 32))}}},
		failing, 1<<20); err == nil {
		t.Fatal("reader error ignored")
	}
	if rels, _ := dbs[0].ListAgentReleases(ctx); len(rels) != 0 {
		t.Fatalf("partial import stored: %+v", rels)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("disk read error") }
