package agentrelease

import (
	"crypto/ed25519"
	"testing"
)

// FuzzManifest: the strict parser never panics, and anything it accepts
// re-validates, has only valid platforms and verifies only with the right
// key and signature.
func FuzzManifest(f *testing.F) {
	f.Add(sampleManifest("4.4.0").Encode())
	f.Add([]byte(`{"schema":1,"version":"4.4.0","created_at":"2026-10-02T10:00:00Z","key_id":"k","artifacts":[]}`))
	f.Add([]byte(`{}`))
	pub, priv := testKey(f)
	k := Keyring{"test-key": pub}
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := ParseManifest(b)
		if err != nil {
			return
		}
		if m.validate() != nil || len(m.Artifacts) > MaxArtifacts {
			t.Fatal("accepted manifest does not re-validate")
		}
		for _, a := range m.Artifacts {
			if !a.Platform().Valid() || a.Size < 1 || a.Size > MaxArtifactSize {
				t.Fatalf("bad artifact accepted: %+v", a)
			}
		}
		if m.KeyID == "test-key" {
			if _, err := k.Verify(b, ed25519.Sign(priv, b), "test-key"); err != nil {
				t.Fatalf("valid signature refused: %v", err)
			}
		}
		if _, err := k.Verify(b, make([]byte, ed25519.SignatureSize), "test-key"); err == nil {
			t.Fatal("zero signature accepted")
		}
	})
}

// FuzzVersion: comparison never panics, is antisymmetric and reflexive.
func FuzzVersion(f *testing.F) {
	for _, s := range []string{"4.4.0", "4.4.0-rc.1", "4.3.1~11-gabc", "dev", "v1.2.3+x", "1.2", ""} {
		f.Add(s, "4.4.0")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		ab, okAB := Compare(a, b)
		ba, okBA := Compare(b, a)
		if okAB != okBA || (okAB && ab != -ba) {
			t.Fatalf("Compare(%q,%q)=%d,%v but Compare(%q,%q)=%d,%v", a, b, ab, okAB, b, a, ba, okBA)
		}
		if aa, ok := Compare(a, a); ok && aa != 0 {
			t.Fatalf("Compare(%q,%q) = %d", a, a, aa)
		}
		if IsRelease(a) {
			if _, ok := Compare(a, "0.0.0"); !ok {
				t.Fatalf("release %q not comparable", a)
			}
		}
	})
}
