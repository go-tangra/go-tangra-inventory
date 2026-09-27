package releases

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Config tunes the service (config agent_releases).
type Config struct {
	BundleDir    string // releases shipped in the image ("" = none)
	KeepVersions int    // retention (>= 2)
	ChunkBytes   int    // database chunk size
}

// Auditor records release import outcomes (audit.Writer).
type Auditor interface {
	Record(ctx context.Context, e audit.Event) error
}

// Store is the storage the service needs: the release tables and the audit
// table (refusals and imports are recorded).
type Store interface {
	repo.ReleaseStore
	AppendAudit(ctx context.Context, row store.AuditRow) error
}

// Service verifies, stores, retains and serves agent releases.
type Service struct {
	repo  repo.ReleaseStore
	keys  agentrelease.Keyring
	audit Auditor
	log   *slog.Logger
	cfg   Config

	mu      sync.Mutex
	bundled []string // versions seeded from BundleDir by this process

	beforeStore func() // tests: runs between verification and storage
}

// New builds the service; keys is the compiled release keyring.
func New(r repo.ReleaseStore, keys agentrelease.Keyring, a Auditor, log *slog.Logger, cfg Config) *Service {
	if cfg.KeepVersions < 1 {
		cfg.KeepVersions = 5
	}
	return &Service{repo: r, keys: keys, audit: a, log: log, cfg: cfg}
}

// KeyIDs lists the release signing keys the service trusts.
func (s *Service) KeyIDs() []string { return s.keys.KeyIDs() }

// SeedBundles imports every release directory of BundleDir (named after its
// version) and returns how many verified releases it holds; a refused bundle
// is logged, audited and skipped. The newest seeded version becomes the
// platform current agent version.
func (s *Service) SeedBundles(ctx context.Context) int {
	if s.cfg.BundleDir == "" {
		return 0
	}
	entries, err := os.ReadDir(s.cfg.BundleDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			s.log.Warn("agent releases: bundle directory unreadable", "dir", s.cfg.BundleDir, "err", err)
		}
		return 0
	}
	var seeded []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		rel, imported, err := s.ImportDir(ctx, filepath.Join(s.cfg.BundleDir, e.Name()), store.ReleaseBundled)
		if err != nil {
			s.log.Error("agent releases: bundled release refused", "dir", e.Name(), "err", err)
			continue
		}
		seeded = append(seeded, rel.Version)
		s.log.Info("agent releases: bundled release available", "version", rel.Version, "key_id", rel.KeyID, "imported", imported)
	}
	s.mu.Lock()
	s.bundled = seeded
	s.mu.Unlock()
	return len(seeded)
}

// ImportDir verifies a release directory (manifest, signature, artifacts)
// and stores it. Bundles must be named after their version. Symlinks, files
// outside the directory, unknown keys, invalid signatures and size or sha256
// mismatches are refused before anything is written; the outcome is
// audited. imported is false when the same release was already stored.
func (s *Service) ImportDir(ctx context.Context, dir, source string) (store.AgentRelease, bool, error) {
	rel, err := s.verifyDir(dir, source)
	if err == nil {
		if s.beforeStore != nil {
			s.beforeStore()
		}
		var imported bool
		imported, err = s.repo.ImportAgentRelease(ctx, rel, s.opener(dir), s.cfg.ChunkBytes)
		if err == nil {
			if imported {
				s.record(ctx, rel, source, audit.OutcomeOK, "")
			}
			return rel, imported, nil
		}
	}
	s.record(ctx, rel, source, audit.OutcomeRefused, err.Error())
	return store.AgentRelease{}, false, err
}

