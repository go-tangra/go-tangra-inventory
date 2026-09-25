// Package sender speaks the off-mesh IngestService edge on behalf of the
// endpoint agent: it enrolls (consuming a one-time token for a per-agent
// credential) and submits collected inventory, presenting the credential in
// call metadata. Transient failures are retried with exponential backoff.
package sender

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const (
	// MetaAgentIDKey carries the agent id on SubmitInventory/StreamCommands.
	MetaAgentIDKey = "x-agent-id"
	// MetaCredentialKey carries the per-agent credential on authenticated calls.
	MetaCredentialKey = "x-agent-credential"

	defaultCallTimeout = 30 * time.Second
	baseBackoff        = 1 * time.Second
	maxBackoff         = 30 * time.Second
	maxAttempts        = 5
)

// Options selects the transport to the ingest edge.
type Options struct {
	// Insecure dials in plaintext (development stack only).
	Insecure bool
	// CAFile pins the ingest server's issuing CA (PEM bundle): when set, only
	// these CAs are trusted; when empty the operating system roots are used.
	CAFile string
	// ServerName overrides the name verified against the server certificate
	// (default: the host part of the endpoint).
	ServerName string
}

// ClientTLSConfig builds the agent's TLS configuration: TLS 1.2 minimum, the
// server certificate always verified (system roots, or only the CAs in CAFile),
// and an optional server-name override. No client certificate is presented; the
// agent authenticates with its per-agent credential in call metadata.
func (o Options) ClientTLSConfig() (*tls.Config, error) {
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: o.ServerName}
	if o.CAFile != "" {
		raw, err := os.ReadFile(o.CAFile) // #nosec G304 -- operator-supplied CA path
		if err != nil {
			return nil, fmt.Errorf("sender: ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("sender: ca_file %s: no PEM certificate found", o.CAFile)
		}
		cfg.RootCAs = pool
	}
	return cfg, nil
}

// Sender dials the ingest edge and performs enroll/submit. It is safe to reuse
// across calls; each call dials a fresh connection (the ingest edge is contacted
// infrequently, on the agent's collection interval).
type Sender struct {
	endpoint string
	opts     Options
	timeout  time.Duration
}

// New returns a Sender for the given ingest endpoint. With opts.Insecure the
// connection is plaintext (development only); otherwise TLS verified against
// the system roots or the pinned opts.CAFile.
func New(endpoint string, opts Options) *Sender {
	return &Sender{endpoint: endpoint, opts: opts, timeout: defaultCallTimeout}
}

// Dial opens a gRPC connection and returns an IngestService client. Callers own
// the returned connection and must Close it (used by the daemon for streaming).
func (s *Sender) Dial() (invv1.IngestServiceClient, *grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if s.opts.Insecure {
		creds = insecure.NewCredentials()
	} else {
		tlsCfg, err := s.opts.ClientTLSConfig()
		if err != nil {
			return nil, nil, err
		}
		creds = credentials.NewTLS(tlsCfg)
	}
	conn, err := grpc.NewClient(s.endpoint, grpc.WithTransportCredentials(creds))
	if err != nil {
		return nil, nil, fmt.Errorf("sender: dial %s: %w", s.endpoint, err)
	}
	return invv1.NewIngestServiceClient(conn), conn, nil
}

// AuthContext returns ctx with the agent id and credential attached as metadata.
func AuthContext(ctx context.Context, agentID, credential string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, MetaAgentIDKey, agentID, MetaCredentialKey, credential)
}

// Enroll consumes a one-time enrollment token and returns the issued agent id
// and per-agent credential (the credential is returned exactly once).
func (s *Sender) Enroll(ctx context.Context, token string, ident store.Identity, version string) (agentID, credential string, err error) {
	client, conn, err := s.Dial()
	if err != nil {
		return "", "", err
	}
	defer conn.Close()

	req := &invv1.EnrollRequest{
		EnrollmentToken: token,
		Identity:        identityToProto(ident),
		AgentVersion:    version,
	}
	err = s.retry(ctx, func(cctx context.Context) error {
		resp, e := client.Enroll(cctx, req)
		if e != nil {
			return e
		}
		agentID, credential = resp.GetAgentId(), resp.GetAgentCredential()
		return nil
	})
	if err != nil {
		return "", "", fmt.Errorf("sender: enroll: %w", err)
	}
	if agentID == "" || credential == "" {
		return "", "", errors.New("sender: enroll returned empty agent id or credential")
	}
	return agentID, credential, nil
}

// Submit posts an inventory snapshot authenticated by the per-agent credential
// and returns the assigned snapshot id.
func (s *Sender) Submit(ctx context.Context, agentID, credential string, inv store.Inventory) (snapshotID string, err error) {
	client, conn, err := s.Dial()
	if err != nil {
		return "", err
	}
	defer conn.Close()

	req := &invv1.SubmitRequest{Inventory: toProto(inv)}
	err = s.retry(ctx, func(cctx context.Context) error {
		resp, e := client.SubmitInventory(AuthContext(cctx, agentID, credential), req)
		if e != nil {
			return e
		}
		snapshotID = resp.GetSnapshotId()
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("sender: submit: %w", err)
	}
	return snapshotID, nil
}

// retry runs fn with a per-attempt timeout, backing off exponentially on
// transient errors and giving up on permanent ones (e.g. Unauthenticated).
func (s *Sender) retry(ctx context.Context, fn func(context.Context) error) error {
	backoff := baseBackoff
	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, s.timeout)
		err := fn(cctx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable(err) {
			return err
		}
		if attempt == maxAttempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
	return lastErr
}

// retryable reports whether err is worth retrying (transient transport/server
// conditions) rather than a permanent rejection.
func retryable(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted, codes.Unknown:
		return true
	default:
		return false
	}
}
