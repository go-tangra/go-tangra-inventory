package agentcerts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

var errBoom = errors.New("boom")

func TestConfigFromAgentConfig(t *testing.T) {
	c := config.DefaultAgent().Certificates
	c.DeployHook, c.KeyMode, c.Group = "/usr/local/sbin/reload", "0640", "www-data"
	got := ConfigFrom(c)
	want := Config{Dir: "/etc/inventory-agent/certs", Owner: "root", Group: "www-data", DirMode: 0o750, CertMode: 0o644,
		KeyMode: 0o640, KeepPrevious: 1, Hook: "/usr/local/sbin/reload", HookTimeout: 5 * time.Minute}
	if got != want {
		t.Fatalf("ConfigFrom = %+v", got)
	}
}

func TestNewDefaults(t *testing.T) {
	s := New(testConfig(), Deps{})
	if s.now == nil || s.logf == nil {
		t.Fatal("defaults not set")
	}
	s.logf("certs: %s", "default logger works")
	if d := s.now(); d.IsZero() {
		t.Fatal("clock")
	}
}

func TestFileInfoKinds(t *testing.T) {
	if !(FileInfo{Mode: fs.ModeDir}).IsDir() || !(FileInfo{Mode: fs.ModeSymlink}).IsSymlink() || !(FileInfo{}).IsRegular() {
		t.Fatal("kinds")
	}
}

// TestInstallFresh: the certbot layout with modes and owners from the local
// configuration, the live link swapped via a temp link, v3 metadata.
func TestInstallFresh(t *testing.T) {
	h := newHarness(t, nil)
	b := h.ca.bundle(t, 0x4f3a)
	res := h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	parsed, _ := certmaterial.ParseBundle(b.cert, h.ca.pem, b.key, certmaterial.Options{Now: h.clk.now})
	if res.Serial != "4f3a" || res.Fingerprint != parsed.Fingerprint || res.HookExitCode != HookNotRun || res.Reason != "" {
		t.Fatalf("result = %+v", res)
	}
	gen := h.fs.liveGen(t, "www")
	if gen != "20260601T080001Z-4f3a" {
		t.Fatalf("generation = %s", gen)
	}
	for _, d := range []string{".", "live", "archive", "archive/www", "renewal", genPath("www", gen)} {
		n := h.fs.node(d)
		if n == nil || !n.mode.IsDir() || n.mode.Perm() != 0o750 || n.uid != 0 || n.gid != 33 {
			t.Fatalf("dir %s = %+v", d, n)
		}
	}
	files := map[string]struct {
		data []byte
		mode fs.FileMode
	}{
		fileCert: {parsed.CertPEM, 0o644}, fileChain: {parsed.ChainPEM, 0o644},
		fileFullChain: {parsed.FullChainPEM, 0o644}, fileKey: {b.key, 0o640},
	}
	for f, w := range files {
		n := h.fs.node(genPath("www", gen) + "/" + f)
		if n == nil || !bytes.Equal(n.data, w.data) || n.mode != w.mode || n.uid != 0 || n.gid != 33 {
			t.Fatalf("file %s = %+v", f, n)
		}
	}
	if h.fs.node("live/.www.tmp") != nil || h.fs.node("renewal/.www.json.tmp") != nil {
		t.Fatal("temp entries left")
	}
	mn := h.fs.node("renewal/www.json")
	if mn == nil || mn.mode != 0o644 || mn.gid != 33 {
		t.Fatalf("metadata node = %+v", mn)
	}
	var raw map[string]any
	if err := json.Unmarshal(mn.data, &raw); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "common_name", "serial_number", "fingerprint", "issued_at", "expires_at", "last_updated",
		"issuer_name", "dns_names", "ip_addresses", "renewal_count", "certificate_id", "item_id", "generation", "has_key"} {
		if _, ok := raw[k]; !ok {
			t.Fatalf("metadata lacks %s: %s", k, mn.data)
		}
	}
	m := h.st.readMeta("www")
	if m.CommonName != "www.example.com" || m.IssuerName != "Test Issuing CA" || m.SerialNumber != "4f3a" || m.ItemID != "item-www" ||
		m.CertificateID != "cert-1" || m.Generation != gen || !m.HasKey || m.RenewalCount != 0 || m.PreviousSerial != "" ||
		len(m.DNSNames) != 2 || len(m.IPAddresses) != 1 || m.LastHookExecution != nil {
		t.Fatalf("metadata = %+v", m)
	}
	if bytes.Contains(mn.data, []byte("PRIVATE KEY")) || bytes.Contains(mn.data, []byte("BEGIN")) {
		t.Fatal("metadata holds material")
	}
	if !strings.Contains(h.log.String(), "name www installed serial 4f3a") || strings.Contains(h.log.String(), "PRIVATE") {
		t.Fatalf("log = %s", h.log)
	}
}

