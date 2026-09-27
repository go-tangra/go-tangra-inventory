package agentrelease

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Tests use ephemeral key pairs generated per run: no private key is ever
// committed (the production public keys are injected at build time).

func testKey(t testing.TB) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func sampleManifest(version string) Manifest {
	arts := []Artifact{}
	for _, p := range []struct{ os, arch, it, file string }{
		{"linux", "amd64", "deb", "tangra-inventory-agent_" + version + "_amd64.deb"},
		{"linux", "amd64", "rpm", "tangra-inventory-agent-" + version + "-1.x86_64.rpm"},
		{"linux", "amd64", "binary", "inventory-agent-linux-amd64"},
		{"linux", "arm64", "deb", "tangra-inventory-agent_" + version + "_arm64.deb"},
		{"linux", "arm64", "rpm", "tangra-inventory-agent-" + version + "-1.aarch64.rpm"},
		{"linux", "arm64", "binary", "inventory-agent-linux-arm64"},
		{"windows", "amd64", "binary", "inventory-agent-windows-amd64.exe"},
		{"windows", "arm64", "binary", "inventory-agent-windows-arm64.exe"},
	} {
		arts = append(arts, Artifact{OS: p.os, Arch: p.arch, InstallType: p.it, File: p.file, Size: 1234, SHA256: sum([]byte(p.file))})
	}
	return Manifest{Schema: 1, Version: version, CreatedAt: time.Date(2026, 10, 2, 10, 0, 0, 0, time.UTC), KeyID: "test-key", Artifacts: arts}
}

func encodeManifest(t testing.TB, m Manifest) []byte {
	t.Helper()
	return m.Encode()
}

func TestParseManifestValid(t *testing.T) {
	m := sampleManifest("4.4.0")
	b := encodeManifest(t, m)
	got, err := ParseManifest(b)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != "4.4.0" || got.KeyID != "test-key" || len(got.Artifacts) != 8 || !got.CreatedAt.Equal(m.CreatedAt) {
		t.Fatalf("manifest = %+v", got)
	}
	// Pre-release versions are releases too.
	if _, err := ParseManifest(encodeManifest(t, sampleManifest("4.5.0-rc.1"))); err != nil {
		t.Fatalf("pre-release: %v", err)
	}
}

