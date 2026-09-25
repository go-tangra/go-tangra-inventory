package sender_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/ingest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// testCA is a throwaway private CA generated per test (nothing is committed).
type testCA struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "inventory ingest test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &testCA{cert: cert, key: key}
}

// writeCA writes the CA certificate as PEM and returns its path.
func (c *testCA) writeCA(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// issueServer writes a server certificate/key for the given DNS names (plus
// 127.0.0.1) and returns their paths.
func (c *testCA) issueServer(t *testing.T, dnsNames ...string) (certFile, keyFile string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "ingest"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, c.cert, &key.PublicKey, c.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

// tlsIngest serves a real ingest edge over TLS on a loopback port and returns
// its address plus the enroll service used to mint tokens.
func tlsIngest(t *testing.T, certFile, keyFile string) (addr string, enr *enroll.Service) {
	t.Helper()
	mem := memstore.New()
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	enr = enroll.New(mem, env)
	snaps := snapshots.New(mem, hosts.New(mem), events.HubPublisher{})
	srv := ingest.New(enr, snaps, registry.NewMemory(), mem, 0, "tls-test")

	loader, err := ingest.NewCertLoader(certFile, keyFile)
	if err != nil {
		t.Fatalf("NewCertLoader: %v", err)
	}
	gs := ingest.NewGRPCServer(srv, loader.TransportOption())
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return lis.Addr().String(), enr
}

func mint(t *testing.T, enr *enroll.Service) string {
	t.Helper()
	secret, _, err := enr.MintToken(context.Background(), "tenant-a", "admin@example.com", "tls", time.Hour)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	return secret
}

// rawEnroll performs one Enroll call (no retries) through the sender's dialer.
func rawEnroll(t *testing.T, s *sender.Sender) error {
	t.Helper()
	client, conn, err := s.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.Enroll(ctx, &invv1.EnrollRequest{EnrollmentToken: "x"})
	return err
}

func TestTLSIngest_EnrollAndSubmitEndToEnd(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.issueServer(t, "localhost", "ingest.example.org")
	addr, enr := tlsIngest(t, certFile, keyFile)

	_, port, _ := net.SplitHostPort(addr)
	cases := []struct {
		name     string
		endpoint string
		opts     sender.Options
	}{
		{"ip endpoint", addr, sender.Options{CAFile: ca.writeCA(t)}},
		{"dns endpoint", net.JoinHostPort("localhost", port), sender.Options{CAFile: ca.writeCA(t)}},
		{"server name override", addr, sender.Options{CAFile: ca.writeCA(t), ServerName: "ingest.example.org"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := sender.New(tc.endpoint, tc.opts)
			ctx := context.Background()
			ident := store.Identity{Hostname: "h-" + strings.ReplaceAll(tc.name, " ", "-"), HardwareUUID: "uuid-" + tc.name}
			agentID, cred, err := s.Enroll(ctx, mint(t, enr), ident, "1.0.0")
			if err != nil {
				t.Fatalf("Enroll over TLS: %v", err)
			}
			snapID, err := s.Submit(ctx, agentID, cred, store.Inventory{Identity: ident, CollectedAt: time.Now(), AgentVersion: "1.0.0"})
			if err != nil {
				t.Fatalf("Submit over TLS: %v", err)
			}
			if snapID == "" {
				t.Fatal("empty snapshot id")
			}
		})
	}
}

func TestTLSIngest_PlaintextClientRefused(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.issueServer(t, "localhost")
	addr, _ := tlsIngest(t, certFile, keyFile)

	// The agent's development (insecure) mode cannot talk to a TLS listener.
	err := rawEnroll(t, sender.New(addr, sender.Options{Insecure: true}))
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("plaintext client: err = %v, want Unavailable", err)
	}

	// Nor can a bare plaintext gRPC client.
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = invv1.NewIngestServiceClient(conn).Enroll(ctx, &invv1.EnrollRequest{EnrollmentToken: "x"})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("raw plaintext client: err = %v, want Unavailable", err)
	}
}

func TestTLSIngest_ServerVerification(t *testing.T) {
	ca := newTestCA(t)
	certFile, keyFile := ca.issueServer(t, "localhost")
	addr, _ := tlsIngest(t, certFile, keyFile)

	// Sanity: the pinned CA succeeds (the call reaches the server and is
	// rejected on the bogus token, not at the handshake).
	if err := rawEnroll(t, sender.New(addr, sender.Options{CAFile: ca.writeCA(t)})); status.Code(err) == codes.Unavailable {
		t.Fatalf("pinned CA: handshake failed: %v", err)
	}

	cases := []struct {
		name string
		opts sender.Options
	}{
		// System roots do not know the private CA.
		{"system roots", sender.Options{}},
		// A CA file pins trust: a different CA is refused.
		{"pinned other CA", sender.Options{CAFile: newTestCA(t).writeCA(t)}},
		// The server name must match the certificate.
		{"wrong server name", sender.Options{CAFile: ca.writeCA(t), ServerName: "evil.example.org"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := rawEnroll(t, sender.New(addr, tc.opts))
			if status.Code(err) != codes.Unavailable {
				t.Fatalf("err = %v, want Unavailable (handshake refused)", err)
			}
		})
	}
}

func TestSenderOptions_BadCAFile(t *testing.T) {
	dir := t.TempDir()
	notPEM := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(dir, "missing.pem"), notPEM} {
		if _, _, err := sender.New("127.0.0.1:1", sender.Options{CAFile: p}).Dial(); err == nil || !strings.Contains(err.Error(), "ca_file") {
			t.Fatalf("Dial with ca_file %s: err = %v, want ca_file error", p, err)
		}
	}
}

func TestSenderOptions_ClientTLSConfig(t *testing.T) {
	cfg, err := sender.Options{ServerName: "ingest.example.org"}.ClientTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MinVersion < 0x0303 { // TLS 1.2
		t.Fatalf("MinVersion = %x, want >= TLS 1.2", cfg.MinVersion)
	}
	if cfg.InsecureSkipVerify {
		t.Fatal("InsecureSkipVerify must never be set")
	}
	if cfg.RootCAs != nil {
		t.Fatal("no ca_file must mean system roots (nil RootCAs)")
	}
	if cfg.ServerName != "ingest.example.org" {
		t.Fatalf("ServerName = %q", cfg.ServerName)
	}
}
