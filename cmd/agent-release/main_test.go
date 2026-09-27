package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

var artifactNames = []string{
	"tangra-inventory-agent_4.5.0_amd64.deb", "tangra-inventory-agent_4.5.0_arm64.deb",
	"tangra-inventory-agent-4.5.0-1.x86_64.rpm", "tangra-inventory-agent-4.5.0-1.aarch64.rpm",
	"inventory-agent-linux-amd64", "inventory-agent-linux-arm64",
	"inventory-agent-windows-amd64.exe", "inventory-agent-windows-arm64.exe",
}

func releaseDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range artifactNames {
		if err := os.WriteFile(filepath.Join(dir, n), []byte("artifact "+n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func runCLI(env map[string]string, args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := run(args, func(k string) string { return env[k] }, &out, &errb)
	return code, out.String(), errb.String()
}

func TestKeygenSignVerifyRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	priv := filepath.Join(tmp, "release.key")
	code, out, errOut := runCLI(nil, "keygen", "-out-private", priv, "-key-id", "tangra-test")
	if code != 0 {
		t.Fatalf("keygen = %d %s", code, errOut)
	}
	keyring := strings.TrimSpace(out)
	if !strings.HasPrefix(keyring, "tangra-test:") {
		t.Fatalf("keygen output = %q", out)
	}
	if st, err := os.Stat(priv); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("private key file = %v %v", st, err)
	}
	if code, _, _ := runCLI(nil, "keygen", "-out-private", priv, "-key-id", "tangra-test"); code == 0 {
		t.Fatal("keygen must not overwrite an existing key")
	}
	seed, _ := os.ReadFile(priv)
	env := map[string]string{"AGENT_RELEASE_SIGNING_KEY": strings.TrimSpace(string(seed))}

	dir := releaseDir(t)
	if code, _, errOut := runCLI(env, "sign", "-key-env", "AGENT_RELEASE_SIGNING_KEY", "-key-id", "tangra-test", "-version", "4.5.0", "-dir", dir); code != 0 {
		t.Fatalf("sign = %d %s", code, errOut)
	}
	manifest, err := os.ReadFile(filepath.Join(dir, agentrelease.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	m, err := agentrelease.ParseManifest(manifest)
	if err != nil || m.Version != "4.5.0" || len(m.Artifacts) != 8 || m.KeyID != "tangra-test" {
		t.Fatalf("manifest = %+v %v", m, err)
	}
	if a, err := m.Artifact(agentrelease.Platform{OS: "linux", Arch: "arm64", InstallType: "rpm"}); err != nil || a.File != "tangra-inventory-agent-4.5.0-1.aarch64.rpm" {
		t.Fatalf("rpm arm64 = %+v %v", a, err)
	}
	if code, out, errOut := runCLI(nil, "verify", "-dir", dir, "-version", "4.5.0", "-keyring", keyring); code != 0 || !strings.Contains(out, "4.5.0") {
		t.Fatalf("verify = %d %s %s", code, out, errOut)
	}

	// Verification fails after one artifact byte changes ...
	art := filepath.Join(dir, "inventory-agent-linux-amd64")
	b, _ := os.ReadFile(art)
	b[0] ^= 1
	_ = os.WriteFile(art, b, 0o644)
	if code, _, _ := runCLI(nil, "verify", "-dir", dir, "-keyring", keyring); code == 0 {
		t.Fatal("verify accepted a modified artifact")
	}
	b[0] ^= 1
	_ = os.WriteFile(art, b, 0o644)
	// ... or the manifest changes ...
	_ = os.WriteFile(filepath.Join(dir, agentrelease.ManifestFile), bytes.Replace(manifest, []byte("4.5.0"), []byte("4.5.1"), 1), 0o644)
	if code, _, _ := runCLI(nil, "verify", "-dir", dir, "-keyring", keyring); code == 0 {
		t.Fatal("verify accepted a modified manifest")
	}
	_ = os.WriteFile(filepath.Join(dir, agentrelease.ManifestFile), manifest, 0o644)
	// ... or the version differs from the expected one, or the key is unknown.
	if code, _, _ := runCLI(nil, "verify", "-dir", dir, "-version", "4.6.0", "-keyring", keyring); code == 0 {
		t.Fatal("verify accepted another version")
	}
	if code, _, _ := runCLI(nil, "verify", "-dir", dir); code == 0 {
		t.Fatal("verify without a known key (empty compiled keyring) must fail")
	}
	_ = os.Remove(art)
	if code, _, _ := runCLI(nil, "verify", "-dir", dir, "-keyring", keyring); code == 0 {
		t.Fatal("verify accepted a missing artifact")
	}
}

func TestSignKeyOnlyFromEnvironment(t *testing.T) {
	dir := releaseDir(t)
	// A key value passed where the variable name belongs is refused.
	fake := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	code, _, errOut := runCLI(map[string]string{fake: "x"}, "sign", "-key-env", fake, "-key-id", "k", "-version", "4.5.0", "-dir", dir)
	if code == 0 || !strings.Contains(errOut, "environment variable name") {
		t.Fatalf("flag value accepted: %d %s", code, errOut)
	}
	// Missing secret: a clear error (the protected release environment is
	// the only place the key exists).
	code, _, errOut = runCLI(nil, "sign", "-key-env", "AGENT_RELEASE_SIGNING_KEY", "-key-id", "k", "-version", "4.5.0", "-dir", dir)
	if code == 0 || !strings.Contains(errOut, "AGENT_RELEASE_SIGNING_KEY is not set") {
		t.Fatalf("missing secret = %d %s", code, errOut)
	}
	code, _, _ = runCLI(map[string]string{"K": "not-base64!"}, "sign", "-key-env", "K", "-key-id", "k", "-version", "4.5.0", "-dir", dir)
	if code == 0 {
		t.Fatal("broken key accepted")
	}
}

func TestSignRequiresEveryArtifact(t *testing.T) {
	tmp := t.TempDir()
	priv := filepath.Join(tmp, "k")
	if code, _, _ := runCLI(nil, "keygen", "-out-private", priv, "-key-id", "k"); code != 0 {
		t.Fatal("keygen")
	}
	seed, _ := os.ReadFile(priv)
	env := map[string]string{"K": strings.TrimSpace(string(seed))}
	dir := releaseDir(t)
	_ = os.Remove(filepath.Join(dir, "inventory-agent-windows-arm64.exe"))
	if code, _, errOut := runCLI(env, "sign", "-key-env", "K", "-key-id", "k", "-version", "4.5.0", "-dir", dir); code == 0 || !strings.Contains(errOut, "windows/arm64") {
		t.Fatalf("missing artifact = %d %s", code, errOut)
	}
	dir = releaseDir(t)
	_ = os.WriteFile(filepath.Join(dir, "other_4.5.0_amd64.deb"), []byte("x"), 0o644)
	if code, _, errOut := runCLI(env, "sign", "-key-env", "K", "-key-id", "k", "-version", "4.5.0", "-dir", dir); code == 0 || !strings.Contains(errOut, "ambiguous") {
		t.Fatalf("ambiguous artifact = %d %s", code, errOut)
	}
	if code, _, _ := runCLI(env, "sign", "-key-env", "K", "-key-id", "k", "-version", "v4.5", "-dir", dir); code == 0 {
		t.Fatal("bad version accepted")
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"keygen"}, {"keygen", "-key-id", "Bad Id", "-out-private", "x"}, {"sign"}, {"verify"}, {"keygen", "-bogus"}} {
		if code, _, _ := runCLI(nil, args...); code != 2 && code != 1 {
			t.Errorf("%v = %d", args, code)
		}
	}
}
