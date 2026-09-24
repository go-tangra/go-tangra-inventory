package ingest

import (
	"context"
	"crypto/tls"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra/v4/freyatest/testutil"
)

// writePair writes an in-test issued certificate and key under dir.
func writePair(t *testing.T, dir string, crt tls.Certificate) (certFile, keyFile string) {
	t.Helper()
	certFile = filepath.Join(dir, "tls.crt")
	keyFile = filepath.Join(dir, "tls.key")
	if err := os.WriteFile(certFile, testutil.CertPEM(crt), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, testutil.KeyPEM(crt), 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile
}

func serial(t *testing.T, l *CertLoader) string {
	t.Helper()
	c, err := l.TLSConfig().GetCertificate(&tls.ClientHelloInfo{})
	if err != nil {
		t.Fatalf("GetCertificate: %v", err)
	}
	return c.Leaf.SerialNumber.String()
}

func TestCertLoader_LoadsAndServes(t *testing.T) {
	ca := testutil.MustCA("example.org")
	crt := ca.MustIssue("ingest", testutil.IssueOptions{})
	certFile, keyFile := writePair(t, t.TempDir(), crt)

	l, err := NewCertLoader(certFile, keyFile)
	if err != nil {
		t.Fatalf("NewCertLoader: %v", err)
	}
	cfg := l.TLSConfig()
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %x, want TLS 1.2", cfg.MinVersion)
	}
	if cfg.ClientAuth != tls.NoClientCert {
		t.Fatalf("ClientAuth = %v, want NoClientCert (agents authenticate by credential)", cfg.ClientAuth)
	}
	if got, want := serial(t, l), crt.Leaf.SerialNumber.String(); got != want {
		t.Fatalf("served serial %s, want %s", got, want)
	}
	if l.TransportOption() == nil {
		t.Fatal("TransportOption returned nil")
	}
}

func TestCertLoader_Refusals(t *testing.T) {
	ca := testutil.MustCA("example.org")
	dir := t.TempDir()
	certFile, keyFile := writePair(t, dir, ca.MustIssue("ingest", testutil.IssueOptions{}))
	other := ca.MustIssue("other", testutil.IssueOptions{})
	mismatchedKey := filepath.Join(dir, "other.key")
	if err := os.WriteFile(mismatchedKey, testutil.KeyPEM(other), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, cert, key, want string
	}{
		{"empty", "", "", "tls_cert_file"},
		{"missing cert", filepath.Join(dir, "nope.crt"), keyFile, "cert"},
		{"missing key", certFile, filepath.Join(dir, "nope.key"), "key"},
		{"mismatched", certFile, mismatchedKey, "certificate"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewCertLoader(tc.cert, tc.key)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestCertLoader_HotReload(t *testing.T) {
	ca := testutil.MustCA("example.org")
	dir := t.TempDir()
	first := ca.MustIssue("ingest", testutil.IssueOptions{})
	certFile, keyFile := writePair(t, dir, first)
	l, err := NewCertLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, 16)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l.Watch(ctx, 10*time.Millisecond, func(err error) {
		select {
		case errs <- err:
		default:
		}
	})

	// A broken pair on disk keeps the current certificate and reports the error.
	if err := os.WriteFile(keyFile, []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-errs:
	case <-time.After(5 * time.Second):
		t.Fatal("reload error not reported")
	}
	if got := serial(t, l); got != first.Leaf.SerialNumber.String() {
		t.Fatalf("certificate replaced by a broken pair: serial %s", got)
	}

	// A renewed pair is picked up without a restart.
	second := ca.MustIssue("ingest", testutil.IssueOptions{})
	writePair(t, dir, second)
	want := second.Leaf.SerialNumber.String()
	deadline := time.Now().Add(5 * time.Second)
	for serial(t, l) != want {
		if time.Now().After(deadline) {
			t.Fatal("renewed certificate not picked up")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
