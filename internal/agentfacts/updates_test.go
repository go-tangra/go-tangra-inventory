package agentfacts

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func testdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/updates/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseAptSimulate(t *testing.T) {
	got := ParseAptSimulate(testdata(t, "apt-simulate.txt"))
	want := []Pending{
		{Name: "libssl3", Installed: "3.0.2-0ubuntu1.10", Available: "3.0.2-0ubuntu1.12", Security: true},
		{Name: "openssl", Installed: "3.0.2-0ubuntu1.10", Available: "3.0.2-0ubuntu1.12", Security: true},
		{Name: "curl", Installed: "7.81.0-1ubuntu1.13", Available: "7.81.0-1ubuntu1.14"},
		{Name: "tzdata", Installed: "2023c-0ubuntu0.22.04.2", Available: "2024a-0ubuntu0.22.04", Security: true},
		{Name: "console-setup", Installed: "1.226ubuntu1", Available: "1.226ubuntu1.1"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("apt:\n got %+v\nwant %+v", got, want)
	}
	if len(ParseAptSimulate("Inst broken [1.0\nInst x [1] (\n")) != 0 {
		t.Fatal("malformed Inst lines parsed")
	}
}

func TestParseDnf(t *testing.T) {
	got := ParseDnfCheckUpdate(testdata(t, "dnf-check-update.txt"))
	want := []Pending{
		{Name: "kernel", Available: "5.14.0-362.24.1.el9_3"},
		{Name: "openssl", Available: "3.0.7-25.el9_3"},
		{Name: "openssl-libs", Available: "3.0.7-25.el9_3"},
		{Name: "a-very-long-package-name-that-wraps", Available: "2.4.1-1.el9"},
		{Name: "glibc", Available: "2.34-83.el9_3.12"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("dnf:\n got %+v\nwant %+v", got, want)
	}
	sec := ParseDnfSecurity(testdata(t, "dnf-updateinfo-security.txt"))
	if len(sec) != 3 || !sec["openssl"] || !sec["openssl-libs"] || !sec["kernel"] || sec["glibc"] {
		t.Fatalf("security = %v", sec)
	}
	if n := nevraName("noarch-only"); n != "" {
		t.Fatalf("nevra = %q", n)
	}
}

func TestParseApkAndPacman(t *testing.T) {
	got := ParseApkUpgradable(testdata(t, "apk-list.txt"))
	want := []Pending{
		{Name: "busybox", Installed: "1.36.1-r4", Available: "1.36.1-r5"},
		{Name: "libcrypto3", Installed: "3.1.4-r1", Available: "3.1.4-r5"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("apk:\n got %+v\nwant %+v", got, want)
	}
	got = ParseCheckupdates(testdata(t, "checkupdates.txt"))
	want = []Pending{
		{Name: "linux", Installed: "6.5.9.arch2-1", Available: "6.6.1.arch1-1"},
		{Name: "openssl", Installed: "3.1.4-1", Available: "3.2.0-1"},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("pacman:\n got %+v\nwant %+v", got, want)
	}
	if len(ParseApkUpgradable("x [upgradable from: ]\n-1 y [upgradable from: z]\n")) != 0 {
		t.Fatal("malformed apk lines parsed")
	}
}

func TestRebootRequired(t *testing.T) {
	ksta := testdata(t, "needrestart.txt")
	cases := []struct {
		file     bool
		needsRst int
		nr       string
		want     string
	}{
		{true, -1, "", store.TriTrue},
		{false, 1, "", store.TriTrue},
		{false, 0, "", store.TriFalse},
		{false, -1, ksta, store.TriTrue},
		{false, -1, "NEEDRESTART-KSTA: 1\n", store.TriFalse},
		{false, -1, "NEEDRESTART-KSTA: 0\n", store.TriUnknown},
		{false, -1, "NEEDRESTART-KSTA: x\n", store.TriUnknown},
		{false, 2, "", store.TriUnknown},
		{false, -1, "", store.TriUnknown},
	}
	for i, c := range cases {
		if got := RebootRequired(c.file, c.needsRst, c.nr); got != c.want {
			t.Errorf("case %d: %q want %q", i, got, c.want)
		}
	}
}

func TestAutomaticUpdates(t *testing.T) {
	cfg := testdata(t, "apt-config.txt")
	if AutomaticUpdates("apt", cfg, map[string]bool{"apt-daily-upgrade.timer": true}) != store.TriTrue {
		t.Fatal("apt enabled")
	}
	if AutomaticUpdates("apt", cfg, nil) != store.TriFalse || AutomaticUpdates("apt", `APT::Periodic::Unattended-Upgrade "0";`, map[string]bool{"apt-daily-upgrade.timer": true}) != store.TriFalse {
		t.Fatal("apt disabled")
	}
	if AutomaticUpdates("dnf", "", map[string]bool{"dnf-automatic-install.timer": true}) != store.TriTrue ||
		AutomaticUpdates("dnf", "", nil) != store.TriFalse ||
		AutomaticUpdates("yum", "", map[string]bool{"yum-cron.service": true}) != store.TriTrue ||
		AutomaticUpdates("apk", "", nil) != store.TriFalse ||
		AutomaticUpdates("", "", nil) != store.TriUnknown {
		t.Fatal("automatic update rules")
	}
}

func TestMergePending(t *testing.T) {
	programs := []store.Program{{Name: "openssl", Version: "3.0.1"}, {Name: "curl", Version: "8.0"}, {Name: "vim", Version: "9"}}
	pending := []Pending{
		{Name: "openssl", Available: "3.0.2", Security: false},
		{Name: "curl", Installed: "8.0", Available: "8.1"},
		{Name: "libnew", Installed: "0.9", Available: "1.0"}, // not in the installed list → added
	}
	out, st, dropped := MergePending(programs, pending, map[string]bool{"openssl": true})
	if dropped != 0 || len(out) != 4 {
		t.Fatalf("programs = %+v", out)
	}
	if out[0].AvailableVersion != "3.0.2" || !out[0].SecurityUpdate || out[1].AvailableVersion != "8.1" || out[1].SecurityUpdate ||
		out[2].AvailableVersion != "" || out[3] != (store.Program{Name: "libnew", Version: "0.9", AvailableVersion: "1.0"}) {
		t.Fatalf("merged = %+v", out)
	}
	if st.PendingCount != 3 || st.SecurityCount != 1 {
		t.Fatalf("counts = %+v", st)
	}
	// Bound: 5001 pending → 5000 + dropped 1.
	var many []Pending
	for i := 0; i < store.MaxPendingUpdates+1; i++ {
		many = append(many, Pending{Name: fmt.Sprint("p", i), Available: "2"})
	}
	out, st, dropped = MergePending(nil, many, nil)
	if len(out) != store.MaxPendingUpdates || dropped != 1 || st.PendingCount != store.MaxPendingUpdates+1 {
		t.Fatalf("bound: %d programs, dropped %d, %+v", len(out), dropped, st)
	}
}

func TestUpdateStatus(t *testing.T) {
	if UpdateStatus("", nil, 0) != store.UpdateUnsupported || UpdateStatus("apt", fmt.Errorf("x"), 0) != store.UpdateError ||
		UpdateStatus("apt", nil, 0) != store.UpdateUpToDate || UpdateStatus("apt", nil, 3) != store.UpdateAvailable {
		t.Fatal("status rules")
	}
	if !strings.Contains(fmt.Sprint(Managers), "pacman") {
		t.Fatal("managers")
	}
}
