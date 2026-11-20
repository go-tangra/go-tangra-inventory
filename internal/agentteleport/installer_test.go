package agentteleport

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// TestJoin: the happy path installs the exact release from the origin,
// writes a 0600 config with the token and the unit, enables it and
// reports the node's host uuid.
func TestJoin(t *testing.T) {
	k := newKit(t)
	reported := false
	m := material()
	r := k.in.Join(context.Background(), m, func() bool { reported = true; return true })
	if r != (Result{State: StateJoined, NodeHostUUID: testUUID}) || !reported {
		t.Fatalf("result = %+v installing=%v", r, reported)
	}
	if strings.Trim(string(m.Token), "\x00") != "" {
		t.Fatal("token not wiped")
	}
	want := []string{testBase + "/teleport-v17.4.2-linux-amd64-bin.tar.gz.sha256", testBase + "/teleport-v17.4.2-linux-amd64-bin.tar.gz"}
	if strings.Join(k.cdnURLs(), ",") != strings.Join(want, ",") {
		t.Fatalf("urls = %v", k.http.urls)
	}

	if got := read(t, k.root, BinaryPath); got != "#!teleport-binary" {
		t.Fatalf("binary = %q", got)
	}
	fi, _ := os.Stat(filepath.Join(k.root, BinaryPath))
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("binary mode %v", fi.Mode())
	}
	for _, rel := range []string{"usr/local/bin/tctl", "teleport/README.md", "usr/local/bin/README.md"} {
		if _, err := os.Stat(filepath.Join(k.root, rel)); err == nil {
			t.Fatalf("%s extracted", rel)
		}
	}
	fi, _ = os.Stat(filepath.Join(k.root, ConfigPath))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("config mode %v", fi.Mode())
	}
	var c fileConfig
	raw := read(t, k.root, ConfigPath)
	if err := yaml.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	if c.Version != "v3" || c.Teleport.NodeName != "node-1.example.com" || c.Teleport.DataDir != "/var/lib/teleport" ||
		c.Teleport.JoinParams != (joinParams{Method: "token", TokenName: testToken}) || c.Teleport.ProxyServer != "teleport.example.com:443" ||
		len(c.Teleport.CAPin) != 1 || c.Teleport.CAPin[0] != testPin || c.AuthService.Enabled != "no" || c.ProxyService.Enabled != "no" ||
		c.SSHService.Enabled != "yes" || c.SSHService.Labels["tangra.io/os"] != "Debian 12" || len(c.SSHService.Labels) != 2 {
		t.Fatalf("config = %+v", c)
	}
	if c.Teleport.DiagAddr != DiagAddr || ReadyURL != "http://"+DiagAddr+"/readyz" {
		t.Fatalf("diag_addr = %q, ready url %q", c.Teleport.DiagAddr, ReadyURL)
	}
	if ours([]byte(raw)) {
		t.Fatal("a config with a token counts as a leftover")
	}
	unit := read(t, k.root, UnitPath)
	if !strings.Contains(unit, "ExecStart=/usr/local/bin/teleport start --config=/etc/teleport.yaml\n") || !strings.Contains(unit, "Restart=on-failure\n") {
		t.Fatalf("unit = %s", unit)
	}
	for _, c := range []string{"daemon-reload", "enable --now " + UnitName} {
		if !k.sys.called(c) {
			t.Fatalf("%s not run: %v", c, k.sys.calls)
		}
	}
	if ents, _ := os.ReadDir(filepath.Join(k.root, stagingRel)); len(ents) != 0 {
		t.Fatalf("staging left %v", ents)
	}
}

// TestJoinRetryAfterFailure: a config the agent left without a token does
// not block a new join.
func TestJoinRetryAfterFailure(t *testing.T) {
	k := newKit(t)
	write(t, k.root, ConfigPath, string(renderConfig("n", material(), "")))
	if r := k.join(t); r.State != StateJoined {
		t.Fatalf("result = %+v", r)
	}
}

