package certmaterial

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func opts() Options { return Options{RequireKey: true, Now: fixedNow} }

func TestParseBundleValid(t *testing.T) {
	chain := fixture(t, "chain.pem")
	cases := []struct{ cert, key string }{
		{"rsa.crt", "rsa.pkcs8.key"}, {"rsa.crt", "rsa.pkcs1.key"},
		{"ecdsa.crt", "ecdsa.pkcs8.key"}, {"ecdsa.crt", "ecdsa.sec1.key"},
		{"ed25519.crt", "ed25519.key"},
	}
	for _, c := range cases {
		t.Run(c.cert+"/"+c.key, func(t *testing.T) {
			cert, key := fixture(t, c.cert), fixture(t, c.key)
			b, err := ParseBundle(cert, chain, key, opts())
			if err != nil {
				t.Fatal(err)
			}
			blk, _ := pem.Decode(cert)
			sum := sha256.Sum256(blk.Bytes)
			if b.Fingerprint != hex.EncodeToString(sum[:]) || b.Fingerprint != strings.ToLower(b.Fingerprint) {
				t.Fatalf("fingerprint %s", b.Fingerprint)
			}
			if !bytes.Equal(b.Leaf.Raw, blk.Bytes) || len(b.Chain) != 2 || b.Chain[0].Subject.CommonName != "Fixture Issuing CA" {
				t.Fatal("leaf must be first, chain intermediate then root")
			}
			if !b.HasKey || !bytes.Equal(b.KeyPEM, key) {
				t.Fatal("key not kept")
			}
			if b.CommonName != "www.example.com" || b.Serial != b.Leaf.SerialNumber.Text(16) || b.Serial != strings.ToLower(b.Serial) {
				t.Fatalf("cn/serial %q %q", b.CommonName, b.Serial)
			}
			if fmt.Sprint(b.DNSNames) != "[www.example.com example.com]" || fmt.Sprint(b.IPAddresses) != "[192.0.2.10 2001:db8::1]" {
				t.Fatalf("sans %v %v", b.DNSNames, b.IPAddresses)
			}
			if !b.NotBefore.Equal(b.Leaf.NotBefore) || !b.NotAfter.Equal(b.Leaf.NotAfter) {
				t.Fatal("validity")
			}
			want := string(cert) + string(chain)
			if string(b.FullChainPEM) != want || strings.Contains(string(b.FullChainPEM), "\n\n") {
				t.Fatalf("fullchain:\n%s", b.FullChainPEM)
			}
			if string(b.CertPEM) != string(cert) || string(b.ChainPEM) != string(chain) {
				t.Fatal("re-encoded PEM differs")
			}
		})
	}
}

func TestParseBundleLeafWithChainInCert(t *testing.T) {
	// lcm may return the leaf followed by its chain in cert_pem.
	cert := append(fixture(t, "ecdsa.crt"), fixture(t, "intermediate.pem")...)
	b, err := ParseBundle(cert, fixture(t, "root.pem"), fixture(t, "ecdsa.pkcs8.key"), opts())
	if err != nil {
		t.Fatal(err)
	}
	if b.Leaf.Subject.CommonName != "www.example.com" || len(b.Chain) != 2 {
		t.Fatalf("leaf first, rest chain: %d", len(b.Chain))
	}
}

func TestParseBundleWithoutKey(t *testing.T) {
	b, err := ParseBundle(fixture(t, "ecdsa.crt"), nil, nil, Options{Now: fixedNow})
	if err != nil {
		t.Fatal(err)
	}
	if b.HasKey || b.KeyPEM != nil || len(b.Chain) != 0 || b.ChainPEM != nil {
		t.Fatal("no key, no chain expected")
	}
	if string(b.FullChainPEM) != string(fixture(t, "ecdsa.crt")) {
		t.Fatal("fullchain without chain is the leaf")
	}
	if _, err := ParseBundle(fixture(t, "ecdsa.crt"), nil, []byte("  \n"), opts()); !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("whitespace key with RequireKey: %v", err)
	}
}

func TestParseBundleDefaultClock(t *testing.T) {
	// The fixtures are valid until 2036; the real clock is used without Now.
	if _, err := ParseBundle(fixture(t, "ecdsa.crt"), nil, nil, Options{}); err != nil {
		t.Fatal(err)
	}
}