// TestRenewalAndPruning: a renewal is a new generation with previous_serial
// and renewal_count; keep_previous generations stay, older ones go.
func TestRenewalAndPruning(t *testing.T) {
	h := newHarness(t, nil)
	a, b, c := h.ca.bundle(t, 0xa1), h.ca.bundle(t, 0xb2), h.ca.bundle(t, 0xc3)
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
	genA := h.fs.liveGen(t, "www")
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	genB := h.fs.liveGen(t, "www")
	if m := h.st.readMeta("www"); m.PreviousSerial != "a1" || m.RenewalCount != 1 {
		t.Fatalf("meta after renewal = %+v", m)
	}
	if h.fs.node(genPath("www", genA)) == nil {
		t.Fatal("previous generation pruned too early")
	}
	h.mustInstall(c.request(h.ca, "www"), store.DeliveryInstalled)
	if h.fs.node(genPath("www", genA)) != nil || h.fs.node(genPath("www", genB)) == nil {
		t.Fatal("pruning kept the wrong generations")
	}
	if m := h.st.readMeta("www"); m.PreviousSerial != "b2" || m.RenewalCount != 2 {
		t.Fatalf("meta = %+v", m)
	}
	if !bytes.Equal(h.fs.liveFile(t, "www", fileKey), c.key) {
		t.Fatal("live key is not the new key")
	}

	// keep_previous 0: only the live generation stays.
	h0 := newHarness(t, func(c *Config) { c.KeepPrevious = 0 })
	h0.mustInstall(a.request(h0.ca, "api"), store.DeliveryInstalled)
	h0.mustInstall(b.request(h0.ca, "api"), store.DeliveryInstalled)
	if gens, _ := h0.st.generations("api"); len(gens) != 1 {
		t.Fatalf("generations = %v", gens)
	}
}

// TestReinstallKeepsRenewalHistory: a reinstall of the same certificate
// (tampered files) keeps previous_serial and renewal_count.
func TestReinstallKeepsRenewalHistory(t *testing.T) {
	h := newHarness(t, nil)
	a, b := h.ca.bundle(t, 0xa1), h.ca.bundle(t, 0xb2)
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	h.fs.node(genPath("www", h.fs.liveGen(t, "www")) + "/" + fileCert).data = []byte("tampered")
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	if m := h.st.readMeta("www"); m.PreviousSerial != "a1" || m.RenewalCount != 1 {
		t.Fatalf("meta = %+v", m)
	}
}

