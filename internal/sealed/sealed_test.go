package sealed

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func kek(t *testing.T) []byte {
	t.Helper()
	k := bytes.Repeat([]byte{7}, 32)
	return k
}

func TestSealOpenRoundTrip(t *testing.T) {
	e, err := NewEnvelope(kek(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewEnvelope([]byte("short")); !errors.Is(err, ErrKEK) {
		t.Fatalf("short kek accepted: %v", err)
	}
	pt := []byte(`{"host":"acme.example.org","apiToken":"LCM-MARKER-TOKEN-1"}`)
	blob, err := e.Seal(pt, ADIssuer("i1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("LCM-MARKER-TOKEN-1")) {
		t.Fatal("plaintext in blob")
	}
	got, err := e.Open(blob, ADIssuer("i1"))
	if err != nil || !bytes.Equal(got, pt) {
		t.Fatalf("open: %v %q", err, got)
	}
	// Wrong associated data, tampering, wrong KEK and short blobs are refused.
	if _, err := e.Open(blob, ADIssuer("i2")); !errors.Is(err, ErrTampered) {
		t.Fatalf("ad mismatch: %v", err)
	}
	bad := append([]byte(nil), blob...)
	bad[len(bad)-1] ^= 1
	if _, err := e.Open(bad, ADIssuer("i1")); !errors.Is(err, ErrTampered) {
		t.Fatalf("tamper: %v", err)
	}
	e2, _ := NewEnvelope(bytes.Repeat([]byte{8}, 32))
	if _, err := e2.Open(blob, ADIssuer("i1")); !errors.Is(err, ErrTampered) {
		t.Fatalf("wrong kek: %v", err)
	}
	if _, err := e.Open(blob[:10], ADIssuer("i1")); !errors.Is(err, ErrTampered) {
		t.Fatalf("short: %v", err)
	}
	if _, err := e.Seal(bytes.Repeat([]byte("x"), MaxSettingsBytes+1), ADIssuer("i1")); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
}

func TestADHelpers(t *testing.T) {
	cases := []struct {
		name string
		got  []byte
		want string
	}{
		{"ca", ADCA("c1"), "ca:c1"},
		{"issuer", ADIssuer("i1"), "issuer:i1"},
		{"secret", ADSecret("s1"), "secret:s1"},
		{"webhook", ADWebhook("w1"), "webhook:w1"},
		{"certkey", ADCertKey("k1"), "certkey:k1"},
		{"target", ADTarget("t1"), "target:t1"},
		{"config", ADConfig("cfg1"), "config:cfg1"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}

func TestRedactMergeScrub(t *testing.T) {
	fields := []string{"apiToken"}
	stored := Settings{"host": "h", "port": float64(25), "apiToken": "LCM-MARKER-TOKEN-2"}
	r := Redact(stored, fields)
	if r["apiToken"] != Marker || r["host"] != "h" {
		t.Fatalf("redact %v", r)
	}
	if v, ok := Redact(Settings{"apiToken": ""}, fields)["apiToken"]; ok {
		t.Fatalf("empty credential should vanish: %v", v)
	}
	if _, ok := Public(stored, fields)["apiToken"]; ok {
		t.Fatal("public keeps apiToken")
	}
	// Merge: marker/omitted keep, "" clears, value replaces.
	if m := Merge(stored, Settings{"host": "h2", "apiToken": Marker}, fields); m["apiToken"] != "LCM-MARKER-TOKEN-2" || m["host"] != "h2" {
		t.Fatalf("merge marker %v", m)
	}
	if m := Merge(stored, Settings{"host": "h2"}, fields); m["apiToken"] != "LCM-MARKER-TOKEN-2" {
		t.Fatalf("merge omitted %v", m)
	}
	if m := Merge(stored, Settings{"host": "h2", "apiToken": ""}, fields); m["apiToken"] != nil {
		t.Fatalf("merge clear %v", m)
	}
	if m := Merge(stored, Settings{"apiToken": "new"}, fields); m["apiToken"] != "new" {
		t.Fatalf("merge replace %v", m)
	}
	if m := Merge(Settings{}, Settings{"apiToken": Marker}, fields); m["apiToken"] != nil {
		t.Fatalf("merge marker without stored %v", m)
	}
	// Scrub removes the credential (raw and base64) and later lines.
	enc := base64.StdEncoding.EncodeToString([]byte("LCM-MARKER-TOKEN-2"))
	s := Scrub("403 auth failed for LCM-MARKER-TOKEN-2 ("+enc+")\nsecond line LCM-MARKER-TOKEN-2", stored, fields)
	if strings.Contains(s, "LCM-MARKER") || strings.Contains(s, enc) || strings.Contains(s, "second") {
		t.Fatalf("scrub %q", s)
	}
	if got := Scrub(strings.Repeat("a", 2000), stored, fields); len(got) != 1024 {
		t.Fatalf("scrub length %d", len(got))
	}
}

func TestDecodeEncode(t *testing.T) {
	if _, err := Decode([]byte(`[1]`)); err == nil {
		t.Fatal("array accepted")
	}
	if _, err := Decode(bytes.Repeat([]byte("x"), MaxSettingsBytes+1)); !errors.Is(err, ErrTooLarge) {
		t.Fatal("oversize accepted")
	}
	s, err := Decode([]byte(`{"host":"h"}`))
	if err != nil || s["host"] != "h" {
		t.Fatal(err)
	}
	if _, err := Encode(Settings{"x": strings.Repeat("y", MaxSettingsBytes)}); !errors.Is(err, ErrTooLarge) {
		t.Fatal("encode oversize accepted")
	}
	if _, err := Encode(Settings{"f": func() {}}); err == nil {
		t.Fatal("unmarshalable accepted")
	}
	if b, err := Encode(s); err != nil || string(b) != `{"host":"h"}` {
		t.Fatalf("encode %s %v", b, err)
	}
}

func TestLoadKEK(t *testing.T) {
	dir := t.TempDir()
	raw := bytes.Repeat([]byte{9}, 32)
	p := filepath.Join(dir, "kek")
	if err := os.WriteFile(p, []byte(base64.StdEncoding.EncodeToString(raw)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	k, err := LoadKEK("file", p, "")
	if err != nil || !bytes.Equal(k, raw) {
		t.Fatalf("file base64: %v", err)
	}
	if err := os.WriteFile(p, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if k, err := LoadKEK("file", p, ""); err != nil || !bytes.Equal(k, raw) {
		t.Fatalf("file raw: %v", err)
	}
	t.Setenv("LCM_KEK", base64.RawStdEncoding.EncodeToString(raw))
	if k, err := LoadKEK("env", "", "LCM_KEK"); err != nil || !bytes.Equal(k, raw) {
		t.Fatalf("env: %v", err)
	}
	t.Setenv("LCM_KEK", "")
	if _, err := LoadKEK("env", "", "LCM_KEK"); err == nil {
		t.Fatal("empty env accepted")
	}
	if _, err := LoadKEK("file", filepath.Join(dir, "missing"), ""); err == nil {
		t.Fatal("missing file accepted")
	}
	if _, err := LoadKEK("vault", "", ""); err == nil {
		t.Fatal("unknown source accepted")
	}
	if err := os.WriteFile(p, []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKEK("file", p, ""); !errors.Is(err, ErrKEK) {
		t.Fatalf("bad key: %v", err)
	}
}

// countReader fails after n successful reads.
type countReader struct{ left int }

func (c *countReader) Read(b []byte) (int, error) {
	if c.left == 0 {
		return 0, errors.New("rng down")
	}
	c.left--
	for i := range b {
		b[i] = 1
	}
	return len(b), nil
}

func TestSealRandomFailure(t *testing.T) {
	e, _ := NewEnvelope(kek(t))
	old := randReader
	defer func() { randReader = old }()
	for n := 0; n < 3; n++ {
		randReader = &countReader{left: n}
		if _, err := e.Seal([]byte("{}"), ADCA("c")); err == nil {
			t.Fatalf("read %d: expected rng error", n)
		}
	}
	randReader = &countReader{left: 3}
	if _, err := e.Seal([]byte("{}"), ADCA("c")); err != nil {
		t.Fatal(err)
	}
}