func TestPreflight(t *testing.T) {
	ctx := context.Background()
	for name, setup := range map[string]func(k *kit){
		"arch":       func(k *kit) { k.in.arch = "386" },
		"no systemd": func(k *kit) { _ = os.RemoveAll(filepath.Join(k.root, "run")) },
		"systemd file": func(k *kit) {
			_ = os.RemoveAll(filepath.Join(k.root, "run"))
			write(t, k.root, "run/systemd/system", "")
		},
		"config":          func(k *kit) { write(t, k.root, ConfigPath, "version: v3\n") },
		"config with tok": func(k *kit) { write(t, k.root, ConfigPath, string(renderConfig("n", material(), "x"))) },
		"config unread":   func(k *kit) { mkdir(t, k.root, ConfigPath) },
		"packaged":        func(k *kit) { write(t, k.root, "lib/systemd/system/teleport.service", "") },
		"active":          func(k *kit) { k.sys.active = func() bool { return true } },
		"enabled":         func(k *kit) { delete(k.sys.fail, "is-enabled --quiet "+UnitName) },
	} {
		k := newKit(t)
		setup(k)
		want := ReasonAlreadyInstalled
		if name == "arch" || name == "no systemd" || name == "systemd file" {
			want = ReasonUnsupportedPlatform
		}
		if got := k.in.Preflight(ctx); got != want {
			t.Errorf("%s: preflight = %q", name, got)
		}
		r := k.join(t)
		if r.State != StateFailed || r.Reason != want || len(k.http.urls) != 0 {
			t.Errorf("%s: join = %+v (urls %v)", name, r, k.http.urls)
		}
	}
	if got := newKit(t).in.Preflight(ctx); got != "" {
		t.Fatalf("clean host: %q", got)
	}
}

func TestJoinRefusals(t *testing.T) {
	k := newKit(t)
	bad := material()
	bad.Version = "17.4"
	r := k.in.Join(context.Background(), bad, func() bool { t.Fatal("installing reported"); return true })
	if r.State != StateFailed || r.Reason != ReasonInvalidMaterial || r.Detail != "agentteleport: invalid join material: invalid teleport_version" {
		t.Fatalf("invalid = %+v", r)
	}
	k.in.hostname = func() (string, error) { return "", errors.New("no") }
	if r := k.join(t); r.Reason != reasonInstall {
		t.Fatalf("hostname err = %+v", r)
	}
	k.in.hostname = func() (string, error) { return "bad_name", nil }
	if r := k.join(t); r.Reason != reasonInstall || r.Detail != "hostname is not a valid node name" {
		t.Fatalf("hostname invalid = %+v", r)
	}
	k = newKit(t)
	if r := k.in.Join(context.Background(), material(), func() bool { return false }); !r.Aborted || len(k.http.urls) != 0 {
		t.Fatalf("aborted = %+v", r)
	}
}

func TestDownloadFailures(t *testing.T) {
	url := testBase + "/" + archiveName(testVersion, "amd64")
	archive := goodArchive(t)
	cases := map[string]struct {
		setup  func(k *kit)
		reason string
	}{
		"no sum":        {func(k *kit) { delete(k.http.files, url+".sha256") }, reasonDownload},
		"sum transport": {func(k *kit) { k.http.files[url+".sha256"] = resp{err: errors.New("dial tcp: refused")} }, reasonDownload},
		"sum read":      {func(k *kit) { k.http.files[url+".sha256"] = resp{status: 200, body: &errReader{}} }, reasonDownload},
		"sum empty":     {func(k *kit) { k.serve(url, archive, "") }, reasonDownload},
		"sum malformed": {func(k *kit) { k.serve(url, archive, "XYZ  file\n") }, reasonDownload},
		"no archive":    {func(k *kit) { delete(k.http.files, url) }, reasonDownload},
		"archive read":  {func(k *kit) { k.http.files[url] = resp{status: 200, body: &errReader{data: archive[:10]}} }, reasonDownload},
		"too large":     {func(k *kit) { k.in.maxArchive = int64(len(archive)) - 1 }, reasonDownload},
		"mismatch":      {func(k *kit) { k.serve(url, archive, sum([]byte("other"))) }, reasonChecksum},
		"staging":       {func(k *kit) { write(t, k.root, "var/lib/inventory-agent", "") }, reasonInstall},
		"bad url":       {func(k *kit) { k.in.base = "https://cdn.test\x7f" }, reasonDownload},
	}
	for name, c := range cases {
		k := newKit(t)
		c.setup(k)
		r := k.join(t)
		if r.State != StateFailed || r.Reason != c.reason {
			t.Errorf("%s: %+v", name, r)
		}
		if _, err := os.Stat(filepath.Join(k.root, BinaryPath)); err == nil {
			t.Errorf("%s: binary installed", name)
		}
		if _, err := os.Stat(filepath.Join(k.root, ConfigPath)); err == nil {
			t.Errorf("%s: config written", name)
		}
		if ents, _ := os.ReadDir(filepath.Join(k.root, stagingRel)); len(ents) != 0 {
			t.Errorf("%s: staging left %v", name, ents)
		}
	}
}

