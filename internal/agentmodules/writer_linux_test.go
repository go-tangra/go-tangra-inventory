//go:build linux

package agentmodules

import (
	"archive/zip"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

var ctx = context.Background()

func mode(t *testing.T, p string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

// TestInstallWritesTree (T012/T013): the tree lands at <dir>/<module>
// exactly as the archive's <module>/ tree; .env and private/ are 0600
// (private/ directories 0700), files marked executable 0755, others 0644,
// directories 0750; nothing is left in staging; no hook ran.
func TestInstallWritesTree(t *testing.T) {
	h := newHarness(t, "")
	res := h.w.Install(ctx, request("sms-gw", smsGW(t)))
	if res.State != store.DeliveryInstalled || res.Reason != "" || res.HookExitCode != -1 {
		t.Fatalf("result = %+v", res)
	}
	m := filepath.Join(h.dir, "sms-gw")
	for rel, want := range map[string]fs.FileMode{
		"":                    fs.ModeDir | 0o750,
		"compose.yaml":        0o644,
		"config":              fs.ModeDir | 0o750,
		"config/app.yaml":     0o644,
		".env":                0o600,
		"private":             fs.ModeDir | 0o700,
		"private/db":          fs.ModeDir | 0o700,
		"private/db/password": 0o600,
		"bin":                 fs.ModeDir | 0o750,
		"bin/start.sh":        0o755,
	} {
		if got := mode(t, filepath.Join(m, rel)); got != want {
			t.Errorf("%s: mode %v, want %v", rel, got, want)
		}
	}
	if b, _ := os.ReadFile(filepath.Join(m, ".env")); string(b) != "JOIN_TOKEN=secret-token\nDB_PASSWORD=pw\n" {
		t.Fatalf(".env = %q", b)
	}
	fi, _ := os.Lstat(filepath.Join(m, "compose.yaml"))
	if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != os.Getuid() || int(st.Gid) != os.Getgid() {
		t.Fatalf("owner = %d:%d", st.Uid, st.Gid)
	}
	if got := h.entries(t); len(got) != 1 || got[0] != "sms-gw" {
		t.Fatalf("modules directory = %v", got)
	}
	if strings.Contains(h.log.String(), "secret-token") {
		t.Fatal("bundle content logged")
	}
	if len(h.hooks.runs) != 0 {
		t.Fatal("hook ran without configuration")
	}
}

// TestInstallRefusesExisting (T012, R5): an existing <dir>/<module> is
// never touched.
func TestInstallRefusesExisting(t *testing.T) {
	h := newHarness(t, "/usr/local/sbin/module-up")
	if err := os.Mkdir(filepath.Join(h.dir, "sms-gw"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(h.dir, "sms-gw", ".env"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := h.w.Install(ctx, request("sms-gw", smsGW(t)))
	if res.State != store.DeliveryFailed || res.Reason != store.ReasonAlreadyInstalled || res.HookExitCode != -1 {
		t.Fatalf("result = %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(h.dir, "sms-gw", ".env")); string(b) != "old" {
		t.Fatal("existing module changed")
	}
	if len(h.entries(t)) != 1 || len(h.hooks.runs) != 0 {
		t.Fatal("staging left or hook run")
	}
	// A symlink of that name counts as existing too.
	h2 := newHarness(t, "")
	_ = os.Symlink("/etc", filepath.Join(h2.dir, "sms-gw"))
	if res := h2.w.Install(ctx, request("sms-gw", smsGW(t))); res.Reason != store.ReasonAlreadyInstalled {
		t.Fatalf("symlink: %+v", res)
	}
}

// TestInstallRefusesHostileArchives (T012, SR-005): nothing is written.
func TestInstallRefusesHostileArchives(t *testing.T) {
	ok := zent{name: "sms-gw/compose.yaml", body: "x"}
	many := []zent{}
	for i := 0; i <= MaxEntries; i++ {
		many = append(many, zent{name: "sms-gw/f" + string(rune('a'+i%26)) + strings.Repeat("x", i/26)})
	}
	big := strings.Repeat("\x00", MaxUnpacked/2+1)
	cases := map[string]struct {
		entries []zent
		reason  string
	}{
		"absolute":           {[]zent{ok, {name: "/etc/cron.d/x"}}, store.ReasonInvalidBundle},
		"dot dot":            {[]zent{ok, {name: "../x"}}, store.ReasonInvalidBundle},
		"inner dot dot":      {[]zent{ok, {name: "sms-gw/../x"}}, store.ReasonInvalidBundle},
		"dot component":      {[]zent{ok, {name: "sms-gw/./x"}}, store.ReasonInvalidBundle},
		"double slash":       {[]zent{ok, {name: "sms-gw//x"}}, store.ReasonInvalidBundle},
		"backslash":          {[]zent{ok, {name: "sms-gw\\..\\x"}}, store.ReasonInvalidBundle},
		"outside module":     {[]zent{ok, {name: "other/x"}}, store.ReasonInvalidBundle},
		"module prefix only": {[]zent{ok, {name: "sms-gw-evil/x"}}, store.ReasonInvalidBundle},
		"module is a file":   {[]zent{{name: "sms-gw", body: "x"}}, store.ReasonInvalidBundle},
		"symlink":            {[]zent{ok, {name: "sms-gw/link", body: "/etc/shadow", mode: fs.ModeSymlink | 0o777}}, store.ReasonInvalidBundle},
		"device":             {[]zent{ok, {name: "sms-gw/dev", mode: fs.ModeDevice | 0o600}}, store.ReasonInvalidBundle},
		"dir without slash":  {[]zent{ok, {name: "sms-gw/d", mode: fs.ModeDir | 0o755}}, store.ReasonInvalidBundle},
		"duplicate file":     {[]zent{ok, ok}, store.ReasonInvalidBundle},
		"duplicate dir":      {[]zent{ok, {name: "sms-gw/d/"}, {name: "sms-gw/d/"}}, store.ReasonInvalidBundle},
		"file then dir":      {[]zent{ok, {name: "sms-gw/d/"}, {name: "sms-gw/compose.yaml/"}}, store.ReasonInvalidBundle},
		"dir then file":      {[]zent{{name: "sms-gw/d/"}, {name: "sms-gw/d", body: "x"}}, store.ReasonInvalidBundle},
		"file as parent":     {[]zent{ok, {name: "sms-gw/compose.yaml/x", body: "x"}}, store.ReasonInvalidBundle},
		"encrypted":          {[]zent{{name: "sms-gw/x", body: "x", raw: func(h *zip.FileHeader) { h.Flags |= 0x1 }}}, store.ReasonInvalidBundle},
		"no files":           {[]zent{{name: "sms-gw/"}, {name: "sms-gw/d/"}}, store.ReasonInvalidBundle},
		"over 300 entries":   {many, store.ReasonTooLarge},
		"over 16 MiB":        {[]zent{{name: "sms-gw/a", body: big}, {name: "sms-gw/b", body: big}}, store.ReasonTooLarge},
	}
	for name, c := range cases {
		h := newHarness(t, "/usr/local/sbin/module-up")
		res := h.w.Install(ctx, request("sms-gw", zipOf(t, c.entries...)))
		if res.State != store.DeliveryFailed || res.Reason != c.reason {
			t.Errorf("%s: %+v, want %s", name, res, c.reason)
		}
		if got := h.entries(t); len(got) != 0 || len(h.hooks.runs) != 0 {
			t.Errorf("%s: wrote %v", name, got)
		}
	}
}

// TestInstallChecksRequest (T012): module name, version, size and digest.
func TestInstallChecksRequest(t *testing.T) {
	h := newHarness(t, "")
	data := smsGW(t)
	cases := map[string]struct {
		mut    func(*Request)
		reason string
	}{
		"traversal module": {func(r *Request) { r.Module = "../etc" }, store.ReasonInvalidModule},
		"upper module":     {func(r *Request) { r.Module = "SMS" }, store.ReasonInvalidModule},
		"bad version":      {func(r *Request) { r.Version = "1.2.0\nX=1" }, store.ReasonInvalidBundle},
		"long version":     {func(r *Request) { r.Version = "1.2.3-" + strings.Repeat("a", 60) }, store.ReasonInvalidBundle},
		"digest":           {func(r *Request) { r.SHA256 = strings.Repeat("0", 64) }, store.ReasonChecksumMismatch},
		"size":             {func(r *Request) { r.Size++ }, store.ReasonChecksumMismatch},
		"declared size":    {func(r *Request) { r.Size = store.MaxModuleBundleBytes + 1 }, store.ReasonTooLarge},
		"actual size":      {func(r *Request) { r.Data = make([]byte, store.MaxModuleBundleBytes+1) }, store.ReasonTooLarge},
		"not a zip":        {func(r *Request) { *r = request("sms-gw", []byte("not a zip")) }, store.ReasonInvalidBundle},
		"other module":     {func(r *Request) { *r = request("mail", data) }, store.ReasonInvalidBundle},
	}
	for name, c := range cases {
		r := request("sms-gw", data)
		c.mut(&r)
		if res := h.w.Install(ctx, r); res.State != store.DeliveryFailed || res.Reason != c.reason {
			t.Errorf("%s: %+v, want %s", name, res, c.reason)
		}
	}
	if len(h.entries(t)) != 0 {
		t.Fatal("wrote something")
	}
}

// TestInstallFailuresCleanStaging (T012): every write failure removes the
// staging tree and reports write_failed or disk_full.
func TestInstallFailuresCleanStaging(t *testing.T) {
	enospc := &fs.PathError{Op: "write", Err: syscall.ENOSPC}
	cases := map[string]struct {
		op     string
		err    error
		reason string
	}{
		"mkdir staging":   {"Mkdir:.staging", errors.New("eio"), store.ReasonWriteFailed},
		"mkdir subdir":    {"Mkdir:.staging-sms-gw-0001020304050607/config", errors.New("eio"), store.ReasonWriteFailed},
		"write":           {"WriteFile", errors.New("eio"), store.ReasonWriteFailed},
		"disk full":       {"WriteFile", enospc, store.ReasonDiskFull},
		"chmod":           {"Chmod", errors.New("eperm"), store.ReasonWriteFailed},
		"chown":           {"Lchown", errors.New("eperm"), store.ReasonWriteFailed},
		"rename":          {"Rename", errors.New("exdev"), store.ReasonWriteFailed},
		"rename existing": {"Rename", &os.LinkError{Op: "rename", Err: syscall.ENOTEMPTY}, store.ReasonAlreadyInstalled},
		"inspect":         {"Lstat:sms-gw", errors.New("eio"), store.ReasonWriteFailed},
		"directory":       {"Lstat:.", errors.New("eio"), store.ReasonWriteFailed},
	}
	for name, c := range cases {
		h := newHarness(t, "")
		h.w.rand = strings.NewReader("\x00\x01\x02\x03\x04\x05\x06\x07")
		h.fs.errs[c.op] = c.err
		res := h.w.Install(ctx, request("sms-gw", smsGW(t)))
		if res.State != store.DeliveryFailed || res.Reason != c.reason {
			t.Errorf("%s: %+v, want %s", name, res, c.reason)
		}
		if got := h.entries(t); len(got) != 0 {
			t.Errorf("%s: left %v", name, got)
		}
	}
	// No randomness: refused before anything is written.
	h := newHarness(t, "")
	h.w.rand = strings.NewReader("")
	if res := h.w.Install(ctx, request("sms-gw", smsGW(t))); res.Reason != store.ReasonWriteFailed {
		t.Fatalf("rand: %+v", res)
	}
	// Not enough free space.
	h = newHarness(t, "")
	free := uint64(10)
	h.fs.free = &free
	if res := h.w.Install(ctx, request("sms-gw", smsGW(t))); res.Reason != store.ReasonDiskFull {
		t.Fatalf("free: %+v", res)
	}
	// A failed directory sync is only logged.
	h = newHarness(t, "")
	h.fs.errs["SyncDir"] = errors.New("eio")
	if res := h.w.Install(ctx, request("sms-gw", smsGW(t))); res.State != store.DeliveryInstalled || !strings.Contains(h.log.String(), "not synced") {
		t.Fatalf("sync: %+v", res)
	}
}

// TestInstallUnsafeDirectoryAndOwner: a modules directory writable by
// others, or an unknown owner or group, is refused before writing.
func TestInstallUnsafeDirectoryAndOwner(t *testing.T) {
	h := newHarness(t, "")
	if err := os.Chmod(h.dir, 0o777); err != nil {
		t.Fatal(err)
	}
	if res := h.w.Install(ctx, request("sms-gw", smsGW(t))); res.Reason != store.ReasonWriteFailed || res.Detail != "unsafe_directory" {
		t.Fatalf("unsafe: %+v", res)
	}
	if err := h.w.Recover(); err == nil {
		t.Fatal("unsafe directory recovered")
	}
	_ = os.Chmod(h.dir, 0o700)
	for name, mut := range map[string]func(*Config){
		"owner": func(c *Config) { c.Owner = "nobody-here" },
		"group": func(c *Config) { c.Group = "nobody-here" },
	} {
		mut(&h.w.cfg)
		if res := h.w.Install(ctx, request("sms-gw", smsGW(t))); res.Reason != store.ReasonWriteFailed {
			t.Errorf("%s: %+v", name, res)
		}
		h.w.cfg.Owner, h.w.cfg.Group = "app", "app"
	}
	if res := h.w.Install(ctx, request("sms-gw", smsGW(t))); res.State != store.DeliveryInstalled {
		t.Fatalf("named owner: %+v", res)
	}
}

// TestInstallHook (T013): the local hook runs in MODULE_DIR with exactly
// PATH, LANG and the MODULE_* variables; a refused, failing or timed-out
// hook reports hook_failed with the closed reason.
func TestInstallHook(t *testing.T) {
	const hook = "/usr/local/sbin/module-up"
	t.Setenv("SECRET_FROM_AGENT_ENV", "leak")
	h := newHarness(t, hook)
	h.hooks.out = agentcerts.HookOutcome{Output: []byte("Container sms-gw started")}
	res := h.w.Install(ctx, request("sms-gw", smsGW(t)))
	if res.State != store.DeliveryInstalled || res.HookExitCode != 0 {
		t.Fatalf("result = %+v", res)
	}
	spec := h.hooks.runs[0]
	dir := filepath.Join(h.dir, "sms-gw")
	want := "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin\nLANG=C.UTF-8\nMODULE_NAME=sms-gw\nMODULE_VERSION=1.2.0\nMODULE_DIR=" + dir
	if spec.Path != hook || spec.Dir != dir || spec.Timeout != time.Minute || strings.Join(spec.Env, "\n") != want {
		t.Fatalf("spec = %+v", spec)
	}
	if !strings.Contains(h.log.String(), "Container sms-gw started") {
		t.Fatal("hook output not logged locally")
	}
	for name, c := range map[string]struct {
		missing bool
		out     agentcerts.HookOutcome
		reason  string
		code    int
	}{
		"refused": {missing: true, reason: store.ReasonHookRefused, code: -1},
		"failed":  {out: agentcerts.HookOutcome{ExitCode: 3, Output: []byte("boom")}, reason: store.ReasonHookFailed, code: 3},
		"timeout": {out: agentcerts.HookOutcome{ExitCode: 256, TimedOut: true}, reason: store.ReasonHookTimeout, code: 256},
	} {
		h := newHarness(t, hook)
		h.hooks.missing, h.hooks.out = c.missing, c.out
		res := h.w.Install(ctx, request("sms-gw", smsGW(t)))
		if res.State != store.DeliveryHookFailed || res.Reason != c.reason || res.HookExitCode != c.code || strings.Contains(res.Detail, "boom") {
			t.Errorf("%s: %+v", name, res)
		}
		if _, err := os.Stat(filepath.Join(h.dir, "sms-gw", "compose.yaml")); err != nil {
			t.Errorf("%s: files not kept after the hook: %v", name, err)
		}
	}
}

// TestRecover removes interrupted writes only.
func TestRecover(t *testing.T) {
	h := newHarness(t, "")
	_ = os.MkdirAll(filepath.Join(h.dir, ".staging-sms-gw-01", "private"), 0o700)
	_ = os.WriteFile(filepath.Join(h.dir, ".staging-sms-gw-01", "private", "k"), []byte("x"), 0o600)
	_ = os.Mkdir(filepath.Join(h.dir, "mail"), 0o750)
	if err := h.w.Recover(); err != nil {
		t.Fatal(err)
	}
	if got := h.entries(t); len(got) != 1 || got[0] != "mail" || !strings.Contains(h.log.String(), "interrupted write") {
		t.Fatalf("after recover: %v", got)
	}
	h.fs.errs["ReadDir"] = errors.New("eio")
	if err := h.w.Recover(); err == nil {
		t.Fatal("read error hidden")
	}
	delete(h.fs.errs, "ReadDir")
	_ = os.Mkdir(filepath.Join(h.dir, ".staging-x"), 0o700)
	h.fs.errs["RemoveAll"] = errors.New("eio")
	if err := h.w.Recover(); err == nil {
		t.Fatal("remove error hidden")
	}
}

// TestReadEntry: a corrupt member, an unreadable one and the extraction
// budget.
func TestReadEntry(t *testing.T) {
	data := zipOf(t, zent{name: "sms-gw/a", body: strings.Repeat("a", 100), raw: func(h *zip.FileHeader) { h.Method = zip.Store }})
	entries, f := parseArchive(data, "sms-gw")
	if f != nil {
		t.Fatal(f)
	}
	file := entries[len(entries)-1]
	if _, f := readEntry(file, 10); f == nil || f.reason != store.ReasonTooLarge {
		t.Fatalf("budget: %+v", f)
	}
	i := strings.Index(string(data), strings.Repeat("a", 100))
	data[i+50] = 'b' // CRC mismatch
	entries, _ = parseArchive(data, "sms-gw")
	if _, f := readEntry(entries[len(entries)-1], MaxUnpacked); f == nil || f.reason != store.ReasonInvalidBundle {
		t.Fatalf("crc: %+v", f)
	}
	file.file.Method = 99
	if _, f := readEntry(file, MaxUnpacked); f == nil || f.detail != "unreadable entry" {
		t.Fatalf("method: %+v", f)
	}
	// Through Install: the corrupt member fails the write and cleans up.
	h := newHarness(t, "")
	if res := h.w.Install(ctx, request("sms-gw", data)); res.Reason != store.ReasonInvalidBundle || len(h.entries(t)) != 0 {
		t.Fatalf("corrupt install: %+v", res)
	}
}

func TestConstructors(t *testing.T) {
	c := ConfigFrom(config.DefaultAgent().Modules)
	if c.Dir != "/opt/tangra/modules" || c.Owner != "root" || c.Group != "root" || c.Hook != "" || c.HookTimeout != 10*time.Minute {
		t.Fatalf("config = %+v", c)
	}
	w := New(c, Deps{})
	if w.rand == nil || w.logf == nil {
		t.Fatal("defaults")
	}
	dir := filepath.Join(t.TempDir(), "m")
	w, err := NewOS(Config{Dir: dir, Owner: "0", Group: "0"}, Deps{})
	if err != nil || w.fs == nil || w.acc == nil || w.hooks == nil {
		t.Fatalf("NewOS: %v", err)
	}
	if fi, _ := os.Lstat(dir); !fi.IsDir() || fi.Mode().Perm() != 0o700 {
		t.Fatal("directory not created 0700")
	}
	file := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(file, nil, 0o600)
	if _, err := NewOS(Config{Dir: filepath.Join(file, "m")}, Deps{}); err == nil {
		t.Fatal("unusable directory accepted")
	}
	if _, err := resolveID("nobody-here", fakeAccounts{}.LookupUser); err == nil {
		t.Fatal("unknown account")
	}
}

func TestPermFor(t *testing.T) {
	for _, c := range []struct {
		rel  string
		dir  bool
		arch fs.FileMode
		want fs.FileMode
	}{
		{"", true, 0, 0o750}, {"private", true, 0, 0o700}, {"private/x", true, 0, 0o700}, {"privateer", true, 0, 0o750},
		{".env", false, 0o755, 0o600}, {"config/.env", false, 0, 0o600}, {"private/run.sh", false, 0o755, 0o600},
		{"run.sh", false, 0o700, 0o755}, {"a.yaml", false, 0o666, 0o644},
	} {
		if got := permFor(c.rel, c.dir, c.arch); got != c.want {
			t.Errorf("%s: %v, want %v", c.rel, got, c.want)
		}
	}
}
