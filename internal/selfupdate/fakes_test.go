package selfupdate

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
	"io/fs"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

// ---- in-memory file system with fault injection

type memFile struct {
	data      []byte
	mode      fs.FileMode
	mod       time.Time
	ownerRoot bool
}

type memFS struct {
	mu     sync.Mutex
	files  map[string]*memFile
	dirs   map[string]fs.FileMode
	free   uint64
	failOn map[string]error // "op:path" or "op:*"
	now    func() time.Time
	// dirNotRoot marks directories owned by someone else.
	dirNotRoot map[string]bool
}

func newMemFS() *memFS {
	return &memFS{files: map[string]*memFile{}, dirs: map[string]fs.FileMode{}, free: 1 << 40, failOn: map[string]error{},
		now: func() time.Time { return t0 }, dirNotRoot: map[string]bool{}}
}

func (m *memFS) fault(op, path string) error {
	if err, ok := m.failOn[op+":"+path]; ok {
		delete(m.failOn, op+":"+path)
		return err
	}
	if err, ok := m.failOn[op+":*"]; ok {
		delete(m.failOn, op+":*")
		return err
	}
	return nil
}

func (m *memFS) MkdirAll(path string, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fault("mkdir", path); err != nil {
		return err
	}
	for p := filepath.Clean(path); p != "/" && p != "."; p = filepath.Dir(p) {
		if _, ok := m.dirs[p]; !ok {
			m.dirs[p] = perm
		}
	}
	return nil
}

type memWriter struct {
	m        *memFS
	path     string
	buf      bytes.Buffer
	perm     fs.FileMode
	writeErr error
	closeErr error
}

func (w *memWriter) Write(p []byte) (int, error) {
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.buf.Write(p)
}

func (w *memWriter) Close() error {
	if w.closeErr != nil {
		return w.closeErr
	}
	w.m.put(w.path, w.buf.Bytes(), w.perm)
	return nil
}

func (m *memFS) put(path string, data []byte, perm fs.FileMode) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.files[filepath.Clean(path)] = &memFile{data: append([]byte(nil), data...), mode: perm, mod: m.now(), ownerRoot: true}
}

func (m *memFS) Create(path string, perm fs.FileMode) (io.WriteCloser, error) {
	m.mu.Lock()
	err := m.fault("create", path)
	werr := m.fault("write", path)
	cerr := m.fault("close", path)
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return &memWriter{m: m, path: path, perm: perm, writeErr: werr, closeErr: cerr}, nil
}

func (m *memFS) CreateExclusive(path string, data []byte, perm fs.FileMode) error {
	m.mu.Lock()
	if err := m.fault("excl", path); err != nil {
		m.mu.Unlock()
		return err
	}
	_, exists := m.files[filepath.Clean(path)]
	m.mu.Unlock()
	if exists {
		return fs.ErrExist
	}
	m.put(path, data, perm)
	return nil
}

func (m *memFS) Open(path string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fault("open", path); err != nil {
		return nil, err
	}
	f, ok := m.files[filepath.Clean(path)]
	if !ok {
		return nil, fs.ErrNotExist
	}
	if err := m.fault("readerr", path); err != nil {
		return io.NopCloser(io.MultiReader(bytes.NewReader(f.data[:len(f.data)/2]), errReader{err})), nil
	}
	return io.NopCloser(bytes.NewReader(append([]byte(nil), f.data...))), nil
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

func (m *memFS) ReadFile(path string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fault("read", path); err != nil {
		return nil, err
	}
	f, ok := m.files[filepath.Clean(path)]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return append([]byte(nil), f.data...), nil
}

func (m *memFS) WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	m.mu.Lock()
	err := m.fault("writeatomic", path)
	m.mu.Unlock()
	if err != nil {
		return err
	}
	m.put(path, data, perm)
	return nil
}

func (m *memFS) Remove(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fault("remove", path); err != nil {
		return err
	}
	delete(m.files, filepath.Clean(path))
	return nil
}

func (m *memFS) RemoveAll(path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := filepath.Clean(path)
	for k := range m.files {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(m.files, k)
		}
	}
	for k := range m.dirs {
		if k == p || strings.HasPrefix(k, p+"/") {
			delete(m.dirs, k)
		}
	}
	return nil
}

func (m *memFS) Stat(path string) (FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fault("stat", path); err != nil {
		return FileInfo{}, err
	}
	f, ok := m.files[filepath.Clean(path)]
	if !ok {
		return FileInfo{}, fs.ErrNotExist
	}
	return FileInfo{Mode: f.mode, Size: int64(len(f.data)), ModTime: f.mod, OwnerRoot: f.ownerRoot, Private: f.mode.Perm()&0o077 == 0}, nil
}

func (m *memFS) StatDir(path string) (FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fault("statdir", path); err != nil {
		return FileInfo{}, err
	}
	perm, ok := m.dirs[filepath.Clean(path)]
	if !ok {
		return FileInfo{}, fs.ErrNotExist
	}
	return FileInfo{Mode: perm | fs.ModeDir, ModTime: m.now(), OwnerRoot: !m.dirNotRoot[filepath.Clean(path)], Private: perm&0o077 == 0}, nil
}