func TestInstallFailures(t *testing.T) {
	url := testBase + "/" + archiveName(testVersion, "amd64")
	for name, setup := range map[string]func(k *kit){
		"no binary": func(k *kit) {
			a := tarGz(t, entry{"teleport/tctl", tar.TypeReg, "x"})
			k.serve(url, a, sum(a))
		},
		"bin dir": func(k *kit) { write(t, k.root, "usr/local", "") },
		"bin busy": func(k *kit) {
			mkdir(t, k.root, BinaryPath)
			write(t, k.root, BinaryPath+"/x", "")
		},
	} {
		k := newKit(t)
		setup(k)
		if r := k.join(t); r.State != StateFailed || r.Reason != reasonInstall {
			t.Errorf("%s: %+v", name, r)
		}
		if ents, _ := os.ReadDir(filepath.Join(k.root, "usr/local/bin")); len(ents) > 1 {
			t.Errorf("%s: temporary binary left %v", name, ents)
		}
	}
}

// TestJoinFailuresRemoveToken: every failure after the config was written
// leaves a config without the token and the unit disabled.
func TestJoinFailuresRemoveToken(t *testing.T) {
	cases := map[string]struct {
		setup  func(k *kit)
		reason string
	}{
		"unit":          {func(k *kit) { write(t, k.root, "etc/systemd/system", "") }, reasonInstall},
		"daemon-reload": {func(k *kit) { k.sys.fail["daemon-reload"] = true }, reasonJoin},
		"enable":        {func(k *kit) { k.sys.fail["enable --now "+UnitName] = true }, reasonJoin},
		"never active":  {func(k *kit) { k.sys.active = func() bool { return false } }, reasonTimeout},
		"no uuid": {func(k *kit) {
			on := false
			k.sys.onCall = func(c string) { on = on || c == "enable --now "+UnitName }
			k.sys.active = func() bool { return on }
		}, reasonTimeout},
		"bad uuid": {func(k *kit) {
			write(t, k.root, HostUUIDRel, "nope")
			k.sys.onCall = nil
			k.sys.active = func() bool { return k.slept > 0 }
		}, reasonTimeout},
		// The host uuid is written before the node registers: joined needs
		// Teleport's own readiness (the node connected to the cluster).
		"not ready": {func(k *kit) {
			on := false
			k.sys.onCall = func(c string) {
				if c == "enable --now "+UnitName {
					on = true
					write(t, k.root, HostUUIDRel, testUUID+"\n")
					k.ready(503)
				}
			}
			k.sys.active = func() bool { return on }
		}, reasonTimeout},
		"probe unreachable": {func(k *kit) {
			on := false
			k.sys.onCall = func(c string) {
				if c == "enable --now "+UnitName {
					on = true
					write(t, k.root, HostUUIDRel, testUUID+"\n")
					k.http.files[ReadyURL] = resp{err: errors.New("connection refused")}
				}
			}
			k.sys.active = func() bool { return on }
		}, reasonTimeout},
		"cancelled": {func(k *kit) {
			k.sys.active = func() bool { return false }
			k.in.sleep = func(context.Context, time.Duration) error { return context.Canceled }
		}, reasonTimeout},
	}
	for name, c := range cases {
		k := newKit(t)
		c.setup(k)
		r := k.join(t)
		if r.State != StateFailed || r.Reason != c.reason {
			t.Errorf("%s: %+v", name, r)
			continue
		}
		noToken(t, k.root, r)
		if !ours([]byte(read(t, k.root, ConfigPath))) {
			t.Errorf("%s: config not reset", name)
		}
		if !k.sys.called("disable --now " + UnitName) {
			t.Errorf("%s: unit not disabled", name)
		}
	}
	// The timeout is the configured one: 15 minutes of 2 s polls.
	k := newKit(t)
	k.sys.active = func() bool { return false }
	k.join(t)
	if k.slept != int(15*time.Minute/pollInterval) {
		t.Fatalf("polls = %d", k.slept)
	}
	// The join waits for the unit to become active.
	k = newKit(t)
	n := 0
	k.sys.active = func() bool { n++; return n > 3 }
	if r := k.join(t); r.State != StateJoined {
		t.Fatalf("slow start = %+v", r)
	}
}

