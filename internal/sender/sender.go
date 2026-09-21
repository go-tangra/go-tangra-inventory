// Package sender speaks the off-mesh IngestService edge on behalf of the
// endpoint agent: it enrolls (consuming a one-time token for a per-agent
// credential) and submits collected inventory, presenting the credential in
// call metadata. Transient failures are retried with exponential backoff.
package sender

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"time"

	invv1 "github.com/go-freya/freya/services/inventory/api/proto/inventory/v1"
	"github.com/go-freya/freya/services/inventory/internal/store"
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

// Sender dials the ingest edge and performs enroll/submit. It is safe to reuse
// across calls; each call dials a fresh connection (the ingest edge is contacted
// infrequently, on the agent's collection interval).
type Sender struct {
	endpoint string
	insecure bool
	timeout  time.Duration
}

// New returns a Sender for the given ingest endpoint. When insecure is true the
// connection uses plaintext (development only); otherwise TLS with system roots.
func New(endpoint string, insecureConn bool) *Sender {
	return &Sender{endpoint: endpoint, insecure: insecureConn, timeout: defaultCallTimeout}
}

// Dial opens a gRPC connection and returns an IngestService client. Callers own
// the returned connection and must Close it (used by the daemon for streaming).
func (s *Sender) Dial() (invv1.IngestServiceClient, *grpc.ClientConn, error) {
	var creds credentials.TransportCredentials
	if s.insecure {
		creds = insecure.NewCredentials()
	} else {
		creds = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
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