// TestCertificateOnly: a key-less bundle carries a matching key over,
// refuses a non-matching one without changing anything, and installs
// without a key when there is none.
func TestCertificateOnly(t *testing.T) {
	h := newHarness(t, nil)
	a := h.ca.bundle(t, 0xa1)
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)

	renewed := h.ca.reissue(t, a, 0xa2)
	req := renewed.request(h.ca, "www")
	req.KeyPEM, req.HasKey = nil, false
	h.mustInstall(req, store.DeliveryInstalled)
	if !bytes.Equal(h.fs.liveFile(t, "www", fileKey), a.key) {
		t.Fatal("matching key not carried over")
	}
	if m := h.st.readMeta("www"); !m.HasKey || m.SerialNumber != "a2" {
		t.Fatalf("meta = %+v", m)
	}

	other := h.ca.bundle(t, 0xb1)
	req = other.request(h.ca, "www")
	req.KeyPEM, req.HasKey = nil, false
	gen := h.fs.liveGen(t, "www")
	h.fs.resetWrites()
	res := h.install(req)
	if res.State != store.DeliveryFailed || res.Reason != store.ReasonKeyMismatch || h.fs.writeCount() != 0 || h.fs.liveGen(t, "www") != gen {
		t.Fatalf("mismatch = %+v writes=%d", res, h.fs.writeCount())
	}

	req = other.request(h.ca, "nokey")
	req.KeyPEM, req.HasKey = nil, false
	h.mustInstall(req, store.DeliveryInstalled)
	if h.fs.liveFile(t, "nokey", fileKey) != nil || h.st.readMeta("nokey").HasKey {
		t.Fatal("key-less install wrote a key")
	}
	// The key-less install is idempotent too.
	h.mustInstall(req, store.DeliveryUnchanged)
	// A key read failure of the current generation is a write failure.
	req = renewed.request(h.ca, "www")
	req.CertPEM, req.KeyPEM, req.HasKey = h.ca.leaf(t, a.k, 0xa3, "www.example.com"), nil, false
	h.fs.setFail("ReadFile "+genPath("www", gen)+"/"+fileKey, errBoom)
	if res := h.install(req); res.Reason != store.ReasonWriteFailed || res.Detail != "read current key" {
		t.Fatalf("key read = %+v", res)
	}
}

func TestInstallRejectsInvalidInput(t *testing.T) {
	h := newHarness(t, nil)
	b := h.ca.bundle(t, 7)
	cases := []struct {
		name   string
		mut    func(*Request)
		reason string
	}{
		{"name traversal", func(r *Request) { r.Name = "../etc" }, store.ReasonInvalidName},
		{"name hidden", func(r *Request) { r.Name = ".hidden" }, store.ReasonInvalidName},
		{"name slash", func(r *Request) { r.Name = "a/b" }, store.ReasonInvalidName},
		{"garbage", func(r *Request) { r.CertPEM = []byte("not pem") }, store.ReasonInvalidBundle},
		{"key required", func(r *Request) { r.KeyPEM = nil }, store.ReasonInvalidBundle},
		{"key mismatch", func(r *Request) { _, r.KeyPEM = newKey(t) }, store.ReasonKeyMismatch},
		{"oversized", func(r *Request) { r.ChainPEM = bytes.Repeat([]byte("a"), certmaterial.MaxChainBytes+1) }, store.ReasonBundleTooLarge},
	}
	for _, c := range cases {
		req := b.request(h.ca, "www")
		c.mut(&req)
		if res := h.install(req); res.State != store.DeliveryFailed || res.Reason != c.reason || res.HookExitCode != HookNotRun {
			t.Fatalf("%s: %+v", c.name, res)
		}
	}
	h.clk.t = validTo.AddDate(0, 0, 1)
	if res := h.install(b.request(h.ca, "www")); res.Reason != store.ReasonCertificateNotValid {
		t.Fatalf("expired: %+v", res)
	}
	if h.fs.writeCount() != 0 {
		t.Fatal("invalid input wrote something")
	}
}

