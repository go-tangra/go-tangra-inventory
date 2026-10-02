package agentcerts

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"net"
	"path"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// --- in-memory filesystem with fault injection ---

type memNode struct {
	mode     fs.FileMode
	uid, gid int
	data     []byte
	target   string
}

// memFS is a fake FS. Entries are created as uid/gid of the fake process
// (root by default). fail injects errors by "Op path" (or "Op *"); writes
// counts mutating calls.
type memFS struct {
	mu       sync.Mutex
	nodes    map[string]*memNode
	uid, gid int
	fail     map[string]error
	writes   int
	free     uint64
	freeErr  error
	chowns   []string
}

func newMemFS() *memFS {
	return &memFS{nodes: map[string]*memNode{".": {mode: fs.ModeDir | 0o755}}, fail: map[string]error{}, free: 1 << 40}
}

func pathErr(op, rel string, err error) error { return &fs.PathError{Op: op, Path: rel, Err: err} }

func (m *memFS) check(op, rel string) error {
	if err, ok := m.fail[op+" "+rel]; ok {
		return err
	}
	if err, ok := m.fail[op+" *"]; ok {
		return err
	}
	return nil
}

func (m *memFS) parentDir(rel string) error {
	p, ok := m.nodes[path.Dir(rel)]
	if !ok || !p.mode.IsDir() {
		return syscall.ENOENT
	}
	return nil
}

func (m *memFS) Lstat(rel string) (FileInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("Lstat", rel); err != nil {
		return FileInfo{}, err
	}
	n, ok := m.nodes[rel]
	if !ok {
		return FileInfo{}, pathErr("lstat", rel, fs.ErrNotExist)
	}
	return FileInfo{Mode: n.mode, UID: n.uid, GID: n.gid, Size: int64(len(n.data))}, nil
}

func (m *memFS) create(op, rel string, n *memNode) error {
	if err := m.check(op, rel); err != nil {
		return err
	}
	if _, ok := m.nodes[rel]; ok {
		return pathErr(op, rel, fs.ErrExist)
	}
	if err := m.parentDir(rel); err != nil {
		return pathErr(op, rel, err)
	}
	m.nodes[rel] = n
	m.writes++
	return nil
}

func (m *memFS) Mkdir(rel string, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.create("Mkdir", rel, &memNode{mode: fs.ModeDir | perm, uid: m.uid, gid: m.gid})
}

func (m *memFS) WriteFile(rel string, data []byte, perm fs.FileMode, uid, gid int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.create("WriteFile", rel, &memNode{mode: perm, uid: uid, gid: gid, data: append([]byte(nil), data...)})
}

func (m *memFS) ReadFile(rel string, max int64) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("ReadFile", rel); err != nil {
		return nil, err
	}
	n, ok := m.nodes[rel]
	switch {
	case !ok:
		return nil, pathErr("open", rel, fs.ErrNotExist)
	case !n.mode.IsRegular():
		return nil, pathErr("open", rel, syscall.ELOOP)
	case int64(len(n.data)) > max:
		return nil, errors.New("too large")
	}
	return append([]byte(nil), n.data...), nil
}

func (m *memFS) Chmod(rel string, perm fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("Chmod", rel); err != nil {
		return err
	}
	n, ok := m.nodes[rel]
	if !ok || n.mode&fs.ModeSymlink != 0 {
		return pathErr("chmod", rel, fs.ErrNotExist)
	}
	n.mode = n.mode.Type() | perm
	m.writes++
	return nil
}

func (m *memFS) Lchown(rel string, uid, gid int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("Lchown", rel); err != nil {
		return err
	}
	n, ok := m.nodes[rel]
	if !ok {
		return pathErr("lchown", rel, fs.ErrNotExist)
	}
	n.uid, n.gid = uid, gid
	m.writes++
	m.chowns = append(m.chowns, fmt.Sprintf("%s %d:%d", rel, uid, gid))
	return nil
}

func (m *memFS) Symlink(target, rel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.create("Symlink", rel, &memNode{mode: fs.ModeSymlink | 0o777, uid: m.uid, gid: m.gid, target: target})
}

