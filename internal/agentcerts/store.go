package agentcerts

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Config is the local store configuration (contracts/agent-config.md §1).
// Nothing in it is ever taken from the server.
type Config struct {
	Dir          string // absolute directory of the store
	Owner, Group string // file owner and group (names or numeric ids)
	DirMode      fs.FileMode
	CertMode     fs.FileMode
	KeyMode      fs.FileMode
	KeepPrevious int           // previous generations kept per name (0-5)
	Hook         string        // absolute deploy hook path; "" = none
	HookTimeout  time.Duration // bound of one hook run
}

// ConfigFrom maps the validated agent configuration.
func ConfigFrom(c config.AgentCertificates) Config {
	dir, cert, key := c.Modes()
	return Config{Dir: c.Directory, Owner: c.Owner, Group: c.Group, DirMode: dir, CertMode: cert, KeyMode: key,
		KeepPrevious: c.KeepPrevious, Hook: c.DeployHook, HookTimeout: c.HookTimeout()}
}

// Deps are the store's collaborators. RootUID is the owner every store
// directory and the hook must have (0; tests use their own uid).
type Deps struct {
	FS       FS
	Accounts Accounts
	Hooks    HookRunner
	RootUID  int
	Now      func() time.Time
	Logf     func(format string, args ...any)
}

// Request is one delivered bundle (CertificateBundle of FetchCertificate).
// Only Name selects anything on disk; it is validated again here.
type Request struct {
	ItemID        string
	Name          string
	CertificateID string
	CertPEM       []byte
	ChainPEM      []byte
	KeyPEM        []byte
	HasKey        bool // key_policy require: a key must be present
	RerunHook     bool // re-armed after hook_failed: run the hook even when unchanged
	IsRenewal     bool // server's view; the local metadata wins when present
}

// Result is what the agent reports (ReportCertificateRequest). HookExitCode
// is -1 when no hook ran, 256 on timeout. Detail never holds hook output.
type Result struct {
	State        string
	Reason       string
	Serial       string
	Fingerprint  string
	HookExitCode int
	Detail       string
}

// Store installs delivered certificates under the configured directory.
// Installs are serialised (one at a time per agent, including the hook).
type Store struct {
	cfg   Config
	fs    FS
	acc   Accounts
	hooks HookRunner
	root  int
	now   func() time.Time
	logf  func(format string, args ...any)
	mu    sync.Mutex
}

// New builds a store over the given dependencies.
func New(cfg Config, d Deps) *Store {
	s := &Store{cfg: cfg, fs: d.FS, acc: d.Accounts, hooks: d.Hooks, root: d.RootUID, now: d.Now, logf: d.Logf}
	if s.now == nil {
		s.now = time.Now
	}
	if s.logf == nil {
		s.logf = log.Printf
	}
	return s
}

// failure is a reported install failure: a closed reason and a short,
// material-free detail.
type failure struct {
	reason string
	detail string
}

// Detail values.
const (
	detailUnsafeDir = "unsafe_directory"
	detailDiskFull  = "insufficient free space"
)

func fail(reason, detail string) *failure { return &failure{reason: reason, detail: detail} }

// ioFailure maps a filesystem error: no space → disk_full, anything else →
// write_failed with the step that failed.
func ioFailure(step string, err error) *failure {
	if errors.Is(err, syscall.ENOSPC) {
		return fail(store.ReasonDiskFull, step)
	}
	if errors.Is(err, errUnsafe) {
		return fail(store.ReasonWriteFailed, detailUnsafeDir)
	}
	return fail(store.ReasonWriteFailed, step)
}

// Install validates and installs one bundle and runs the local hook
// (contracts/agent-config.md §2 install algorithm).
func (s *Store) Install(ctx context.Context, req Request) Result {
	s.mu.Lock()
	defer s.mu.Unlock()
	res, f := s.install(ctx, req)
	if f != nil {
		res.State, res.Reason, res.Detail = store.DeliveryFailed, f.reason, f.detail
		s.logf("certs: item %s name %q failed: %s (%s)", req.ItemID, req.Name, f.reason, f.detail)
		return res
	}
	s.logf("certs: item %s name %s %s serial %s fingerprint %s (hook exit %d)", req.ItemID, req.Name, res.State, res.Serial,
		res.Fingerprint, res.HookExitCode)
	return res
}