func TestOwnerResolution(t *testing.T) {
	b := newCA(t)
	for _, c := range []struct {
		owner, group, detail string
	}{{"nobody-here", "root", "owner"}, {"root", "nogroup-here", "group"}} {
		h := newHarness(t, func(cfg *Config) { cfg.Owner, cfg.Group = c.owner, c.group })
		h.ca = b
		res := h.install(h.ca.bundle(t, 1).request(h.ca, "www"))
		if res.Reason != store.ReasonOwnerUnknown || res.Detail != c.detail {
			t.Fatalf("%+v", res)
		}
	}
	// Numeric ids need no lookup; a named service user owns the files.
	h := newHarness(t, func(cfg *Config) { cfg.Owner, cfg.Group = "nginx", "1001" })
	h.mustInstall(h.ca.bundle(t, 1).request(h.ca, "www"), store.DeliveryInstalled)
	n := h.fs.node(genPath("www", h.fs.liveGen(t, "www")) + "/" + fileKey)
	if n.uid != 101 || n.gid != 1001 {
		t.Fatalf("key owner = %d:%d", n.uid, n.gid)
	}
	if d := h.fs.node(genPath("www", h.fs.liveGen(t, "www"))); d.uid != 0 || d.gid != 1001 {
		t.Fatalf("generation dir owner = %d:%d (directories stay root-owned)", d.uid, d.gid)
	}
	if !strings.Contains(strings.Join(h.fs.chowns, ","), genPath("www", h.fs.liveGen(t, "www"))+" 0:1001") {
		t.Fatalf("chowns = %v", h.fs.chowns)
	}
}

// TestUnsafeDirectories: a store directory that is a symlink or not owned by
// root, or a live entry that is a real directory, refuses the install.
func TestUnsafeDirectories(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(m *memFS)
	}{
		{"live symlink", func(m *memFS) { m.nodes["live"] = &memNode{mode: fs.ModeSymlink | 0o777, target: "/etc"} }},
		{"archive not root-owned", func(m *memFS) { m.nodes["archive"] = &memNode{mode: fs.ModeDir | 0o750, uid: 1000} }},
		{"root not root-owned", func(m *memFS) { m.nodes["."].uid = 1000 }},
		{"renewal is a file", func(m *memFS) { m.nodes["renewal"] = &memNode{mode: 0o644} }},
		{"live/www is a directory", func(m *memFS) {
			m.nodes["live"] = &memNode{mode: fs.ModeDir | 0o750}
			m.nodes["live/www"] = &memNode{mode: fs.ModeDir | 0o750}
		}},
	} {
		h := newHarness(t, nil)
		c.setup(h.fs)
		res := h.install(h.ca.bundle(t, 1).request(h.ca, "www"))
		if res.State != store.DeliveryFailed || res.Reason != store.ReasonWriteFailed || res.Detail != detailUnsafeDir {
			t.Fatalf("%s: %+v", c.name, res)
		}
		if n := h.fs.node("archive/www"); n != nil && len(mustGens(t, h, "www")) != 0 {
			t.Fatalf("%s: a generation was written", c.name)
		}
	}
}

