package certmaterial

import (
	"bytes"
	"crypto"
	"strings"
	"testing"
)

// FuzzParseBundle: ParseBundle never panics, and an accepted bundle
// satisfies every invariant the relay and the agent rely on.
func FuzzParseBundle(f *testing.F) {
	cert, chain, key := fixture(f, "ecdsa.crt"), fixture(f, "chain.pem"), fixture(f, "ecdsa.pkcs8.key")
	f.Add(cert, chain, key, true)
	f.Add(cert, []byte{}, []byte{}, false)
	f.Add(fixture(f, "rsa.crt"), chain, fixture(f, "rsa.pkcs1.key"), true)
	f.Add(cert, chain, fixture(f, "mismatch.key"), true)
	f.Add([]byte("-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"), []byte("x"), []byte("y"), false)
	f.Fuzz(func(t *testing.T, cert, chain, key []byte, requireKey bool) {
		b, err := ParseBundle(cert, chain, key, Options{RequireKey: requireKey, Now: fixedNow})
		if err != nil {
			if strings.Contains(err.Error(), "-----BEGIN") {
				t.Fatalf("error carries PEM: %v", err)
			}
			return
		}
		if len(cert)+len(chain)+len(key) > MaxBundleBytes || len(b.Chain) > MaxChainCerts {
			t.Fatal("bounds not enforced")
		}
		if b.Leaf == nil || b.Fingerprint != Fingerprint(b.Leaf.Raw) || len(b.Fingerprint) != 64 {
			t.Fatal("fingerprint invariant")
		}
		if requireKey && !b.HasKey {
			t.Fatal("key required but absent")
		}
		if b.HasKey {
			s, err := parseKey(b.KeyPEM)
			if err != nil {
				t.Fatal(err)
			}
			if !b.Leaf.PublicKey.(interface{ Equal(crypto.PublicKey) bool }).Equal(s.Public()) {
				t.Fatal("accepted key does not match the leaf")
			}
		}
		if fixtureNow.Before(b.NotBefore) || fixtureNow.After(b.NotAfter) {
			t.Fatal("accepted outside the validity window")
		}
		if !bytes.HasPrefix(b.FullChainPEM, b.CertPEM) || bytes.Contains(b.FullChainPEM, []byte("\n\n")) {
			t.Fatal("fullchain invariant")
		}
	})
}
