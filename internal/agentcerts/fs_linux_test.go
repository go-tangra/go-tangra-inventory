//go:build linux

package agentcerts

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// osHarness is a store over the real filesystem in a temp dir, run as the
// test user: the "root" uid is the test uid and owner/group are numeric.
func osStore(t *testing.T, mut func(*Config)) (*Store, string, *clock) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "certs")
	cfg := testConfig()
	cfg.Dir, cfg.Owner, cfg.Group = dir, strconv.Itoa(os.Getuid()), strconv.Itoa(os.Getgid())
	if mut != nil {
		mut(&cfg)
	}
	clk := &clock{t: testNow}
	st, err := NewOS(cfg, Deps{RootUID: os.Getuid(), Now: func() time.Time { clk.advance(time.Second); return clk.now() },
		Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return st, dir, clk
}

func TestOSStoreInstall(t *testing.T) {
	st, dir, _ := osStore(t, nil)
	ca := newCA(t)
	b := ca.bundle(t, 0x4f3a)
	if res := st.Install(context.Background(), b.request(ca, "www")); res.State != store.DeliveryInstalled {
		t.Fatalf("install: %+v", res)
	}
	key, err := os.ReadFile(filepath.Join(dir, "live/www/privkey.pem"))
	if err != nil || !bytes.Equal(key, b.key) {
		t.Fatalf("live key: %v", err)
	}
	fi, _ := os.Lstat(filepath.Join(dir, "live/www/privkey.pem"))
	if fi.Mode().Perm() != 0o640 {
		t.Fatalf("key mode %v", fi.Mode())
	}
	for _, d := range []string{"", "live", "archive", "archive/www", "renewal"} {
		fi, err := os.Lstat(filepath.Join(dir, d))
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != 0o750 {
			t.Fatalf("dir %q: %v %v", d, fi.Mode(), err)
		}
	}
	target, _ := os.Readlink(filepath.Join(dir, "live/www"))
	if _, ok := parseTarget("www", target); !ok {
		t.Fatalf("link %s", target)
	}
	if res := st.Install(context.Background(), b.request(ca, "www")); res.State != store.DeliveryUnchanged {
		t.Fatalf("again: %+v", res)
	}
	if err := st.Recover(); err != nil {
		t.Fatal(err)
	}
}

// TestOSStoreSwapIsAtomic: while installs alternate between two certificates,
// readers that resolve live/<name> once always see a matching key and
// certificate, and plain path readers never miss a file.
func TestOSStoreSwapIsAtomic(t *testing.T) {
	st, dir, _ := osStore(t, func(c *Config) { c.KeepPrevious = 5 })
	ca := newCA(t)
	bundles := []bundle{ca.bundle(t, 0xa1), ca.bundle(t, 0xb2)}
	pairs := map[string]string{}
	for _, b := range bundles {
		p, err := certmaterial.ParseBundle(b.cert, nil, b.key, certmaterial.Options{Now: func() time.Time { return testNow }})
		if err != nil {
			t.Fatal(err)
		}
		pairs[string(p.CertPEM)] = string(b.key)
	}
	if res := st.Install(context.Background(), bundles[0].request(ca, "www")); res.State != store.DeliveryInstalled {
		t.Fatal(res)
	}
	var stop atomic.Bool
	var reads, mismatches, misses atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for !stop.Load() {
				if _, err := os.ReadFile(filepath.Join(dir, "live/www/fullchain.pem")); err != nil {
					misses.Add(1)
				}
				r, err := os.OpenRoot(filepath.Join(dir, "live/www"))
				if err != nil {
					misses.Add(1)
					continue
				}
				c, err1 := r.ReadFile(fileCert)
				k, err2 := r.ReadFile(fileKey)
				_ = r.Close()
				if err1 != nil || err2 != nil {
					continue // generation pruned while held (kept 5 back)
				}
				reads.Add(1)
				if pairs[string(c)] != string(k) {
					mismatches.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 40; i++ {
		if res := st.Install(context.Background(), bundles[(i+1)%2].request(ca, "www")); res.State != store.DeliveryInstalled {
			stop.Store(true)
			wg.Wait()
			t.Fatalf("install %d: %+v", i, res)
		}
	}
	stop.Store(true)
	wg.Wait()
	if mismatches.Load() != 0 || misses.Load() != 0 || reads.Load() == 0 {
		t.Fatalf("reads=%d mismatches=%d misses=%d", reads.Load(), mismatches.Load(), misses.Load())
	}
	gens, _ := st.generations("www")
	if len(gens) != 6 {
		t.Fatalf("generations kept = %d", len(gens))
	}
}

// TestOSStoreCrashRecovery: a generation staged but never switched to (crash
// before the rename) and a leftover temp link are removed at start.
func TestOSStoreCrashRecovery(t *testing.T) {
	st, dir, _ := osStore(t, nil)
	ca := newCA(t)
	b := ca.bundle(t, 0xa1)
	st.Install(context.Background(), b.request(ca, "www"))
	staged := filepath.Join(dir, "archive/www/29990101T000000Z-ff")
	if err := os.Mkdir(staged, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(staged, "privkey.pem"), b.key, 0o600)
	_ = os.Symlink("../archive/www/29990101T000000Z-ff", filepath.Join(dir, "live/.www.tmp"))
	if err := st.Recover(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(staged); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("staged generation kept")
	}
	if _, err := os.Lstat(filepath.Join(dir, "live/.www.tmp")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("temp link kept")
	}
	if _, err := os.ReadFile(filepath.Join(dir, "live/www/cert.pem")); err != nil {
		t.Fatal("live set damaged")
	}
}

// TestOSFSConfinement: the OS filesystem never follows a symlink out of the
// store and refuses symlinks where files are expected.
func TestOSFSConfinement(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "certs")
	outside := filepath.Join(base, "outside")
	_ = os.WriteFile(outside, []byte("secret"), 0o600)
	f, err := OpenFS(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Symlink(outside, "esc"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadFile("esc", 100); err == nil {
		t.Fatal("ReadFile followed a symlink")
	}
	if err := f.WriteFile("esc", []byte("x"), 0o644, os.Getuid(), os.Getgid()); err == nil {
		t.Fatal("WriteFile followed a symlink")
	}
	if b, _ := os.ReadFile(outside); string(b) != "secret" {
		t.Fatal("outside file modified")
	}
	if err := f.Chmod("esc", 0o777); err == nil {
		t.Fatal("Chmod of a symlink")
	}
	if err := f.Mkdir("d", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := f.Symlink("..", "d/up"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.ReadFile("d/up/../../outside", 100); err == nil {
		t.Fatal("path escaped the root")
	}
	if _, err := f.ReadFile("d", 100); err == nil {
		t.Fatal("ReadFile of a directory")
	}
	if err := f.WriteFile("d/big", bytes.Repeat([]byte("a"), 200), 0o640, os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("d/big", []byte("a"), 0o640, os.Getuid(), os.Getgid()); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("exclusive create: %v", err)
	}
	if _, err := f.ReadFile("d/big", 100); err == nil {
		t.Fatal("size bound")
	}
	if data, err := f.ReadFile("d/big", 1000); err != nil || len(data) != 200 {
		t.Fatalf("read: %v", err)
	}
	if _, err := f.ReadFile("missing", 10); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	fi, err := f.Lstat("d/big")
	if err != nil || fi.Mode.Perm() != 0o640 || fi.UID != os.Getuid() {
		t.Fatalf("lstat: %+v %v", fi, err)
	}
	if _, err := f.Lstat("nope"); err == nil {
		t.Fatal("lstat missing")
	}
	if err := f.Chmod("d/big", 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.Chmod("nope", 0o600); err == nil {
		t.Fatal("chmod missing")
	}
	if err := f.Lchown("d/big", os.Getuid(), os.Getgid()); err != nil {
		t.Fatal(err)
	}
	if err := f.Lchown("d/big", 0, 0); err == nil && os.Getuid() != 0 {
		t.Fatal("chown to root as non-root")
	}
	if tgt, err := f.Readlink("esc"); err != nil || tgt != outside {
		t.Fatalf("readlink %s %v", tgt, err)
	}
	if err := f.Rename("d/big", "d/big2"); err != nil {
		t.Fatal(err)
	}
	if names, err := f.ReadDir("d"); err != nil || len(names) != 2 {
		t.Fatalf("readdir %v %v", names, err)
	}
	if _, err := f.ReadDir("nope"); err == nil {
		t.Fatal("readdir missing")
	}
	if err := f.SyncDir("d"); err != nil {
		t.Fatal(err)
	}
	if err := f.SyncDir("nope"); err == nil {
		t.Fatal("sync missing")
	}
	if n, err := f.Free(); err != nil || n == 0 {
		t.Fatalf("free %d %v", n, err)
	}
	if err := f.RemoveAll("d"); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteFile("nodir/x", nil, 0o600, 0, 0); err == nil {
		t.Fatal("write into a missing directory")
	}
	if err := f.WriteFile("ro", []byte("x"), 0o600, -1, -1); err != nil {
		t.Fatal(err)
	}
	// Errors of the open-file steps.
	fh, _ := os.CreateTemp(base, "w")
	_ = fh.Close()
	if err := writeSync(fh, []byte("x"), 0o600, -1, -1); err == nil {
		t.Fatal("chmod of a closed file")
	}
	ro, _ := os.Open(filepath.Join(base, "outside"))
	defer ro.Close()
	if err := writeSync(ro, []byte("x"), 0o600, -1, -1); err == nil {
		t.Fatal("write to a read-only file")
	}
	if os.Getuid() != 0 {
		rw, _ := os.OpenFile(filepath.Join(base, "outside"), os.O_RDWR, 0)
		defer rw.Close()
		if err := writeSync(rw, []byte("x"), 0o600, 0, 0); err == nil {
			t.Fatal("chown to root as non-root")
		}
	}

	// The store directory itself must not be a symlink; creation errors surface.
	link := filepath.Join(base, "linked")
	_ = os.Symlink(dir, link)
	if _, err := OpenFS(link); !errors.Is(err, errUnsafe) {
		t.Fatalf("symlinked store: %v", err)
	}
	if _, err := OpenFS(filepath.Join(outside, "sub", "certs")); err == nil {
		t.Fatal("parent is a file")
	}
	if _, err := OpenFS(filepath.Join(outside, "certs")); err == nil {
		t.Fatal("create under a file")
	}
	if _, err := NewOS(Config{Dir: filepath.Join(outside, "certs")}, Deps{}); err == nil {
		t.Fatal("NewOS error")
	}
	if os.Getuid() != 0 {
		ro := filepath.Join(base, "ro")
		_ = os.Mkdir(ro, 0o500)
		if _, err := OpenFS(filepath.Join(ro, "certs")); err == nil {
			t.Fatal("create in a read-only directory")
		}
		noexec := filepath.Join(base, "noexec")
		_ = os.MkdirAll(filepath.Join(noexec, "certs"), 0o700)
		_ = os.Chmod(noexec, 0o600)
		defer os.Chmod(noexec, 0o700)
		if _, err := OpenFS(filepath.Join(noexec, "certs")); err == nil {
			t.Fatal("lstat in an unsearchable directory")
		}
		unreadable := filepath.Join(base, "unreadable")
		_ = os.Mkdir(unreadable, 0o000)
		defer os.Chmod(unreadable, 0o700)
		if _, err := OpenFS(unreadable); err == nil {
			t.Fatal("open of an unreadable directory")
		}
	}
	st, err := NewOS(Config{Dir: filepath.Join(base, "other")}, Deps{})
	if err != nil || st.acc == nil || st.hooks == nil || st.fs == nil {
		t.Fatalf("NewOS defaults: %v", err)
	}
	gone, _ := OpenFS(filepath.Join(base, "gone"))
	_ = os.RemoveAll(filepath.Join(base, "gone"))
	if _, err := gone.Free(); err == nil {
		t.Fatal("statfs of a removed directory")
	}
}

func TestOSAccounts(t *testing.T) {
	u, err := user.Current()
	if err != nil {
		t.Skip(err)
	}
	uid, err := OSAccounts{}.LookupUser(u.Username)
	if err != nil || uid != os.Getuid() {
		t.Fatalf("user: %d %v", uid, err)
	}
	g, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skip(err)
	}
	gid, err := OSAccounts{}.LookupGroup(g.Name)
	if err != nil || gid != os.Getgid() {
		t.Fatalf("group: %d %v", gid, err)
	}
	if _, err := (OSAccounts{}).LookupUser("no-such-user-033"); err == nil {
		t.Fatal("unknown user")
	}
	if _, err := (OSAccounts{}).LookupGroup("no-such-group-033"); err == nil {
		t.Fatal("unknown group")
	}
}

func TestInfoOfWithoutStat(t *testing.T) {
	fi := infoOf(fakeFileInfo{})
	if fi.UID != -1 || fi.GID != -1 {
		t.Fatalf("%+v", fi)
	}
	_ = syscall.Getuid
}

type fakeFileInfo struct{ fs.FileInfo }

func (fakeFileInfo) Mode() fs.FileMode { return 0 }
func (fakeFileInfo) Size() int64       { return 0 }
func (fakeFileInfo) Sys() any          { return nil }