func (s *Service) record(ctx context.Context, rel store.AgentRelease, source, outcome, reason string) {
	subject := rel.Version
	if subject == "" {
		subject = "unknown"
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	_ = s.audit.Record(ctx, audit.Event{
		TenantID: audit.PlatformTenant, EventType: audit.AgentReleaseImported, ActorKind: audit.ActorSystem, ActorID: "inventorysvc",
		SubjectKind: audit.SubjectRelease, SubjectID: subject, Outcome: outcome, Reason: reason,
		Details: map[string]any{"source": source, "key_id": rel.KeyID, "manifest_sha256": rel.ManifestSHA256},
	})
}

// regularFile returns the path of name inside dir when it is a regular file
// (no symlink) of at most max bytes.
func regularFile(dir, name string, max int64) (string, os.FileInfo, error) {
	p := filepath.Join(dir, name)
	if filepath.Dir(p) != filepath.Clean(dir) {
		return "", nil, fmt.Errorf("releases: %q escapes the release directory", name)
	}
	st, err := os.Lstat(p)
	if err != nil {
		return "", nil, err
	}
	if !st.Mode().IsRegular() {
		return "", nil, fmt.Errorf("releases: %s is not a regular file", name)
	}
	if max > 0 && st.Size() > max {
		return "", nil, fmt.Errorf("releases: %s is larger than %d bytes", name, max)
	}
	return p, st, nil
}

func (s *Service) verifyDir(dir, source string) (store.AgentRelease, error) {
	var rel store.AgentRelease
	st, err := os.Lstat(dir)
	if err != nil {
		return rel, err
	}
	if !st.IsDir() {
		return rel, fmt.Errorf("releases: %s is not a directory", dir)
	}
	mp, _, err := regularFile(dir, agentrelease.ManifestFile, agentrelease.MaxManifestBytes)
	if err != nil {
		return rel, err
	}
	sp, _, err := regularFile(dir, agentrelease.SignatureFile, 1024)
	if err != nil {
		return rel, err
	}
	manifest, err := os.ReadFile(mp) // #nosec G304 -- regular file inside the release directory
	if err != nil {
		return rel, err
	}
	sigRaw, err := os.ReadFile(sp) // #nosec G304 -- regular file inside the release directory
	if err != nil {
		return rel, err
	}
	sig, err := agentrelease.DecodeSignature(sigRaw)
	if err != nil {
		return rel, err
	}
	m, err := agentrelease.ParseManifest(manifest)
	if err != nil {
		return rel, err
	}
	sum := sha256.Sum256(manifest)
	rel = store.AgentRelease{Version: m.Version, Manifest: manifest, Signature: sig, KeyID: m.KeyID,
		ManifestSHA256: hex.EncodeToString(sum[:]), Source: source, ImportedAt: time.Now().UTC()}
	if _, err := s.keys.Verify(manifest, sig, m.KeyID); err != nil {
		return rel, err
	}
	if source == store.ReleaseBundled && filepath.Base(dir) != m.Version {
		return rel, fmt.Errorf("releases: bundle %s holds version %s", filepath.Base(dir), m.Version)
	}
	for _, a := range m.Artifacts {
		p, fi, err := regularFile(dir, a.File, agentrelease.MaxArtifactSize)
		if err != nil {
			return rel, err
		}
		if fi.Size() != a.Size {
			return rel, fmt.Errorf("%s: %w", a.File, agentrelease.ErrSize)
		}
		if err := verifyFile(p, a); err != nil {
			return rel, fmt.Errorf("%s: %w", a.File, err)
		}
		rel.Artifacts = append(rel.Artifacts, store.AgentArtifact{Version: m.Version, OS: a.OS, Arch: a.Arch, InstallType: a.InstallType,
			File: a.File, Size: a.Size, SHA256: a.SHA256})
	}
	return rel, nil
}

func verifyFile(path string, a agentrelease.Artifact) error {
	f, err := os.Open(path) // #nosec G304 -- regular file inside the release directory
	if err != nil {
		return err
	}
	defer f.Close()
	v := agentrelease.NewArtifactVerifier(a)
	if _, err := io.Copy(v, f); err != nil {
		return err
	}
	return v.Finish()
}

// opener re-verifies every artifact while it is stored: a file changed
// after verification fails the import at EOF.
func (s *Service) opener(dir string) repo.ArtifactOpener {
	return func(a store.AgentArtifact) (io.ReadCloser, error) {
		p, _, err := regularFile(dir, a.File, agentrelease.MaxArtifactSize)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(p) // #nosec G304 -- regular file inside the release directory
		if err != nil {
			return nil, err
		}
		return &verifyingReader{f: f, v: agentrelease.NewArtifactVerifier(agentrelease.Artifact{Size: a.Size, SHA256: a.SHA256})}, nil
	}
}

type verifyingReader struct {
	f *os.File
	v *agentrelease.ArtifactVerifier
}

func (r *verifyingReader) Read(p []byte) (int, error) {
	n, err := r.f.Read(p)
	if n > 0 {
		if _, werr := r.v.Write(p[:n]); werr != nil {
			return 0, werr
		}
	}
	if errors.Is(err, io.EOF) {
		if ferr := r.v.Finish(); ferr != nil {
			return n, ferr
		}
	}
	return n, err
}

func (r *verifyingReader) Close() error { return r.f.Close() }

// sortNewest orders releases by semantic version, newest first
// (unparsable versions last).
func sortNewest(rels []store.AgentRelease) {
	sort.SliceStable(rels, func(i, j int) bool {
		c, ok := agentrelease.Compare(rels[i].Version, rels[j].Version)
		if !ok {
			return agentrelease.IsRelease(rels[i].Version)
		}
		return c > 0
	})
}

// Releases lists the stored releases, newest first.
func (s *Service) Releases(ctx context.Context) ([]store.AgentRelease, error) {
	rels, err := s.repo.ListAgentReleases(ctx)
	if err != nil {
		return nil, err
	}
	sortNewest(rels)
	return rels, nil
}

// Release returns one stored release with manifest and signature.
func (s *Service) Release(ctx context.Context, version string) (store.AgentRelease, error) {
	return s.repo.GetAgentRelease(ctx, version)
}

// CurrentVersion is the platform current agent version: the newest release
// bundled in this image, else the newest stored release ("" when none).
func (s *Service) CurrentVersion(ctx context.Context) string {
	s.mu.Lock()
	bundled := append([]string(nil), s.bundled...)
	s.mu.Unlock()
	best := ""
	for _, v := range bundled {
		if c, ok := agentrelease.Compare(v, best); best == "" || (ok && c > 0) {
			best = v
		}
	}
	if best != "" {
		return best
	}
	rels, err := s.Releases(ctx)
	if err != nil || len(rels) == 0 {
		return ""
	}
	return rels[0].Version
}

// HasArtifact reports whether version has a complete artifact for p.
func (s *Service) HasArtifact(ctx context.Context, version string, p agentrelease.Platform) bool {
	_, err := s.OpenArtifact(ctx, version, p)
	return err == nil
}

// Prune deletes releases beyond the newest KeepVersions, never the platform
// current version, a tenant pin or an active upgrade target.
func (s *Service) Prune(ctx context.Context) ([]string, error) {
	protected, err := s.repo.ProtectedAgentVersions(ctx)
	if err != nil {
		return nil, err
	}
	keep := map[string]bool{s.CurrentVersion(ctx): true}
	for _, v := range protected {
		keep[v] = true
	}
	rels, err := s.Releases(ctx)
	if err != nil {
		return nil, err
	}
	var deleted []string
	for i, r := range rels {
		if i < s.cfg.KeepVersions || keep[r.Version] {
			continue
		}
		if err := s.repo.DeleteAgentRelease(ctx, r.Version); err != nil {
			return deleted, err
		}
		deleted = append(deleted, r.Version)
		s.log.Info("agent releases: pruned", "version", r.Version)
	}
	return deleted, nil
}

// Header is what an agent receives before the artifact bytes: the exact
// signed manifest, its signature and the entry for its platform.
type Header struct {
	Manifest  []byte
	Signature []byte
	KeyID     string
	Artifact  store.AgentArtifact
}

// Stream reads one stored artifact chunk by chunk.
type Stream struct {
	svc    *Service
	header Header
	seq    int
	offset int64
}

// OpenArtifact opens the complete artifact of version for platform p
// (repo.ErrNotFound when the release or the platform entry is missing).
func (s *Service) OpenArtifact(ctx context.Context, version string, p agentrelease.Platform) (*Stream, error) {
	rel, err := s.repo.GetAgentRelease(ctx, version)
	if err != nil {
		return nil, err
	}
	for _, a := range rel.Artifacts {
		if a.OS == p.OS && a.Arch == p.Arch && a.InstallType == p.InstallType && a.Complete {
			return &Stream{svc: s, header: Header{Manifest: rel.Manifest, Signature: rel.Signature, KeyID: rel.KeyID, Artifact: a}}, nil
		}
	}
	return nil, fmt.Errorf("releases: %s has no artifact for %s: %w", version, p, repo.ErrNotFound)
}

// Header returns the release header of the stream.
func (st *Stream) Header() Header { return st.header }

// Next returns the next chunk and its offset; io.EOF after the declared
// size. A chunk missing before the declared size is an error.
func (st *Stream) Next(ctx context.Context) (int64, []byte, error) {
	if st.offset >= st.header.Artifact.Size {
		return st.offset, nil, io.EOF
	}
	data, err := st.svc.repo.ReadArtifactChunk(ctx, st.header.Artifact, st.seq)
	if err != nil {
		return st.offset, nil, fmt.Errorf("releases: chunk %d of %s: %w", st.seq, st.header.Artifact.File, err)
	}
	off := st.offset
	st.seq++
	st.offset += int64(len(data))
	return off, data, nil
}