func (m *memFS) Readlink(rel string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("Readlink", rel); err != nil {
		return "", err
	}
	n, ok := m.nodes[rel]
	if !ok || n.mode&fs.ModeSymlink == 0 {
		return "", pathErr("readlink", rel, syscall.EINVAL)
	}
	return n.target, nil
}

func (m *memFS) Rename(oldRel, newRel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("Rename", newRel); err != nil {
		return err
	}
	n, ok := m.nodes[oldRel]
	if !ok {
		return pathErr("rename", oldRel, fs.ErrNotExist)
	}
	if dst, ok := m.nodes[newRel]; ok && dst.mode.IsDir() {
		return pathErr("rename", newRel, syscall.EISDIR)
	}
	delete(m.nodes, oldRel)
	m.nodes[newRel] = n
	m.writes++
	return nil
}

func (m *memFS) RemoveAll(rel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("RemoveAll", rel); err != nil {
		return err
	}
	for k := range m.nodes {
		if k == rel || strings.HasPrefix(k, rel+"/") {
			delete(m.nodes, k)
			m.writes++
		}
	}
	return nil
}

func (m *memFS) ReadDir(rel string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.check("ReadDir", rel); err != nil {
		return nil, err
	}
	if n, ok := m.nodes[rel]; !ok || !n.mode.IsDir() {
		return nil, pathErr("readdir", rel, fs.ErrNotExist)
	}
	var out []string
	for k := range m.nodes {
		if k != "." && path.Dir(k) == rel {
			out = append(out, path.Base(k))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (m *memFS) SyncDir(rel string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.check("SyncDir", rel)
}

func (m *memFS) Free() (uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.free, m.freeErr
}

// node returns an entry (nil when absent).
func (m *memFS) node(rel string) *memNode {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nodes[rel]
}

// liveGen follows live/<name> the way a reader would.
func (m *memFS) liveGen(t *testing.T, name string) string {
	t.Helper()
	n := m.node(livePath(name))
	if n == nil || n.mode&fs.ModeSymlink == 0 {
		t.Fatalf("live/%s is not a symlink", name)
	}
	gen, ok := parseTarget(name, n.target)
	if !ok {
		t.Fatalf("live/%s -> %s", name, n.target)
	}
	return gen
}

// liveFile reads a file of the live generation of name.
func (m *memFS) liveFile(t *testing.T, name, file string) []byte {
	t.Helper()
	n := m.node(genPath(name, m.liveGen(t, name)) + "/" + file)
	if n == nil {
		return nil
	}
	return n.data
}

func (m *memFS) setFail(key string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail[key] = err
}

func (m *memFS) clearFail() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.fail = map[string]error{}
}

func (m *memFS) resetWrites() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writes = 0
}

func (m *memFS) writeCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.writes
}

// --- accounts, clock, logger ---

type fakeAccounts struct {
	users, groups map[string]int
}

func (a fakeAccounts) LookupUser(n string) (int, error) {
	if id, ok := a.users[n]; ok {
		return id, nil
	}
	return 0, errors.New("unknown user")
}

func (a fakeAccounts) LookupGroup(n string) (int, error) {
	if id, ok := a.groups[n]; ok {
		return id, nil
	}
	return 0, errors.New("unknown group")
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type logBuf struct {
	mu    sync.Mutex
	lines []string
}

func (l *logBuf) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logBuf) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

// --- fake hook runner ---

type fakeHooks struct {
	mu       sync.Mutex
	file     FileInfo
	fileErr  error
	dir      FileInfo
	dirErr   error
	dirs     map[string]FileInfo // per-path override of dir
	outcome  HookOutcome
	runs     []HookSpec
	lstatArg string
	statArgs []string
}

func newFakeHooks() *fakeHooks {
	return &fakeHooks{file: FileInfo{Mode: 0o755}, dir: FileInfo{Mode: fs.ModeDir | 0o755}}
}

func (h *fakeHooks) Lstat(p string) (FileInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lstatArg = p
	return h.file, h.fileErr
}