func mustGens(t *testing.T, h *harness, name string) []string {
	t.Helper()
	g, err := h.st.generations(name)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestForeignLiveLinkIsReplaced: a live link that does not point into the
// archive (or at a missing generation) is never followed and gets replaced.
func TestForeignLiveLinkIsReplaced(t *testing.T) {
	for _, target := range []string{"/etc/shadow", "../archive/www/20260101T000000Z-aa"} {
		h := newHarness(t, nil)
		h.fs.nodes["live"] = &memNode{mode: fs.ModeDir | 0o750}
		h.fs.nodes["live/www"] = &memNode{mode: fs.ModeSymlink | 0o777, target: target}
		h.mustInstall(h.ca.bundle(t, 1).request(h.ca, "www"), store.DeliveryInstalled)
		if h.fs.liveGen(t, "www") == "" {
			t.Fatal("not replaced")
		}
	}
	// A generation directory not owned by root is not trusted either.
	h := newHarness(t, nil)
	b := h.ca.bundle(t, 1)
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	h.fs.node(genPath("www", h.fs.liveGen(t, "www"))).uid = 1000
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
}

// TestDiskFull: free space below 4x the bundle, or ENOSPC while writing,
// fails with disk_full and leaves the previous live set in place.
func TestDiskFull(t *testing.T) {
	h := newHarness(t, nil)
	a, b := h.ca.bundle(t, 0xa1), h.ca.bundle(t, 0xb2)
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
	gen := h.fs.liveGen(t, "www")

	h.fs.free = 100
	if res := h.install(b.request(h.ca, "www")); res.Reason != store.ReasonDiskFull || res.Detail != detailDiskFull {
		t.Fatalf("free check: %+v", res)
	}
	h.fs.free = 1 << 40
	h.fs.setFail("WriteFile *", &fs.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC})
	if res := h.install(b.request(h.ca, "www")); res.Reason != store.ReasonDiskFull {
		t.Fatalf("ENOSPC: %+v", res)
	}
	if h.fs.liveGen(t, "www") != gen || !bytes.Equal(h.fs.liveFile(t, "www", fileKey), a.key) {
		t.Fatal("previous live set changed")
	}
	if gens := mustGens(t, h, "www"); len(gens) != 1 {
		t.Fatalf("staged generation left behind: %v", gens)
	}
	// Free space unknown: the check is skipped.
	h.fs.clearFail()
	h.fs.freeErr = errBoom
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
}

// TestFailureAfterStagingLeavesLiveUntouched: every failing step before the
// swap fails the install, removes the staged generation and keeps live.
func TestFailureAfterStagingLeavesLiveUntouched(t *testing.T) {
	steps := []struct {
		key, detail string
	}{
		{"Lstat .", "prepare directories"},
		{"Mkdir live", "prepare directories"},
		{"Lchown live", "prepare directories"},
		{"Chmod live", "prepare directories"},
		{"Lstat live/www", "read current generation"},
		{"Readlink live/www", "read current generation"},
		{"Lstat GEN", "write generation"},
		{"Mkdir GEN", "write generation"},
		{"Lchown GEN", "write generation"},
		{"WriteFile GEN/privkey.pem", "write generation"},
		{"SyncDir GEN", "write generation"},
		{"Chmod GEN", "write generation"},
		{"SyncDir archive/www", "write generation"},
		{"RemoveAll live/.www.tmp", "switch live link"},
		{"Symlink live/.www.tmp", "switch live link"},
		{"Rename live/www", "switch live link"},
	}
	for _, s := range steps {
		h := newHarness(t, nil)
		a, b := h.ca.bundle(t, 0xa1), h.ca.bundle(t, 0xb2)
		fresh := strings.HasSuffix(s.key, " live")
		if !fresh {
			h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
		}
		var prev string
		if !fresh {
			prev = h.fs.liveGen(t, "www")
		}
		next := h.clk.now().Add(time.Second).UTC().Format("20060102T150405Z") + "-b2"
		h.fs.setFail(strings.ReplaceAll(s.key, "GEN", genPath("www", next)), errBoom)
		res := h.install(b.request(h.ca, "www"))
		if res.State != store.DeliveryFailed || res.Reason != store.ReasonWriteFailed || res.Detail != s.detail {
			t.Fatalf("%s: %+v", s.key, res)
		}
		h.fs.clearFail()
		if fresh {
			continue
		}
		if h.fs.liveGen(t, "www") != prev {
			t.Fatalf("%s: live changed", s.key)
		}
		if h.fs.node(genPath("www", next)) != nil {
			t.Fatalf("%s: staged generation left behind", s.key)
		}
	}
}

