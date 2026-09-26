package agentfacts

import (
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func checkPending(t *testing.T, ps []Pending) {
	t.Helper()
	for _, p := range ps {
		if p.Name == "" || p.Available == "" || len(p.Name) > maxPkgField || len(p.Available) > maxPkgField || len(p.Installed) > maxPkgField {
			t.Fatalf("bad entry %+v", p)
		}
	}
	out, _, _ := MergePending(nil, ps, nil)
	if len(out) > store.MaxPendingUpdates {
		t.Fatalf("merge bound: %d", len(out))
	}
}

func FuzzAptSimulate(f *testing.F) {
	f.Add("Inst libssl3 [3.0.2] (3.0.3 Ubuntu:22.04/jammy-security [amd64])\n")
	f.Add("Inst x [] ()\nInst")
	f.Fuzz(func(t *testing.T, s string) { checkPending(t, ParseAptSimulate(s)) })
}

func FuzzDnfOutput(f *testing.F) {
	f.Add("openssl.x86_64  1:3.0.7-25.el9  baseos\nlong.noarch\n  1.0-1 repo\n", "RHSA-1 Important/Sec. openssl-1:3.0.7-25.el9.x86_64\n")
	f.Fuzz(func(t *testing.T, check, sec string) {
		checkPending(t, ParseDnfCheckUpdate(check))
		for n := range ParseDnfSecurity(sec) {
			if n == "" || len(n) > maxPkgField {
				t.Fatalf("bad security name %q", n)
			}
		}
	})
}

func FuzzApkPacmanOutput(f *testing.F) {
	f.Add("busybox-1.36.1-r5 x86_64 {busybox} (GPL) [upgradable from: busybox-1.36.1-r4]\n", "linux 6.5-1 -> 6.6-1\n")
	f.Fuzz(func(t *testing.T, apk, pacman string) {
		checkPending(t, ParseApkUpgradable(apk))
		checkPending(t, ParseCheckupdates(pacman))
	})
}
