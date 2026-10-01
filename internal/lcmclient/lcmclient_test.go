package lcmclient

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	lcmv1 "github.com/go-tangra/go-tangra-lcm/sdk/v4/api/proto/lcm/v1"
)

const keyPEM = "-----BEGIN PRIVATE KEY-----\nMIGHAgEAMBMGByqGSM49AgEGCCqGSM49AwEHBG0wawIBAQQg\n-----END PRIVATE KEY-----\n"

// fakeCerts is a fake lcm.v1.Certificates client recording the request.
type fakeCerts struct {
	req      *lcmv1.DownloadRequest
	deadline bool
	resp     *lcmv1.CertificateBundle
	err      error
}

func (f *fakeCerts) Download(ctx context.Context, in *lcmv1.DownloadRequest, _ ...grpc.CallOption) (*lcmv1.CertificateBundle, error) {
	f.req = in
	_, f.deadline = ctx.Deadline()
	return f.resp, f.err
}

func bundle(st lcmv1.CertificateStatus, withKey bool) *lcmv1.CertificateBundle {
	b := &lcmv1.CertificateBundle{
		Certificate: &lcmv1.Certificate{Id: "c1", Serial: "4F3A", Subject: "www.example.com", Sans: []string{"www.example.com"},
			NotBefore: timestamppb.New(time.Unix(1000, 0)), NotAfter: timestamppb.New(time.Unix(2000, 0)),
			FingerprintSha256: "9c1d", Status: st},
		CertPem: "-----BEGIN CERTIFICATE-----\nA\n-----END CERTIFICATE-----\n", ChainPem: "chain",
	}
	if withKey {
		b.KeyPem = keyPEM
	}
	return b
}

func TestDownloadPassesThroughAndMaps(t *testing.T) {
	for _, include := range []bool{true, false} {
		f := &fakeCerts{resp: bundle(lcmv1.CertificateStatus_CERTIFICATE_STATUS_ACTIVE, include)}
		c := New(f, 5*time.Second)
		b, err := c.Download(context.Background(), "t1", "c1", include)
		if err != nil {
			t.Fatal(err)
		}
		if f.req.GetTenantId() != "t1" || f.req.GetCertificateId() != "c1" || f.req.GetIncludeKey() != include {
			t.Fatalf("request = %+v", f.req)
		}
		if !f.deadline {
			t.Fatal("no deadline applied")
		}
		if b.CertificateID != "c1" || b.Serial != "4f3a" || b.CommonName != "www.example.com" || len(b.SANs) != 1 ||
			b.FingerprintSHA256 != "9c1d" || b.Status != StatusActive || b.NotBefore.Unix() != 1000 || b.NotAfter.Unix() != 2000 ||
			!strings.HasPrefix(b.CertPEM, "-----BEGIN CERTIFICATE") || b.ChainPEM != "chain" || b.HasKey() != include {
			t.Fatalf("bundle = %+v", b)
		}
		if include {
			if string(b.KeyPEM) != keyPEM {
				t.Fatal("key not relayed")
			}
			b.Wipe()
			for _, x := range b.KeyPEM {
				if x != 0 {
					t.Fatal("key not zeroed")
				}
			}
			if b.HasKey() {
				t.Fatal("wiped bundle still has a key")
			}
		}
	}
}