// TestJoinWaitsForReadiness: a node that is starting (readyz not 200) is
// not joined until Teleport reports ready.
func TestJoinWaitsForReadiness(t *testing.T) {
	k := newKit(t)
	n, on := 0, false
	k.sys.onCall = func(c string) {
		if c == "enable --now "+UnitName {
			on = true
			write(t, k.root, HostUUIDRel, testUUID+"\n")
			k.ready(503)
		}
	}
	k.sys.active = func() bool { return on }
	k.in.sleep = func(ctx context.Context, d time.Duration) error {
		k.slept++
		k.now = k.now.Add(d)
		if n++; n == 3 {
			k.ready(200)
		}
		return nil
	}
	if r := k.join(t); r != (Result{State: StateJoined, NodeHostUUID: testUUID}) || k.slept < 3 {
		t.Fatalf("result = %+v after %d polls", r, k.slept)
	}
}

// TestJoinStopsWhenNoLongerAccepted: while waiting for the join the agent
// asks the server whether the item is still active; once it is cancelled
// or failed, the agent removes the token, stops the unit and reports
// nothing more.
func TestJoinStopsWhenNoLongerAccepted(t *testing.T) {
	k := newKit(t)
	k.sys.active = func() bool { return false } // never joins on its own
	asks := 0
	r := k.in.Join(context.Background(), material(), func() bool { asks++; return asks == 1 })
	if !r.Aborted || r.State != "" {
		t.Fatalf("result = %+v", r)
	}
	if asks != 2 {
		t.Fatalf("asked %d times", asks)
	}
	if k.now.Sub(time.Unix(1_700_000_000, 0)) > 2*recheckInterval {
		t.Fatalf("stopped only after %s", k.now.Sub(time.Unix(1_700_000_000, 0)))
	}
	noToken(t, k.root, r)
	if !ours([]byte(read(t, k.root, ConfigPath))) || !k.sys.called("disable --now "+UnitName) {
		t.Fatalf("not rolled back: %v", k.sys.calls)
	}
}

// TestConfigWriteFailures: when the config cannot be written at all, or
// cannot be reset, the token does not stay on disk.
func TestConfigWriteFailures(t *testing.T) {
	// The config cannot be written: the step fails before the unit.
	k := newKit(t)
	k.http.onFile = func(u string) {
		if strings.HasSuffix(u, ".tar.gz") {
			write(t, k.root, ConfigPath+"/x", "")
		}
	}
	r := k.join(t)
	if r.Reason != reasonInstall || !strings.HasPrefix(r.Detail, "write /etc/teleport.yaml") || k.sys.called("enable --now "+UnitName) {
		t.Fatalf("config write = %+v", r)
	}
	noToken(t, k.root, r)
	// The reset fails but the file can be removed.
	k = newKit(t)
	k.sys.fail["enable --now "+UnitName] = true
	k.sys.onCall = func(call string) {
		if call == "disable --now "+UnitName {
			_ = os.Remove(filepath.Join(k.root, ConfigPath))
			mkdir(t, k.root, ConfigPath)
		}
	}
	r = k.join(t)
	if r.Reason != reasonJoin || strings.Contains(r.Detail, "could not remove") {
		t.Fatalf("reset = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(k.root, ConfigPath)); err == nil {
		t.Fatal("config left")
	}
	// Neither works: the report says so.
	k = newKit(t)
	k.sys.fail["enable --now "+UnitName] = true
	k.sys.onCall = func(call string) {
		if call == "disable --now "+UnitName {
			_ = os.Remove(filepath.Join(k.root, ConfigPath))
			write(t, k.root, ConfigPath+"/x", "")
		}
	}
	if r := k.join(t); r.Reason != reasonJoin || !strings.HasSuffix(r.Detail, "could not remove the join token from /etc/teleport.yaml") {
		t.Fatalf("no reset = %+v", r)
	}
}

func TestArm64AndOrigin(t *testing.T) {
	in := New(Deps{Arch: "arm64"}, time.Minute)
	if in.base != Origin || archiveName("1.2.3", in.arch) != "teleport-v1.2.3-linux-arm64-bin.tar.gz" || in.maxArchive != 512<<20 {
		t.Fatalf("installer = %+v", in)
	}
}

func TestSafeErr(t *testing.T) {
	if got := safeErr(errors.New("a\nb\x00ü")); got != "a?b???" {
		t.Fatalf("safeErr = %q", got)
	}
	if got := safeErr(errors.New(strings.Repeat("x", 300))); len(got) != maxDetail {
		t.Fatalf("len = %d", len(got))
	}
}
