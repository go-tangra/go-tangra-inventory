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
		cmd := exec.Command("go", "build", "-trimpath", "-ldflags", ld, "-o", out, "../inventory-agent")
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
	if ok, out := check(build("release", "4.5.0", release), release, "4.5.0"); !ok {
		t.Fatalf("release build refused: %s", out)
	}
	for name, c := range map[string][2]string{
		"devkey":   {"4.5.0", "dev-local:" + key},
		"rotation": {"4.5.0", release + ",dev-local:" + key},
		"nokey":    {"4.5.0", ""},
		"describe": {"4.4.0~3-gabc123", release},
	} {
		if ok, out := check(build(name, c[0], c[1]), release, "4.5.0"); ok || !strings.Contains(out, "release-check") {
			t.Errorf("%s build accepted: %s", name, out)
		}
	}
	// A dev keyring or a non-release version as the expectation is refused too.
	if ok, _ := check(build("release2", "4.5.0", release), "dev-local:"+key, "4.5.0"); ok {
		t.Error("dev expectation accepted")
	}
	if ok, _ := check(build("release3", "4.5.0", release), release, "4.5.0~1-gabc"); ok {
		t.Error("describe expectation accepted")
	}
}