func TestDownloadStatuses(t *testing.T) {
	for st, want := range map[lcmv1.CertificateStatus]string{
		lcmv1.CertificateStatus_CERTIFICATE_STATUS_EXPIRING:    StatusExpiring,
		lcmv1.CertificateStatus_CERTIFICATE_STATUS_UNSPECIFIED: StatusUnknown,
	} {
		b, err := New(&fakeCerts{resp: bundle(st, true)}, 0).Download(context.Background(), "t1", "c1", true)
		if err != nil || b.Status != want || !b.HasKey() {
			t.Fatalf("%v: %+v %v", st, b.Status, err)
		}
	}
	// Revoked and expired certificates are surfaced as errors; their
	// metadata is returned without material.
	for st, wantErr := range map[lcmv1.CertificateStatus]error{
		lcmv1.CertificateStatus_CERTIFICATE_STATUS_REVOKED: ErrRevoked,
		lcmv1.CertificateStatus_CERTIFICATE_STATUS_EXPIRED: ErrExpired,
	} {
		b, err := New(&fakeCerts{resp: bundle(st, true)}, time.Second).Download(context.Background(), "t1", "c1", true)
		if !errors.Is(err, wantErr) {
			t.Fatalf("%v: %v", st, err)
		}
		if b.HasKey() || b.CertPEM != "" || b.ChainPEM != "" || b.CertificateID != "c1" || b.NotAfter.Unix() != 2000 {
			t.Fatalf("%v: material or metadata wrong: %+v", st, b)
		}
	}
	// A bundle without certificate metadata.
	b, err := New(&fakeCerts{resp: &lcmv1.CertificateBundle{CertPem: "x"}}, 0).Download(context.Background(), "t1", "c1", false)
	if err != nil || b.Status != StatusUnknown || !b.NotAfter.IsZero() || b.CertificateID != "c1" {
		t.Fatalf("empty metadata: %+v %v", b, err)
	}
}

func TestDownloadErrors(t *testing.T) {
	for name, c := range map[string]struct {
		err  error
		want error
	}{
		"not found":   {status.Error(codes.NotFound, "certificate not found"), ErrNotFound},
		"no key":      {status.Error(codes.InvalidArgument, "key: no stored private key for this certificate"), ErrNoKey},
		"unavailable": {status.Error(codes.Unavailable, "connection refused"), ErrUnavailable},
		"deadline":    {status.Error(codes.DeadlineExceeded, "deadline"), ErrUnavailable},
		"ctx":         {context.DeadlineExceeded, ErrUnavailable},
	} {
		_, err := New(&fakeCerts{err: c.err}, time.Second).Download(context.Background(), "t1", "c1", true)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Other failures carry the gRPC code only, never the remote message.
	leak := status.Error(codes.PermissionDenied, "denied "+keyPEM)
	_, err := New(&fakeCerts{err: leak}, time.Second).Download(context.Background(), "t1", "c1", true)
	if err == nil || errors.Is(err, ErrNotFound) || errors.Is(err, ErrUnavailable) || errors.Is(err, ErrNoKey) {
		t.Fatalf("permission denied: %v", err)
	}
	if strings.Contains(err.Error(), "BEGIN") || strings.Contains(err.Error(), "denied ") || !strings.Contains(err.Error(), "PermissionDenied") {
		t.Fatalf("error leaks the remote message: %q", err)
	}
	other := status.Error(codes.InvalidArgument, "certificate_id: bad")
	if _, err := New(&fakeCerts{err: other}, time.Second).Download(context.Background(), "t1", "c1", true); errors.Is(err, ErrNoKey) || err == nil {
		t.Fatalf("other invalid argument: %v", err)
	}
}

type fakeDialer struct {
	service string
	err     error
}

func (d *fakeDialer) Client(_ context.Context, service string) (*grpc.ClientConn, error) {
	d.service = service
	if d.err != nil {
		return nil, d.err
	}
	return grpc.NewClient("passthrough:///lcm", grpc.WithTransportCredentials(insecure.NewCredentials()))
}

func TestDial(t *testing.T) {
	d := &fakeDialer{}
	c, err := Dial(context.Background(), d, "lcm", 3*time.Second)
	if err != nil || c == nil || d.service != "lcm" || c.timeout != 3*time.Second {
		t.Fatalf("dial: %v %v", c, err)
	}
	if _, err := Dial(context.Background(), &fakeDialer{err: errors.New("no peer")}, "lcm", time.Second); err == nil || !strings.Contains(err.Error(), "lcm") {
		t.Fatalf("dial error: %v", err)
	}
}