func encryptedLegacyKey(t *testing.T) []byte {
	blk, _ := pem.Decode(fixture(t, "ecdsa.sec1.key"))
	blk.Headers = map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00000000000000000000000000000000"}
	return pem.EncodeToMemory(blk)
}

func x25519Key(t *testing.T) []byte {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestParseBundleNegative(t *testing.T) {
	cert, chain, key := fixture(t, "ecdsa.crt"), fixture(t, "chain.pem"), fixture(t, "ecdsa.pkcs8.key")
	big := func(n int) []byte { return bytes.Repeat([]byte("A"), n) }
	certBlock := func(der []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}) }
	manyChain := bytes.Repeat(fixture(t, "intermediate.pem"), 600) // > 256 KiB of valid certificates
	cases := []struct {
		name             string
		cert, chain, key []byte
		o                Options
		want             error
		reason           string
	}{
		{"key mismatch", cert, chain, fixture(t, "mismatch.key"), opts(), ErrKeyMismatch, ReasonKeyMismatch},
		{"key of another leaf", cert, chain, fixture(t, "rsa.pkcs8.key"), opts(), ErrKeyMismatch, ReasonKeyMismatch},
		{"expired", fixture(t, "expired.crt"), chain, fixture(t, "expired.key"), opts(), ErrNotValid, ReasonCertificateNotValid},
		{"not yet valid", fixture(t, "notyet.crt"), chain, fixture(t, "notyet.key"), opts(), ErrNotValid, ReasonCertificateNotValid},
		{"expired by clock", cert, chain, key, Options{Now: func() time.Time { return time.Date(2037, 1, 1, 0, 0, 0, 0, time.UTC) }}, ErrNotValid, ReasonCertificateNotValid},
		{"non-PEM cert", []byte("hello"), chain, key, opts(), ErrMalformed, ReasonInvalidBundle},
		{"empty cert", nil, chain, key, opts(), ErrNoCertificate, ReasonInvalidBundle},
		{"leading garbage", append([]byte("junk\n"), cert...), chain, key, opts(), ErrMalformed, ReasonInvalidBundle},
		{"trailing garbage cert", append(append([]byte{}, cert...), "junk"...), chain, key, opts(), ErrMalformed, ReasonInvalidBundle},
		{"trailing garbage chain", cert, append(append([]byte{}, chain...), "junk"...), key, opts(), ErrMalformed, ReasonInvalidBundle},
		{"trailing garbage key", cert, chain, append(append([]byte{}, key...), "junk"...), opts(), ErrBadKey, ReasonInvalidBundle},
		{"leading garbage key", cert, chain, append([]byte("junk\n"), key...), opts(), ErrBadKey, ReasonInvalidBundle},
		{"two keys", cert, chain, append(append([]byte{}, key...), key...), opts(), ErrBadKey, ReasonInvalidBundle},
		{"key in cert position", key, chain, key, opts(), ErrMalformed, ReasonInvalidBundle},
		{"certificate in key position", cert, chain, cert, opts(), ErrBadKey, ReasonInvalidBundle},
		{"non-PEM key", cert, chain, []byte("hello"), opts(), ErrBadKey, ReasonInvalidBundle},
		{"encrypted PKCS#8 key", cert, chain, pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: []byte{1, 2}}), opts(), ErrBadKey, ReasonInvalidBundle},
		{"encrypted legacy key", cert, chain, encryptedLegacyKey(t), opts(), ErrBadKey, ReasonInvalidBundle},
		{"corrupt PKCS#8", cert, chain, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte{1, 2}}), opts(), ErrBadKey, ReasonInvalidBundle},
		{"corrupt PKCS#1", cert, chain, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte{1}}), opts(), ErrBadKey, ReasonInvalidBundle},
		{"corrupt SEC1", cert, chain, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: []byte{1}}), opts(), ErrBadKey, ReasonInvalidBundle},
		{"X25519 key (not a signer)", cert, chain, x25519Key(t), opts(), ErrBadKey, ReasonInvalidBundle},
		{"corrupt certificate DER", certBlock([]byte{1, 2, 3}), chain, key, opts(), ErrMalformed, ReasonInvalidBundle},
		{"PEM headers on certificate", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Headers: map[string]string{"X": "y"}, Bytes: []byte{1}}), chain, key, opts(), ErrMalformed, ReasonInvalidBundle},
		{"empty key with RequireKey", cert, chain, nil, opts(), ErrKeyRequired, ReasonInvalidBundle},
		{"cert > 64 KiB", big(MaxCertBytes + 1), nil, nil, opts(), ErrTooLarge, ReasonBundleTooLarge},
		{"chain > 256 KiB", cert, manyChain, key, opts(), ErrTooLarge, ReasonBundleTooLarge},
		{"key > 16 KiB", cert, chain, big(MaxKeyBytes + 1), opts(), ErrTooLarge, ReasonBundleTooLarge},
		{"total > 512 KiB", cert, big(MaxBundleBytes), key, opts(), ErrTooLarge, ReasonBundleTooLarge},
		{"chain > 10 certs", cert, fixture(t, "chain-11.pem"), key, opts(), ErrTooManyCerts, ReasonInvalidBundle},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseBundle(c.cert, c.chain, c.key, c.o)
			if !errors.Is(err, c.want) {
				t.Fatalf("err = %v, want %v", err, c.want)
			}
			if Reason(err) != c.reason {
				t.Fatalf("reason %q, want %q", Reason(err), c.reason)
			}
			assertNoMaterial(t, err, c.key)
		})
	}
}

