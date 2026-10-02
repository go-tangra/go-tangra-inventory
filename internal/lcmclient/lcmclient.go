package lcmclient

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	lcmv1 "github.com/go-tangra/go-tangra-lcm/sdk/v4/api/proto/lcm/v1"
)

// Errors (closed set; never carrying the remote message).
var (
	// ErrNotFound: lcm has no such certificate in the tenant.
	ErrNotFound = errors.New("lcmclient: certificate not found")
	// ErrNoKey: lcm holds no private key for the certificate (CSR-issued,
	// or the key was already handed out).
	ErrNoKey = errors.New("lcmclient: no stored private key")
	// ErrRevoked / ErrExpired: the certificate may no longer be delivered.
	ErrRevoked = errors.New("lcmclient: certificate revoked")
	ErrExpired = errors.New("lcmclient: certificate expired")
	// ErrUnavailable: lcm could not be reached in time (retryable).
	ErrUnavailable = errors.New("lcmclient: lcm unavailable")
)

// Certificate statuses reported by lcm.
const (
	StatusUnknown  = ""
	StatusActive   = "active"
	StatusExpiring = "expiring"
	StatusExpired  = "expired"
	StatusRevoked  = "revoked"
)

// noKeyMessage is lcm's InvalidArgument message for a certificate without a
// stored key (go-tangra-lcm grpcapi.grpcError, issue.ErrNoStoredKey, since
// the lcm 033 change). Older lcm releases answer Unavailable
// "temporarily_unavailable" instead, which surfaces here as ErrUnavailable
// (the agent retries until the item expires).
const noKeyMessage = "no stored private key"

// Bundle is a downloaded certificate. KeyPEM is a byte slice so the caller
// can zero it (Wipe) once the bundle has been relayed.
type Bundle struct {
	CertificateID     string
	Serial            string // lowercase
	CommonName        string // lcm subject
	SANs              []string
	FingerprintSHA256 string
	NotBefore         time.Time
	NotAfter          time.Time
	Status            string
	CertPEM           string
	ChainPEM          string
	KeyPEM            []byte
}

// HasKey reports whether the bundle carries a private key.
func (b *Bundle) HasKey() bool { return len(b.KeyPEM) > 0 }

// Wipe zeroes the private key bytes held by the bundle.
func (b *Bundle) Wipe() {
	clear(b.KeyPEM)
	b.KeyPEM = b.KeyPEM[:0]
}

// Client downloads certificates from lcm over the module gRPC channel.
type Client struct {
	cc      lcmv1.CertificatesClient
	timeout time.Duration
}

// New builds a client from a connected lcm.v1.Certificates client; timeout
// (> 0) bounds every call (cert_delivery.lcm_timeout_seconds).
func New(cc lcmv1.CertificatesClient, timeout time.Duration) *Client {
	return &Client{cc: cc, timeout: timeout}
}

// Dialer resolves a mesh peer (the Freya app's pooled module client).
type Dialer interface {
	Client(ctx context.Context, service string) (*grpc.ClientConn, error)
}

// Dial builds a client to the lcm module named service (cert_delivery.
// lcm_service) over the SPIFFE mTLS mesh.
func Dial(ctx context.Context, d Dialer, service string, timeout time.Duration) (*Client, error) {
	conn, err := d.Client(ctx, service)
	if err != nil {
		return nil, fmt.Errorf("lcmclient: dial %s: %w", service, err)
	}
	return New(lcmv1.NewCertificatesClient(conn), timeout), nil
}

// Download fetches certificate certID of tenantID; includeKey asks for the
// retained private key. A revoked or expired certificate returns its
// metadata without material together with ErrRevoked or ErrExpired.
func (c *Client) Download(ctx context.Context, tenantID, certID string, includeKey bool) (Bundle, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	resp, err := c.cc.Download(ctx, &lcmv1.DownloadRequest{TenantId: tenantID, CertificateId: certID, IncludeKey: includeKey})
	if err != nil {
		return Bundle{}, mapErr(err)
	}
	meta := resp.GetCertificate()
	b := Bundle{
		CertificateID: certID, Serial: strings.ToLower(meta.GetSerial()), CommonName: meta.GetSubject(),
		SANs: append([]string(nil), meta.GetSans()...), FingerprintSHA256: strings.ToLower(meta.GetFingerprintSha256()),
		Status: statusOf(meta.GetStatus()),
	}
	if ts := meta.GetNotBefore(); ts != nil {
		b.NotBefore = ts.AsTime()
	}
	if ts := meta.GetNotAfter(); ts != nil {
		b.NotAfter = ts.AsTime()
	}
	switch b.Status {
	case StatusRevoked:
		return b, ErrRevoked
	case StatusExpired:
		return b, ErrExpired
	}
	b.CertPEM, b.ChainPEM = resp.GetCertPem(), resp.GetChainPem()
	if k := resp.GetKeyPem(); k != "" {
		b.KeyPEM = []byte(k)
	}
	return b, nil
}

func statusOf(s lcmv1.CertificateStatus) string {
	switch s {
	case lcmv1.CertificateStatus_CERTIFICATE_STATUS_ACTIVE:
		return StatusActive
	case lcmv1.CertificateStatus_CERTIFICATE_STATUS_EXPIRING:
		return StatusExpiring
	case lcmv1.CertificateStatus_CERTIFICATE_STATUS_EXPIRED:
		return StatusExpired
	case lcmv1.CertificateStatus_CERTIFICATE_STATUS_REVOKED:
		return StatusRevoked
	}
	return StatusUnknown
}

// mapErr maps a Download failure to the closed set; anything else keeps
// only the gRPC code (the remote message is never propagated).
func mapErr(err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return ErrUnavailable
	}
	st := status.Convert(err)
	switch st.Code() {
	case codes.NotFound:
		return ErrNotFound
	case codes.Unavailable, codes.DeadlineExceeded:
		return ErrUnavailable
	case codes.InvalidArgument:
		if strings.Contains(st.Message(), noKeyMessage) {
			return ErrNoKey
		}
	}
	return fmt.Errorf("lcmclient: download failed: %s", st.Code())
}