func TestParseManifestStrict(t *testing.T) {
	base := func() map[string]any {
		var m map[string]any
		_ = json.Unmarshal(encodeManifest(t, sampleManifest("4.4.0")), &m)
		return m
	}
	art := func(m map[string]any, i int) map[string]any { return m["artifacts"].([]any)[i].(map[string]any) }
	cases := map[string]func(m map[string]any){
		"unknown field":      func(m map[string]any) { m["extra"] = 1 },
		"unknown art field":  func(m map[string]any) { art(m, 0)["url"] = "https://example.org" },
		"schema 2":           func(m map[string]any) { m["schema"] = 2 },
		"bad version":        func(m map[string]any) { m["version"] = "v4.4" },
		"describe version":   func(m map[string]any) { m["version"] = "4.3.1~11-gabc" },
		"bad key id":         func(m map[string]any) { m["key_id"] = "Key ID" },
		"no artifacts":       func(m map[string]any) { m["artifacts"] = []any{} },
		"duplicate platform": func(m map[string]any) { a := art(m, 1); a["install_type"] = "deb" },
		"bad file name":      func(m map[string]any) { art(m, 0)["file"] = "../evil.deb" },
		"dot file":           func(m map[string]any) { art(m, 0)["file"] = ".." },
		"slash file":         func(m map[string]any) { art(m, 0)["file"] = "a/b.deb" },
		"size 0":             func(m map[string]any) { art(m, 0)["size"] = 0 },
		"size too big":       func(m map[string]any) { art(m, 0)["size"] = MaxArtifactSize + 1 },
		"uppercase sha":      func(m map[string]any) { art(m, 0)["sha256"] = strings.ToUpper(sum([]byte("x"))) },
		"short sha":          func(m map[string]any) { art(m, 0)["sha256"] = "abc" },
		"bad os":             func(m map[string]any) { art(m, 0)["os"] = "darwin" },
		"bad arch":           func(m map[string]any) { art(m, 0)["arch"] = "386" },
		"bad install type":   func(m map[string]any) { art(m, 0)["install_type"] = "snap" },
		"windows package":    func(m map[string]any) { a := art(m, 6); a["install_type"] = "deb" },
		"missing created":    func(m map[string]any) { delete(m, "created_at") },
		"bad created":        func(m map[string]any) { m["created_at"] = "yesterday" },
	}
	for name, mut := range cases {
		m := base()
		mut(m)
		b, _ := json.Marshal(m)
		if _, err := ParseManifest(b); !errors.Is(err, ErrManifest) {
			t.Errorf("%s: err = %v, want ErrManifest", name, err)
		}
	}
	m := base()
	var arts []any
	for i := 0; i < MaxArtifacts+1; i++ {
		a := map[string]any{}
		for k, v := range art(m, 0) {
			a[k] = v
		}
		a["file"] = fmt.Sprintf("f%d", i)
		a["arch"] = []string{"amd64", "arm64"}[i%2]
		arts = append(arts, a)
	}
	m["artifacts"] = arts
	b, _ := json.Marshal(m)
	if _, err := ParseManifest(b); !errors.Is(err, ErrManifest) {
		t.Errorf("> 16 artifacts: %v", err)
	}
	if _, err := ParseManifest(bytes.Repeat([]byte(" "), MaxManifestBytes+1)); !errors.Is(err, ErrManifest) {
		t.Errorf("> 64 KiB: %v", err)
	}
	for _, bad := range []string{``, `nope`, `{}` + `{}`, `[]`} {
		if _, err := ParseManifest([]byte(bad)); !errors.Is(err, ErrManifest) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestKeyringParse(t *testing.T) {
	pub, _ := testKey(t)
	pub2, _ := testKey(t)
	s := "test-key:" + base64.StdEncoding.EncodeToString(pub) + ", next-key:" + base64.StdEncoding.EncodeToString(pub2)
	k, err := ParseKeyring(s)
	if err != nil || len(k) != 2 || !bytes.Equal(k["test-key"], pub) || !bytes.Equal(k["next-key"], pub2) {
		t.Fatalf("keyring = %v %v", k, err)
	}
	if k, err := ParseKeyring(""); err != nil || len(k) != 0 {
		t.Fatalf("empty = %v %v", k, err)
	}
	for _, bad := range []string{"nocolon", "Bad Id:" + base64.StdEncoding.EncodeToString(pub), "k:!!!", "k:" + base64.StdEncoding.EncodeToString([]byte("short")),
		"k:" + base64.StdEncoding.EncodeToString(pub) + ",k:" + base64.StdEncoding.EncodeToString(pub2)} {
		if _, err := ParseKeyring(bad); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if got := k.KeyIDs(); len(got) != 2 || got[0] != "next-key" || got[1] != "test-key" {
		t.Fatalf("key ids = %v", got)
	}
}

func TestCompiledKeyring(t *testing.T) {
	old := productionKeys
	t.Cleanup(func() { productionKeys = old })
	productionKeys = ""
	if k, err := Compiled(); err != nil || len(k) != 0 {
		t.Fatalf("no injected key = %v %v", k, err)
	}
	pub, _ := testKey(t)
	productionKeys = "tangra-agent-2026:" + base64.StdEncoding.EncodeToString(pub)
	if k, err := Compiled(); err != nil || !bytes.Equal(k["tangra-agent-2026"], pub) {
		t.Fatalf("injected = %v %v", k, err)
	}
	productionKeys = "broken"
	if _, err := Compiled(); err == nil {
		t.Fatal("broken injected keyring accepted")
	}
}

func TestVerify(t *testing.T) {
	pub, priv := testKey(t)
	otherPub, otherPriv := testKey(t)
	k := Keyring{"test-key": pub, "other-key": otherPub}
	m := sampleManifest("4.4.0")
	b := encodeManifest(t, m)
	sig := ed25519.Sign(priv, b)

	got, err := k.Verify(b, sig, "test-key")
	if err != nil || got.Version != "4.4.0" {
		t.Fatalf("valid = %v %v", got, err)
	}
	flipped := append([]byte(nil), b...)
	flipped[10] ^= 1
	if _, err := k.Verify(flipped, sig, "test-key"); !errors.Is(err, ErrSignature) {
		t.Errorf("flipped manifest byte: %v", err)
	}
	badSig := append([]byte(nil), sig...)
	badSig[0] ^= 1
	if _, err := k.Verify(b, badSig, "test-key"); !errors.Is(err, ErrSignature) {
		t.Errorf("flipped signature byte: %v", err)
	}
	if _, err := k.Verify(b, sig[:63], "test-key"); !errors.Is(err, ErrSignature) {
		t.Errorf("truncated signature: %v", err)
	}
	if _, err := k.Verify(b, ed25519.Sign(otherPriv, b), "test-key"); !errors.Is(err, ErrSignature) {
		t.Errorf("signed by another key: %v", err)
	}
	if _, err := k.Verify(b, sig, "nope"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("unknown key id: %v", err)
	}
	if _, err := (Keyring{}).Verify(b, sig, "test-key"); !errors.Is(err, ErrUnknownKey) {
		t.Errorf("empty keyring must refuse: %v", err)
	}
	// The manifest names the key that signed it.
	m.KeyID = "other-key"
	b2 := encodeManifest(t, m)
	if _, err := k.Verify(b2, ed25519.Sign(priv, b2), "test-key"); !errors.Is(err, ErrManifest) {
		t.Errorf("key id mismatch: %v", err)
	}
	// A valid signature over an invalid manifest is still refused.
	junk := []byte(`{"schema":1}`)
	if _, err := k.Verify(junk, ed25519.Sign(priv, junk), "test-key"); !errors.Is(err, ErrManifest) {
		t.Errorf("signed junk: %v", err)
	}
}

func TestSignatureFile(t *testing.T) {
	_, priv := testKey(t)
	sig := ed25519.Sign(priv, []byte("m"))
	enc := EncodeSignature(sig)
	got, err := DecodeSignature([]byte(enc + "\n"))
	if err != nil || !bytes.Equal(got, sig) {
		t.Fatalf("round trip = %v", err)
	}
	for _, bad := range []string{"", "!!!", base64.StdEncoding.EncodeToString([]byte("short"))} {
		if _, err := DecodeSignature([]byte(bad)); !errors.Is(err, ErrSignature) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestPlatformSelection(t *testing.T) {
	m := sampleManifest("4.4.0")
	a, err := m.Artifact(Platform{OS: "linux", Arch: "arm64", InstallType: "rpm"})
	if err != nil || a.File != "tangra-inventory-agent-4.4.0-1.aarch64.rpm" {
		t.Fatalf("artifact = %+v %v", a, err)
	}
	m.Artifacts = m.Artifacts[:1]
	if _, err := m.Artifact(Platform{OS: "windows", Arch: "amd64", InstallType: "binary"}); !errors.Is(err, ErrPlatform) {
		t.Fatalf("missing entry: %v", err)
	}
	for _, p := range []Platform{{"linux", "amd64", "deb"}, {"windows", "arm64", "binary"}} {
		if !p.Valid() {
			t.Errorf("%+v must be valid", p)
		}
	}
	for _, p := range []Platform{{}, {"linux", "amd64", "snap"}, {"windows", "amd64", "deb"}, {"darwin", "arm64", "binary"}, {"linux", "386", "binary"}} {
		if p.Valid() {
			t.Errorf("%+v must be invalid", p)
		}
	}
	if (Platform{"linux", "amd64", "deb"}).String() != "linux/amd64 deb" {
		t.Error("platform string")
	}
}

func TestArtifactVerifier(t *testing.T) {
	data := bytes.Repeat([]byte("agent"), 1000)
	a := Artifact{Size: int64(len(data)), SHA256: sum(data)}
	v := NewArtifactVerifier(a)
	if _, err := v.Write(data[:100]); err != nil {
		t.Fatal(err)
	}
	if _, err := v.Write(data[100:]); err != nil {
		t.Fatal(err)
	}
	if err := v.Finish(); err != nil {
		t.Fatalf("valid: %v", err)
	}
	v = NewArtifactVerifier(a)
	_, _ = v.Write(data[:len(data)-1])
	if err := v.Finish(); !errors.Is(err, ErrSize) {
		t.Errorf("truncated: %v", err)
	}
	v = NewArtifactVerifier(a)
	if _, err := v.Write(append(append([]byte(nil), data...), 'x')); !errors.Is(err, ErrSize) {
		t.Errorf("oversized write must fail early: %v", err)
	}
	tampered := append([]byte(nil), data...)
	tampered[3] ^= 1
	v = NewArtifactVerifier(a)
	_, _ = v.Write(tampered)
	if err := v.Finish(); !errors.Is(err, ErrChecksum) {
		t.Errorf("tampered: %v", err)
	}
	if v.Written() != int64(len(data)) {
		t.Errorf("written = %d", v.Written())
	}
}

func TestVersionCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"4.4.0", "4.3.1", 1, true},
		{"4.3.1", "4.4.0", -1, true},
		{"4.4.0", "4.4.0", 0, true},
		{"v4.4.0", "4.4.0", 0, true},
		{"4.10.0", "4.9.9", 1, true},
		{"5.0.0", "4.99.99", 1, true},
		{"4.4.0-rc.1", "4.4.0", -1, true},
		{"4.4.0", "4.4.0-rc.1", 1, true},
		{"4.4.0-rc.2", "4.4.0-rc.10", -1, true},
		{"4.4.0-alpha", "4.4.0-beta", -1, true},
		{"4.4.0-1", "4.4.0-alpha", -1, true},
		{"4.4.0-alpha", "4.4.0-alpha.1", -1, true},
		{"4.4.0-alpha", "4.4.0-1", 1, true},
		{"4.4.0-1", "4.4.0-1", 0, true},
		{"4.4.0-rc..1", "4.4.0", 0, false},
		{"4.3.1~11-gabc1234", "4.3.1", -1, true},
		{"4.3.1~11-gabc1234", "4.3.0", 1, true},
		{"4.3.1~11-gabc1234", "4.3.1~11-gabc1234", 0, true},
		{"4.4.0+build.5", "4.4.0", 0, true},
		{"dev", "4.4.0", 0, false},
		{"4.4.0", "garbage", 0, false},
		{"4.4", "4.4.0", 0, false},
		{"4.4.x", "4.4.0", 0, false},
		{"", "4.4.0", 0, false},
		{"4.4.0-", "4.4.0", 0, false},
		{"04.4.0", "4.4.0", 0, false},
		{"99999999999999999999.0.0", "1.0.0", 0, false},
	}
	for _, c := range cases {
		got, ok := Compare(c.a, c.b)
		if got != c.want || ok != c.ok {
			t.Errorf("Compare(%q, %q) = %d, %v; want %d, %v", c.a, c.b, got, ok, c.want, c.ok)
		}
	}
	if !IsRelease("4.4.0") || !IsRelease("4.4.0-rc.1") || IsRelease("4.3.1~11-gabc") || IsRelease("v4.4.0") || IsRelease("dev") {
		t.Error("IsRelease")
	}
	if BelowFloor("4.4.0") || !BelowFloor("4.3.1") || BelowFloor("dev") || !BelowFloor("4.4.0-rc.1") {
		t.Error("BelowFloor")
	}
}

func TestHasher(t *testing.T) {
	h := NewSHA256()
	_, _ = h.Write([]byte("agent"))
	if h.Hex() != sum([]byte("agent")) {
		t.Fatal("hex digest")
	}
}
