package valkeykv

import (
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"
)

// ClientOption is shared by the event bus and the agent registry, so both
// Valkey connections get the same transport security (the registry used to
// connect in plaintext even when TLS was required).
func TestClientOption(t *testing.T) {
	base := Config{Addresses: []string{"valkey:6379"}, Username: "inventory", Password: "pw"}

	plain, err := ClientOption(Config{Addresses: base.Addresses, AllowPlaintext: true})
	if err != nil || plain.TLSConfig != nil {
		t.Fatalf("plaintext allowed: want no TLS, got %+v %v", plain.TLSConfig, err)
	}

	sys, err := ClientOption(base)
	if err != nil || sys.TLSConfig == nil || sys.TLSConfig.MinVersion != tls.VersionTLS13 || sys.TLSConfig.RootCAs != nil {
		t.Fatalf("TLS with system roots: %+v %v", sys.TLSConfig, err)
	}
	if sys.Username != "inventory" || sys.Password != "pw" || !sys.DisableCache {
		t.Fatalf("credentials/cache not carried over: %+v", sys)
	}

	ca, err := testutil.NewCA("example.org")
	if err != nil {
		t.Fatal(err)
	}
	withCA := base
	withCA.CAPEM = ca.BundlePEM()
	pinned, err := ClientOption(withCA)
	if err != nil || pinned.TLSConfig == nil || pinned.TLSConfig.RootCAs == nil {
		t.Fatalf("TLS with CA: %+v %v", pinned.TLSConfig, err)
	}
	want := x509.NewCertPool()
	want.AppendCertsFromPEM(ca.BundlePEM())
	if !pinned.TLSConfig.RootCAs.Equal(want) {
		t.Fatal("RootCAs is not the configured CA")
	}

	bad := base
	bad.CAPEM = []byte("not a certificate")
	if _, err := ClientOption(bad); err == nil {
		t.Fatal("invalid CA PEM must be refused")
	}
	if _, err := ClientOption(Config{}); err == nil {
		t.Fatal("missing addresses must be refused")
	}
}