// TestFailureAfterSwapStillInstalled: once live points at the new set the
// install is reported installed; later steps only log.
func TestFailureAfterSwapStillInstalled(t *testing.T) {
	for _, key := range []string{"SyncDir live", "RemoveAll renewal/.www.json.tmp", "WriteFile renewal/.www.json.tmp",
		"Rename renewal/www.json", "SyncDir renewal", "ReadDir archive/www", "RemoveAll archive/www/OLD"} {
		h := newHarness(t, func(c *Config) { c.KeepPrevious = 0 })
		a, b := h.ca.bundle(t, 0xa1), h.ca.bundle(t, 0xb2)
		h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
		h.fs.setFail(strings.ReplaceAll(key, "OLD", h.fs.liveGen(t, "www")), errBoom)
		h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
		if !bytes.Equal(h.fs.liveFile(t, "www", fileKey), b.key) {
			t.Fatalf("%s: live set not switched", key)
		}
		if !strings.Contains(h.log.String(), "boom") {
			t.Fatalf("%s: failure not logged: %s", key, h.log)
		}
	}
}

// TestSameSecondGenerations: reinstalling within one second gets a suffixed
// generation; too many collisions fail.
func TestSameSecondGenerations(t *testing.T) {
	h := newHarness(t, nil)
	b := h.ca.bundle(t, 0xb2)
	h.mustInstall(b.request(h.ca, "www"), store.DeliveryInstalled)
	base := h.fs.liveGen(t, "www")
	h.fs.node(genPath("www", base) + "/" + fileCert).data = []byte("tampered")
	h.st.Install(context.Background(), b.request(h.ca, "www")) // same clock second
	if got := h.fs.liveGen(t, "www"); got != base+"-1" {
		t.Fatalf("generation = %s", got)
	}
	for i := 2; i <= maxGenSuffix; i++ {
		h.fs.nodes[genPath("www", base)+"-"+string(rune('0'+i))] = &memNode{mode: fs.ModeDir | 0o750}
	}
	h.fs.node(genPath("www", base+"-1") + "/" + fileCert).data = []byte("tampered")
	if res := h.st.Install(context.Background(), b.request(h.ca, "www")); res.Reason != store.ReasonWriteFailed {
		t.Fatalf("collisions: %+v", res)
	}
}

func TestParseTarget(t *testing.T) {
	for target, ok := range map[string]bool{
		"../archive/www/20260601T080000Z-4f3a":           true,
		"../archive/www/20260601T080000Z-4f3a-1":         true,
		"../archive/www/":                                false,
		"../archive/www/../../etc":                       false,
		"/etc/inventory-agent/certs/archive/www/x":       false,
		"../archive/other/20260601T080000Z-4f3a":         false,
		"../archive/www/20260601T080000Z-4f3a/../../etc": false,
	} {
		if _, got := parseTarget("www", target); got != ok {
			t.Fatalf("%s = %v", target, got)
		}
	}
	if g := New(testConfig(), Deps{Now: func() time.Time { return testNow }}).generation("0123456789abcdef0123"); g != "20260601T080000Z-0123456789abcdef" {
		t.Fatalf("generation = %s", g)
	}
}