func (h *fakeHooks) Stat(p string) (FileInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.statArgs = append(h.statArgs, p)
	if fi, ok := h.dirs[p]; ok {
		return fi, nil
	}
	return h.dir, h.dirErr
}

func (h *fakeHooks) Run(_ context.Context, spec HookSpec) HookOutcome {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.runs = append(h.runs, spec)
	return h.outcome
}

func (h *fakeHooks) runCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runs)
}

// --- certificates ---

// validFrom/validTo bound every test leaf; testNow is inside.
var (
	validFrom = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	validTo   = time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC)
	testNow   = time.Date(2026, 6, 1, 8, 0, 0, 0, time.UTC)
)

type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pem  []byte
}

func newKey(t testing.TB) (*ecdsa.PrivateKey, []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return k, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func newCA(t testing.TB) testCA {
	t.Helper()
	k, _ := newKey(t)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Test Issuing CA"},
		NotBefore: validFrom.AddDate(-1, 0, 0), NotAfter: validTo.AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	c, _ := x509.ParseCertificate(der)
	return testCA{cert: c, key: k, pem: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}

// leaf issues a leaf for key with the given serial.
func (ca testCA) leaf(t testing.TB, key *ecdsa.PrivateKey, serial int64, cn string) []byte {
	t.Helper()
	tpl := &x509.Certificate{SerialNumber: big.NewInt(serial), Subject: pkix.Name{CommonName: cn},
		NotBefore: validFrom, NotAfter: validTo, DNSNames: []string{cn, "example.com"},
		IPAddresses: []net.IP{net.ParseIP("192.0.2.10")}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.cert, &key.PublicKey, ca.key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// bundle is one deliverable certificate with its key.
type bundle struct {
	cert, key []byte
	k         *ecdsa.PrivateKey
}

func (ca testCA) bundle(t testing.TB, serial int64) bundle {
	t.Helper()
	k, kp := newKey(t)
	return bundle{cert: ca.leaf(t, k, serial, "www.example.com"), key: kp, k: k}
}

// reissue is another certificate for the same key.
func (ca testCA) reissue(t testing.TB, b bundle, serial int64) bundle {
	t.Helper()
	return bundle{cert: ca.leaf(t, b.k, serial, "www.example.com"), key: b.key, k: b.k}
}

func (b bundle) request(ca testCA, name string) Request {
	return Request{ItemID: "item-" + name, Name: name, CertificateID: "cert-1", CertPEM: b.cert, ChainPEM: ca.pem,
		KeyPEM: b.key, HasKey: true}
}

// --- store under test ---

type harness struct {
	t     *testing.T
	st    *Store
	fs    *memFS
	hooks *fakeHooks
	clk   *clock
	log   *logBuf
	ca    testCA
}

func testConfig() Config {
	return Config{Dir: "/etc/inventory-agent/certs", Owner: "root", Group: "www-data", DirMode: 0o750, CertMode: 0o644,
		KeyMode: 0o640, KeepPrevious: 1, HookTimeout: 300 * time.Second}
}

func newHarness(t *testing.T, mut func(*Config)) *harness {
	t.Helper()
	cfg := testConfig()
	if mut != nil {
		mut(&cfg)
	}
	h := &harness{t: t, fs: newMemFS(), hooks: newFakeHooks(), clk: &clock{t: testNow}, log: &logBuf{}, ca: newCA(t)}
	h.st = New(cfg, Deps{FS: h.fs, Accounts: fakeAccounts{users: map[string]int{"root": 0, "nginx": 101},
		groups: map[string]int{"root": 0, "www-data": 33}}, Hooks: h.hooks, RootUID: 0, Now: h.clk.now, Logf: h.log.logf})
	return h
}

func (h *harness) install(req Request) Result {
	h.t.Helper()
	h.clk.advance(time.Second)
	return h.st.Install(context.Background(), req)
}

func (h *harness) mustInstall(req Request, state string) Result {
	h.t.Helper()
	res := h.install(req)
	if res.State != state {
		h.t.Fatalf("install %s: %+v (want %s)\nlog:\n%s", req.Name, res, state, h.log)
	}
	return res
}