func TestParseBundleTooManySANs(t *testing.T) {
	leaf := mustLeafWithSANs(t, MaxSANs+1, 0)
	if _, err := ParseBundle(leaf, nil, nil, Options{Now: fixedNow}); !errors.Is(err, ErrTooManySANs) {
		t.Fatalf("dns: %v", err)
	}
	leaf = mustLeafWithSANs(t, 0, MaxSANs+1)
	if _, err := ParseBundle(leaf, nil, nil, Options{Now: fixedNow}); !errors.Is(err, ErrTooManySANs) {
		t.Fatalf("ip: %v", err)
	}
	leaf = mustLeafWithSANs(t, MaxSANs, MaxSANs)
	if _, err := ParseBundle(leaf, nil, nil, Options{Now: fixedNow}); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
}

func mustLeafWithSANs(t *testing.T, dns, ips int) []byte {
	t.Helper()
	k := mustECDSA(t)
	tmpl := leafTemplate(99, fixtureNow.Add(-time.Hour), fixtureNow.Add(time.Hour))
	tmpl.DNSNames, tmpl.IPAddresses = nil, nil
	for i := 0; i < dns; i++ {
		tmpl.DNSNames = append(tmpl.DNSNames, fmt.Sprintf("h%d.example.com", i))
	}
	for i := 0; i < ips; i++ {
		tmpl.IPAddresses = append(tmpl.IPAddresses, []byte{10, 0, byte(i / 256), byte(i % 256)})
	}
	return certPEM(mustCert(t, tmpl, nil, k.Public(), k))
}

// assertNoMaterial: error messages never echo input material.
func assertNoMaterial(t *testing.T, err error, key []byte) {
	t.Helper()
	msg := err.Error()
	if strings.Contains(msg, "-----BEGIN") || strings.Contains(msg, "PRIVATE") ||
		(len(key) > 40 && strings.Contains(msg, string(key[30:40]))) {
		t.Fatalf("error leaks material: %q", msg)
	}
}

func TestReasonOfForeignError(t *testing.T) {
	if Reason(errors.New("x")) != ReasonInvalidBundle {
		t.Fatal("foreign errors map to invalid_bundle")
	}
	if !strings.HasPrefix(ErrNotValid.Error(), "certmaterial: ") {
		t.Fatal("message prefix")
	}
	if errors.Is(ErrNotValid, ErrKeyMismatch) || errors.Is(ErrNotValid, errors.New("x")) {
		t.Fatal("distinct sentinels must not match")
	}
}

func TestFingerprintAndFullChain(t *testing.T) {
	if Fingerprint([]byte("abc")) != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" {
		t.Fatal("fingerprint")
	}
	if got := string(FullChain([]byte("A\n\n"), []byte("\nB\n"))); got != "A\nB\n" {
		t.Fatalf("fullchain %q", got)
	}
	if got := FullChain(nil, nil); len(got) != 0 {
		t.Fatalf("empty fullchain %q", got)
	}
}