// TestRecover: leftovers of a crash are cleaned up at start.
func TestRecover(t *testing.T) {
	h := newHarness(t, nil)
	a := h.ca.bundle(t, 0xa1)
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
	h.mustInstall(a.request(h.ca, "api"), store.DeliveryInstalled)
	live := h.fs.liveGen(t, "www")
	newer := "29990101T000000Z-ff"
	older := "20000101T000000Z-01"
	for _, g := range []string{newer, older} {
		h.fs.nodes[genPath("www", g)] = &memNode{mode: fs.ModeDir | 0o700}
		h.fs.nodes[genPath("www", g)+"/cert.pem"] = &memNode{mode: 0o644}
	}
	h.fs.nodes["live/.www.tmp"] = &memNode{mode: fs.ModeSymlink, target: "x"}
	h.fs.nodes["renewal/.www.json.tmp"] = &memNode{mode: 0o644}
	h.fs.nodes["archive/orphan"] = &memNode{mode: fs.ModeDir | 0o750}
	h.fs.nodes[genPath("orphan", newer)] = &memNode{mode: fs.ModeDir | 0o700}
	h.fs.nodes["archive/.junk"] = &memNode{mode: fs.ModeDir | 0o750}
	h.fs.nodes["archive/evil"] = &memNode{mode: fs.ModeSymlink, target: "/"}
	if err := h.st.Recover(); err != nil {
		t.Fatal(err)
	}
	if h.fs.node(genPath("www", newer)) != nil || h.fs.node(genPath("www", newer)+"/cert.pem") != nil {
		t.Fatal("unswitched generation kept")
	}
	if h.fs.node(genPath("www", older)) == nil || h.fs.node(genPath("www", live)) == nil {
		t.Fatal("recovery removed a switched generation")
	}
	if h.fs.node("live/.www.tmp") != nil || h.fs.node("renewal/.www.json.tmp") != nil {
		t.Fatal("temp entries kept")
	}
	if h.fs.node(genPath("orphan", newer)) != nil || h.fs.node("archive/.junk") == nil || h.fs.node("archive/evil") == nil {
		t.Fatal("orphan/junk handling")
	}
	// A recovered store still installs and reports unchanged.
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryUnchanged)
}

func TestRecoverEdgeCases(t *testing.T) {
	// Nothing there yet.
	h := newHarness(t, nil)
	if err := h.st.Recover(); err != nil {
		t.Fatal(err)
	}
	// Unsafe store directories are refused.
	h.fs.nodes["live"] = &memNode{mode: fs.ModeSymlink, target: "/etc"}
	if err := h.st.Recover(); !errors.Is(err, errUnsafe) {
		t.Fatalf("live symlink: %v", err)
	}
	delete(h.fs.nodes, "live")
	h.fs.nodes["archive"] = &memNode{mode: fs.ModeDir | 0o750, uid: 1000}
	if err := h.st.Recover(); !errors.Is(err, errUnsafe) {
		t.Fatalf("archive owner: %v", err)
	}
	delete(h.fs.nodes, "archive")
	h.fs.setFail("Lstat renewal", errBoom)
	if err := h.st.Recover(); !errors.Is(err, errBoom) {
		t.Fatalf("lstat: %v", err)
	}
	h.fs.clearFail()

	// Failing removals and reads are reported or logged.
	h = newHarness(t, nil)
	a := h.ca.bundle(t, 0xa1)
	h.mustInstall(a.request(h.ca, "www"), store.DeliveryInstalled)
	h.fs.nodes["live/.www.tmp"] = &memNode{mode: fs.ModeSymlink, target: "x"}
	h.fs.setFail("RemoveAll live/.www.tmp", errBoom)
	if err := h.st.Recover(); !errors.Is(err, errBoom) {
		t.Fatalf("remove temp: %v", err)
	}
	h.fs.clearFail()
	delete(h.fs.nodes, "live/.www.tmp")
	newer := genPath("www", "29990101T000000Z-ff")
	h.fs.nodes[newer] = &memNode{mode: fs.ModeDir | 0o700}
	h.fs.setFail("RemoveAll "+newer, errBoom)
	if err := h.st.Recover(); err != nil || !strings.Contains(h.log.String(), "remove 29990101T000000Z-ff") {
		t.Fatalf("remove generation: %v %s", err, h.log)
	}
	h.fs.clearFail()
	h.fs.setFail("ReadDir archive/www", errBoom)
	if err := h.st.Recover(); err != nil || !strings.Contains(h.log.String(), "recovery: name www: boom") {
		t.Fatalf("readdir: %v", err)
	}
	h.fs.clearFail()
	h.fs.setFail("Readlink live/www", errBoom)
	if err := h.st.Recover(); err != nil || h.fs.node(newer) == nil {
		t.Fatalf("readlink: %v (a generation must not be removed when live is unreadable)", err)
	}
}
