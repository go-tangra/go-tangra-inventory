package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestReleaseCheck builds the agent the way the release does and runs
// scripts/check-release-binary.sh on it: a release keyring and version pass;
// a development key, a missing key or a describe version fail.
func TestReleaseCheck(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the agent")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key := base64.StdEncoding.EncodeToString(pub)
	dir := t.TempDir()
	build := func(name, version, keys string) string {
		t.Helper()
		out := filepath.Join(dir, name)
		ld := "-s -w -X main.version=" + version
		if keys != "" {
			ld += " -X github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease.productionKeys=" + keys
		}
		// -buildvcs=false: on a tagged commit the VCS stamp would put the
		// tag's version into every test binary.
		cmd := exec.Command("go", "build", "-trimpath", "-buildvcs=false", "-ldflags", ld, "-o", out, "../inventory-agent")
		cmd.Env = append(cmd.Environ(), "CGO_ENABLED=0")
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v\n%s", name, err, b)
		}
		return out
	}
	release := "tangra-agent-2026:" + key
	check := func(bin, keys, version string) (bool, string) {
		b, err := exec.Command("bash", "../../scripts/check-release-binary.sh", "-keys", keys, "-version", version, bin).CombinedOutput()
		return err == nil, string(b)
	}
	if ok, out := check(build("release", "9.8.7", release), release, "9.8.7"); !ok {
		t.Fatalf("release build refused: %s", out)
	}
	for name, c := range map[string][2]string{
		"devkey":   {"9.8.7", "dev-local:" + key},
		"rotation": {"9.8.7", release + ",dev-local:" + key},
		"nokey":    {"9.8.7", ""},
		"describe": {"9.8.6~3-gabc123", release},
	} {
		if ok, out := check(build(name, c[0], c[1]), release, "9.8.7"); ok || !strings.Contains(out, "release-check") {
			t.Errorf("%s build accepted: %s", name, out)
		}
	}
	// A dev keyring or a non-release version as the expectation is refused too.
	if ok, _ := check(build("release2", "9.8.7", release), "dev-local:"+key, "9.8.7"); ok {
		t.Error("dev expectation accepted")
	}
	if ok, _ := check(build("release3", "9.8.7", release), release, "9.8.7~1-gabc"); ok {
		t.Error("describe expectation accepted")
	}
}
