package ingest

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// CertLoader serves the ingest listener's TLS certificate from a certificate/key
// file pair and re-reads it on an interval so a renewed certificate is picked up
// without a restart (the same model as the platform edge listener). A pair that
// fails to load on reload is reported and the current certificate kept.
//
// The ingest edge authenticates agents by their per-agent bearer credential in
// call metadata, so TLS here is server-authenticated only: no client
// certificate is requested.
type CertLoader struct {
	certFile, keyFile string

	cur atomic.Pointer[tls.Certificate]

	mu              sync.Mutex
	lastCert, lastK []byte
}

// NewCertLoader loads the certificate/key pair, refusing a missing, unreadable
// or mismatched pair.
func NewCertLoader(certFile, keyFile string) (*CertLoader, error) {
	if certFile == "" || keyFile == "" {
		return nil, errors.New("ingest: tls_cert_file and tls_key_file are required for the TLS listener")
	}
	l := &CertLoader{certFile: certFile, keyFile: keyFile}
	if err := l.Reload(); err != nil {
		return nil, err
	}
	return l, nil
}

// Reload re-reads the pair from disk; an unchanged pair is a no-op and a broken
// one leaves the current certificate in place.
func (l *CertLoader) Reload() error {
	certPEM, err := os.ReadFile(l.certFile) // #nosec G304 -- operator-supplied path
	if err != nil {
		return fmt.Errorf("ingest: tls cert: %w", err)
	}
	keyPEM, err := os.ReadFile(l.keyFile) // #nosec G304 -- operator-supplied path
	if err != nil {
		return fmt.Errorf("ingest: tls key: %w", err)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cur.Load() != nil && bytes.Equal(certPEM, l.lastCert) && bytes.Equal(keyPEM, l.lastK) {
		return nil
	}
	crt, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("ingest: tls certificate: %w", err)
	}
	l.cur.Store(&crt)
	l.lastCert, l.lastK = certPEM, keyPEM
	return nil
}

// Watch reloads the pair every interval until ctx is cancelled, passing reload
// failures to onErr (the current certificate keeps being served).
func (l *CertLoader) Watch(ctx context.Context, interval time.Duration, onErr func(error)) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := l.Reload(); err != nil && onErr != nil {
					onErr(err)
				}
			}
		}
	}()
}

// TLSConfig is the listener's server TLS configuration: TLS 1.2 minimum (1.3 is
// negotiated with any current agent), the hot-reloaded certificate, and no
// client-certificate request.
func (l *CertLoader) TLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientAuth: tls.NoClientCert,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			if c := l.cur.Load(); c != nil {
				return c, nil
			}
			return nil, errors.New("ingest: no tls certificate loaded")
		},
	}
}

// TransportOption is the grpc.ServerOption that makes the ingest gRPC server
// serve TLS with this loader's certificate; pass it to NewGRPCServer.
func (l *CertLoader) TransportOption() grpc.ServerOption {
	return grpc.Creds(credentials.NewTLS(l.TLSConfig()))
}
