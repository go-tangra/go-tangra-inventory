//go:build linux

package agentmodules

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
)

// zent is one archive member of a test bundle.
type zent struct {
	name string
	body string
	mode fs.FileMode // 0: 0644 file (or dir for names ending in /)
	raw  func(h *zip.FileHeader)
}

// zipOf builds a bundle.
func zipOf(t testing.TB, entries ...zent) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		h := &zip.FileHeader{Name: e.name, Method: zip.Deflate}
		switch {
		case e.mode != 0:
			h.SetMode(e.mode)
		case strings.HasSuffix(e.name, "/"):
			h.SetMode(fs.ModeDir | 0o755)
		default:
			h.SetMode(0o644)
		}
		if e.raw != nil {
			e.raw(h)
		}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// smsGW is a bundle shaped like the sms-gw module of the catalogue.
func smsGW(t testing.TB) []byte {
	return zipOf(t,
		zent{name: "sms-gw/"},
		zent{name: "sms-gw/compose.yaml", body: "services: {}\n"},
		zent{name: "sms-gw/config/"},
		zent{name: "sms-gw/config/app.yaml", body: "listen: :8080\n"},
		zent{name: "sms-gw/.env", body: "JOIN_TOKEN=secret-token\nDB_PASSWORD=pw\n"},
		zent{name: "sms-gw/private/db/password", body: "pw"},
		zent{name: "sms-gw/bin/start.sh", body: "#!/bin/sh\n", mode: 0o755},
	)
}

func request(module string, data []byte) Request {
	sum := sha256.Sum256(data)
	return Request{ItemID: "item-1", Module: module, Version: "1.2.0", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Data: data}
}

// faultFS injects errors into the real os.Root filesystem.
type faultFS struct {
	agentcerts.FS
	mu   sync.Mutex
	errs map[string]error // op or op:rel-prefix -> error
	free *uint64
}

func (f *faultFS) err(op, rel string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, e := range f.errs {
		o, p, _ := strings.Cut(k, ":")
		if o == op && strings.HasPrefix(rel, p) {
			return e
		}
	}
	return nil
}

func (f *faultFS) Lstat(rel string) (agentcerts.FileInfo, error) {
	if err := f.err("Lstat", rel); err != nil {
		return agentcerts.FileInfo{}, err
	}
	return f.FS.Lstat(rel)
}

func (f *faultFS) Mkdir(rel string, perm fs.FileMode) error {
	if err := f.err("Mkdir", rel); err != nil {
		return err
	}
	return f.FS.Mkdir(rel, perm)
}

func (f *faultFS) WriteFile(rel string, data []byte, perm fs.FileMode, uid, gid int) error {
	if err := f.err("WriteFile", rel); err != nil {
		return err
	}
	return f.FS.WriteFile(rel, data, perm, uid, gid)
}

func (f *faultFS) Chmod(rel string, perm fs.FileMode) error {
	if err := f.err("Chmod", rel); err != nil {
		return err
	}
	return f.FS.Chmod(rel, perm)
}

func (f *faultFS) Lchown(rel string, uid, gid int) error {
	if err := f.err("Lchown", rel); err != nil {
		return err
	}
	return f.FS.Lchown(rel, uid, gid)
}

func (f *faultFS) Rename(oldRel, newRel string) error {
	if err := f.err("Rename", oldRel); err != nil {
		return err
	}
	return f.FS.Rename(oldRel, newRel)
}

func (f *faultFS) RemoveAll(rel string) error {
	if err := f.err("RemoveAll", rel); err != nil {
		return err
	}
	return f.FS.RemoveAll(rel)
}

func (f *faultFS) ReadDir(rel string) ([]string, error) {
	if err := f.err("ReadDir", rel); err != nil {
		return nil, err
	}
	return f.FS.ReadDir(rel)
}

func (f *faultFS) SyncDir(rel string) error {
	if err := f.err("SyncDir", rel); err != nil {
		return err
	}
	return f.FS.SyncDir(rel)
}

func (f *faultFS) Free() (uint64, error) {
	if f.free != nil {
		return *f.free, nil
	}
	return f.FS.Free()
}

// fakeAccounts resolves "app" only.
type fakeAccounts struct{}

func (fakeAccounts) LookupUser(n string) (int, error) {
	if n == "app" {
		return os.Getuid(), nil
	}
	return 0, errors.New("unknown user")
}

func (fakeAccounts) LookupGroup(n string) (int, error) {
	if n == "app" {
		return os.Getgid(), nil
	}
	return 0, errors.New("unknown group")
}

// fakeHooks reports a safe root-owned hook (unless missing) and records runs.
type fakeHooks struct {
	mu      sync.Mutex
	missing bool
	out     agentcerts.HookOutcome
	runs    []agentcerts.HookSpec
}

func (h *fakeHooks) Lstat(string) (agentcerts.FileInfo, error) {
	if h.missing {
		return agentcerts.FileInfo{}, fs.ErrNotExist
	}
	return agentcerts.FileInfo{Mode: 0o755, UID: os.Getuid()}, nil
}

func (h *fakeHooks) Stat(string) (agentcerts.FileInfo, error) {
	return agentcerts.FileInfo{Mode: fs.ModeDir | 0o755, UID: os.Getuid()}, nil
}

func (h *fakeHooks) Run(_ context.Context, spec agentcerts.HookSpec) agentcerts.HookOutcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs = append(h.runs, spec)
	return h.out
}

type harness struct {
	dir   string
	fs    *faultFS
	hooks *fakeHooks
	w     *Writer
	log   *strings.Builder
}

func newHarness(t *testing.T, hook string) *harness {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "modules")
	real, err := agentcerts.OpenFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	h := &harness{dir: dir, fs: &faultFS{FS: real, errs: map[string]error{}}, hooks: &fakeHooks{}, log: &strings.Builder{}}
	var mu sync.Mutex
	cfg := Config{Dir: dir, Owner: strconv.Itoa(os.Getuid()), Group: "app", Hook: hook, HookTimeout: time.Minute}
	h.w = New(cfg, Deps{FS: h.fs, Accounts: fakeAccounts{}, Hooks: h.hooks, RootUID: os.Getuid(),
		Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			h.log.WriteString(strings.TrimSpace(fmt.Sprintf(format, args...)) + "\n")
		}})
	return h
}

// entries lists the modules directory.
func (h *harness) entries(t *testing.T) []string {
	t.Helper()
	des, err := os.ReadDir(h.dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, d := range des {
		out = append(out, d.Name())
	}
	return out
}