func (m *memFS) ReadDir(path string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fault("readdir", path); err != nil {
		return nil, err
	}
	p := filepath.Clean(path)
	seen := map[string]bool{}
	for k := range m.dirs {
		if filepath.Dir(k) == p {
			seen[filepath.Base(k)] = true
		}
	}
	for k := range m.files {
		if rest, ok := strings.CutPrefix(k, p+"/"); ok {
			seen[strings.SplitN(rest, "/", 2)[0]] = true
		}
	}
	var out []string
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out, nil
}

func (m *memFS) CopyFile(src, dst string, perm fs.FileMode) error {
	m.mu.Lock()
	err := m.fault("copy", dst)
	f, ok := m.files[filepath.Clean(src)]
	m.mu.Unlock()
	if err != nil {
		return err
	}
	if !ok {
		return fs.ErrNotExist
	}
	m.put(dst, f.data, perm)
	return nil
}

func (m *memFS) Free(path string) (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.fault("free", path); err != nil {
		return 0, err
	}
	return m.free, nil
}

func (m *memFS) exists(path string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.files[filepath.Clean(path)]
	return ok
}

func (m *memFS) mode(path string) fs.FileMode {
	m.mu.Lock()
	defer m.mu.Unlock()
	if f, ok := m.files[filepath.Clean(path)]; ok {
		return f.mode
	}
	return m.dirs[filepath.Clean(path)]
}

// ---- releases signed with an ephemeral key, served by a fake client

type release struct {
	manifest  []byte
	signature []byte
	keyID     string
	data      map[agentrelease.Platform][]byte
	entries   map[agentrelease.Platform]agentrelease.Artifact
}

var allPlatforms = []agentrelease.Platform{
	{OS: "linux", Arch: "amd64", InstallType: "deb"}, {OS: "linux", Arch: "amd64", InstallType: "rpm"},
	{OS: "linux", Arch: "amd64", InstallType: "binary"}, {OS: "windows", Arch: "amd64", InstallType: "binary"},
}

func makeRelease(t testing.TB, priv ed25519.PrivateKey, keyID, version string, mutate func(*agentrelease.Manifest)) release {
	t.Helper()
	r := release{keyID: keyID, data: map[agentrelease.Platform][]byte{}, entries: map[agentrelease.Platform]agentrelease.Artifact{}}
	m := agentrelease.Manifest{Schema: 1, Version: version, CreatedAt: t0, KeyID: keyID}
	for i, p := range allPlatforms {
		data := bytes.Repeat([]byte(fmt.Sprintf("%s-%s-%d|", version, p.InstallType, i)), 300)
		sum := sha256.Sum256(data)
		a := agentrelease.Artifact{OS: p.OS, Arch: p.Arch, InstallType: p.InstallType, File: fmt.Sprintf("agent-%s-%d.%s", version, i, p.InstallType),
			Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}
		m.Artifacts = append(m.Artifacts, a)
		r.data[p] = data
	}
	if mutate != nil {
		mutate(&m)
	}
	for _, a := range m.Artifacts {
		r.entries[a.Platform()] = a
	}
	r.manifest = m.Encode()
	r.signature = ed25519.Sign(priv, r.manifest)
	return r
}

type fakeDownload struct {
	h      Header
	chunks [][]byte
	offs   []int64
	i      int
	err    error // returned by Next after the chunks
}

func (d *fakeDownload) Header() Header { return d.h }
func (d *fakeDownload) Next() (int64, []byte, error) {
	if d.i < len(d.chunks) {
		c, o := d.chunks[d.i], d.offs[d.i]
		d.i++
		return o, c, nil
	}
	if d.err != nil {
		return 0, nil, d.err
	}
	return 0, nil, io.EOF
}
func (d *fakeDownload) Close() error { return nil }

type fakeClient struct {
	mu       sync.Mutex
	releases map[string]release
	platform agentrelease.Platform
	// tamper hooks
	mutateHeader func(*Header)
	mutateData   func([]byte) []byte
	mutateOffs   func([]int64)
	nextErr      error
	downloadErr  map[string]error // by version
	reports      []Report
	reportErr    error
	check        CheckResult
	checkErr     error
	checks       []bool
}

func (c *fakeClient) Check(_ context.Context, current string, p agentrelease.Platform, apply bool) (CheckResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.checks = append(c.checks, apply)
	return c.check, c.checkErr
}