func (s *Store) install(ctx context.Context, req Request) (Result, *failure) {
	res := Result{HookExitCode: -1}
	if !certmaterial.ValidName(req.Name) {
		return res, fail(store.ReasonInvalidName, "")
	}
	b, err := certmaterial.ParseBundle(req.CertPEM, req.ChainPEM, req.KeyPEM, certmaterial.Options{RequireKey: req.HasKey, Now: s.now})
	if err != nil {
		return res, fail(certmaterial.Reason(err), "")
	}
	res.Serial, res.Fingerprint = b.Serial, b.Fingerprint
	uid, gid, f := s.owner()
	if f != nil {
		return res, f
	}
	if err := s.ensureLayout(req.Name, gid); err != nil {
		return res, ioFailure("prepare directories", err)
	}
	cur, err := s.current(req.Name)
	if err != nil {
		return res, ioFailure("read current generation", err)
	}
	same, err := s.unchanged(req.Name, cur, b, uid, gid)
	if err != nil {
		return res, ioFailure("check installed files", err)
	}
	if same {
		res.State = store.DeliveryUnchanged
		if !req.RerunHook || s.cfg.Hook == "" {
			return res, nil
		}
		return s.finish(ctx, req, b, *cur.meta, cur.meta.PreviousSerial != "", uid, gid, res), nil
	}
	key, hasKey := b.KeyPEM, b.HasKey
	if !hasKey && cur.gen != "" {
		k, ok, err := s.currentKey(req.Name, cur.gen)
		if err != nil {
			return res, ioFailure("read current key", err)
		}
		if ok {
			if !keyMatches(b, k) {
				return res, fail(store.ReasonKeyMismatch, "installed key does not match the certificate")
			}
			key, hasKey = k, true
		}
	}
	if free, err := s.fs.Free(); err == nil && free < 4*uint64(len(b.CertPEM)+len(b.ChainPEM)+len(b.FullChainPEM)+len(key)) {
		return res, fail(store.ReasonDiskFull, detailDiskFull)
	}
	gen, err := s.stage(req.Name, b, key, uid, gid)
	if err != nil {
		return res, ioFailure("write generation", err)
	}
	if err := s.swap(req.Name, gen); err != nil {
		_ = s.fs.RemoveAll(genPath(req.Name, gen))
		return res, ioFailure("switch live link", err)
	}
	renewal := req.IsRenewal
	if cur.meta != nil {
		renewal = cur.meta.Fingerprint != b.Fingerprint
	}
	meta := s.newMeta(req, b, cur.meta, gen, hasKey, renewal)
	if err := s.writeMeta(meta, uid, gid); err != nil {
		s.logf("certs: name %s: metadata not written: %v", req.Name, err)
	}
	s.prune(req.Name, gen)
	res.State = store.DeliveryInstalled
	return s.finish(ctx, req, b, meta, renewal, uid, gid, res), nil
}

// finish runs the configured hook after an installation (or a re-run) and
// records its result in the metadata.
func (s *Store) finish(ctx context.Context, req Request, b certmaterial.Bundle, meta Metadata, renewal bool, uid, gid int, res Result) Result {
	if s.cfg.Hook == "" {
		return res
	}
	h := s.runHook(ctx, req, b, meta.HasKey, renewal)
	now := s.now().UTC()
	code := h.code
	meta.LastHookExecution, meta.HookExitCode = &now, &code
	if err := s.writeMeta(meta, uid, gid); err != nil {
		s.logf("certs: name %s: metadata not written: %v", req.Name, err)
	}
	res.State, res.HookExitCode = store.DeliveryInstalled, h.code
	if h.reason != "" {
		res.State, res.Reason, res.Detail = store.DeliveryHookFailed, h.reason, h.detail
	}
	return res
}

// owner resolves the configured owner and group.
func (s *Store) owner() (uid, gid int, f *failure) {
	uid, err := resolveID(s.cfg.Owner, s.acc.LookupUser)
	if err != nil {
		return 0, 0, fail(store.ReasonOwnerUnknown, "owner")
	}
	gid, err = resolveID(s.cfg.Group, s.acc.LookupGroup)
	if err != nil {
		return 0, 0, fail(store.ReasonOwnerUnknown, "group")
	}
	return uid, gid, nil
}

// resolveID accepts a numeric id or looks the name up.
func resolveID(v string, lookup func(string) (int, error)) (int, error) {
	if n, err := strconv.Atoi(v); err == nil && n >= 0 {
		return n, nil
	}
	id, err := lookup(v)
	if err != nil {
		return 0, fmt.Errorf("agentcerts: unknown account %q: %w", v, err)
	}
	return id, nil
}

// keyMatches reports whether key is an unencrypted private key of the
// bundle's leaf (validity is not re-checked).
func keyMatches(b certmaterial.Bundle, key []byte) bool {
	_, err := certmaterial.ParseBundle(b.CertPEM, nil, key, certmaterial.Options{RequireKey: true,
		Now: func() time.Time { return b.NotBefore }})
	return err == nil
}