func (c *fakeClient) Download(_ context.Context, requestID, version string) (Download, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.downloadErr[version]; err != nil {
		return nil, err
	}
	r, ok := c.releases[version]
	if !ok {
		return nil, errors.New("not found")
	}
	entry := r.entries[c.platform]
	h := Header{Manifest: r.manifest, Signature: r.signature, KeyID: r.keyID, File: entry.File, Size: entry.Size, SHA256: entry.SHA256}
	data := append([]byte(nil), r.data[c.platform]...)
	if c.mutateHeader != nil {
		c.mutateHeader(&h)
	}
	if c.mutateData != nil {
		data = c.mutateData(data)
	}
	d := &fakeDownload{h: h, err: c.nextErr}
	for off := 0; off < len(data); off += 1000 {
		end := min(off+1000, len(data))
		d.chunks = append(d.chunks, data[off:end])
		d.offs = append(d.offs, int64(off))
	}
	if c.mutateOffs != nil {
		c.mutateOffs(d.offs)
	}
	return d, nil
}

func (c *fakeClient) Report(_ context.Context, r Report) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reports = append(c.reports, r)
	return c.reportErr
}

func (c *fakeClient) states() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, r := range c.reports {
		s := r.State
		if r.Reason != "" {
			s += ":" + r.Reason
		}
		out = append(out, s)
	}
	return out
}

// ---- fake installer and clock

type fakeInstaller struct {
	mu          sync.Mutex
	fs          *memFS
	calls       []string
	supported   error
	startErr    error
	installErr  map[string]error // by artifact path
	swapErr     error
	restoreErr  error
	restartErr  error
	onInstall   func() // e.g. the new agent confirms
	onRestart   func()
	startedWith []string
}

func (f *fakeInstaller) record(s string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, s)
}

func (f *fakeInstaller) Supported(installType string) error { return f.supported }

func (f *fakeInstaller) StartHelper(_ context.Context, helper, stateFile, requestID string) error {
	f.record("start " + requestID)
	f.startedWith = []string{helper, stateFile}
	return f.startErr
}

func (f *fakeInstaller) InstallPackage(_ context.Context, installType, artifact string, downgrade bool) error {
	f.record(fmt.Sprintf("install %s %s downgrade=%v", installType, filepath.Base(artifact), downgrade))
	if err := f.installErr[artifact]; err != nil {
		return err
	}
	if f.onInstall != nil {
		f.onInstall()
	}
	return nil
}

func (f *fakeInstaller) SwapBinary(_ context.Context, newBinary, target string) (string, error) {
	f.record("swap " + filepath.Base(newBinary))
	if f.swapErr != nil {
		return "", f.swapErr
	}
	if f.onInstall != nil {
		f.onInstall()
	}
	return target + ".prev", nil
}

func (f *fakeInstaller) RestoreBinary(_ context.Context, previous, target string) error {
	f.record("restore " + filepath.Base(previous))
	return f.restoreErr
}

func (f *fakeInstaller) RestartService(context.Context) error {
	f.record("restart")
	if f.onRestart != nil {
		f.onRestart()
	}
	return f.restartErr
}

func (f *fakeInstaller) log() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return strings.Join(f.calls, "; ")
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	onSleep func()
	err     error
}

func (c *fakeClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	c.mu.Lock()
	c.now = c.now.Add(d)
	hook, err := c.onSleep, c.err
	c.mu.Unlock()
	if hook != nil {
		hook()
	}
	return err
}

// ---- fixture

var t0 = time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC)

const (
	staging = "/var/lib/inventory-agent/upgrade"
	exe     = "/usr/bin/inventory-agent"
)

type fixture struct {
	fs     *memFS
	client *fakeClient
	inst   *fakeInstaller
	clock  *fakeClock
	priv   ed25519.PrivateKey
	keys   agentrelease.Keyring
	u      *Updater
}

func newFixture(t *testing.T, version string, p agentrelease.Platform) *fixture {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{fs: newMemFS(), priv: priv, keys: agentrelease.Keyring{"test-key": pub}, clock: &fakeClock{now: t0}}
	f.fs.now = f.clock.Now
	f.fs.put(exe, []byte("running agent "+version), 0o755)
	f.client = &fakeClient{releases: map[string]release{}, platform: p, downloadErr: map[string]error{}}
	for _, v := range []string{"4.4.0", "4.5.0", "4.3.0", "4.6.0"} {
		f.client.releases[v] = makeRelease(t, priv, "test-key", v, nil)
	}
	f.inst = &fakeInstaller{fs: f.fs, installErr: map[string]error{}}
	f.u = f.updater(version, p)
	return f
}

func (f *fixture) updater(version string, p agentrelease.Platform) *Updater {
	return New(Config{Version: version, Platform: p, Keys: f.keys, StagingDir: staging, Executable: exe, ConfirmTimeout: 5 * time.Minute},
		f.client, f.fs, f.inst, f.clock, slog.New(slog.DiscardHandler))
}

func (f *fixture) state(t *testing.T) State {
	t.Helper()
	st, err := f.u.loadState()
	if err != nil {
		t.Fatalf("state: %v", err)
	}
	return st
}

var (
	debP = agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}
	rpmP = agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "rpm"}
	binP = agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "binary"}
	winP = agentrelease.Platform{OS: "windows", Arch: "amd64", InstallType: "binary"}
)
