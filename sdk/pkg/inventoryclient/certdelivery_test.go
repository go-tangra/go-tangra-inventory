package inventoryclient_test

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/sdk/v4/pkg/inventoryclient"
)

// fakeDelivery is an in-process CertificateDeliveryService recording what it
// was asked and answering with canned responses.
type fakeDelivery struct {
	invv1.UnimplementedCertificateDeliveryServiceServer
	lastCreate  *invv1.CreateCertificateDeliveryRequest
	lastGet     *invv1.GetCertificateDeliveryRequest
	lastPreview *invv1.PreviewCertificateTargetsRequest
	lastVerify  *invv1.VerifyHostCertificatesRequest
	lastRevoke  *invv1.MarkCertificateRevokedRequest
	delivery    *invv1.CertificateDelivery
	fail        error
}

func (f *fakeDelivery) CreateCertificateDelivery(_ context.Context, req *invv1.CreateCertificateDeliveryRequest) (*invv1.CertificateDelivery, error) {
	f.lastCreate = req
	if f.fail != nil {
		return nil, f.fail
	}
	return f.delivery, nil
}

func (f *fakeDelivery) GetCertificateDelivery(_ context.Context, req *invv1.GetCertificateDeliveryRequest) (*invv1.CertificateDelivery, error) {
	f.lastGet = req
	if f.fail != nil {
		return nil, f.fail
	}
	return f.delivery, nil
}

func (f *fakeDelivery) PreviewCertificateTargets(_ context.Context, req *invv1.PreviewCertificateTargetsRequest) (*invv1.PreviewCertificateTargetsResponse, error) {
	f.lastPreview = req
	if f.fail != nil {
		return nil, f.fail
	}
	return &invv1.PreviewCertificateTargetsResponse{
		Hosts: []*invv1.TargetHost{
			{HostId: "h-1", Hostname: "web1", OsName: "linux", Tags: map[string]string{"role": "web"}, AgentOnline: true, Capability: "enabled"},
			{HostId: "h-2", Hostname: "win1", OsName: "windows", Capability: "not_supported_platform"},
		},
		UnknownHostIds: []string{"h-x"},
		Truncated:      true,
	}, nil
}

func (f *fakeDelivery) VerifyHostCertificates(_ context.Context, req *invv1.VerifyHostCertificatesRequest) (*invv1.VerifyHostCertificatesResponse, error) {
	f.lastVerify = req
	if f.fail != nil {
		return nil, f.fail
	}
	return &invv1.VerifyHostCertificatesResponse{
		Hosts: []*invv1.HostCertificateStatus{
			{HostId: "h-1", Hostname: "web1", Status: "match", FingerprintSha256: "ab12", Serial: "01", Reason: "", LastDeliveredAt: 1_700_000_000},
			{HostId: "h-2", Hostname: "web2", Status: "missing", Reason: "never_delivered"},
		},
		Matched: 1,
		Total:   2,
	}, nil
}

func (f *fakeDelivery) MarkCertificateRevoked(_ context.Context, req *invv1.MarkCertificateRevokedRequest) (*invv1.MarkCertificateRevokedResponse, error) {
	f.lastRevoke = req
	if f.fail != nil {
		return nil, f.fail
	}
	return &invv1.MarkCertificateRevokedResponse{CancelledItems: 3, FlaggedHosts: 2}, nil
}

func dialDelivery(t *testing.T, f invv1.CertificateDeliveryServiceServer) *inventoryclient.Client {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	invv1.RegisterCertificateDeliveryServiceServer(gs, f)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return inventoryclient.New(conn)
}

func cannedDelivery() *invv1.CertificateDelivery {
	return &invv1.CertificateDelivery{
		Id: "d-1", TenantId: "t-1", CertificateId: "c-1", Name: "web", KeyPolicy: "require",
		CreatedAt: 1_700_000_000, ExpiresAt: 1_700_086_400,
		Items: []*invv1.CertificateDeliveryItem{{
			Id: "i-1", HostId: "h-1", Hostname: "web1", AgentOnline: true, State: invv1.DeliveryState_DELIVERY_STATE_HOOK_FAILED,
			Reason: "hook_exit_nonzero", Serial: "01", FingerprintSha256: "ab12", HookExitCode: 2, Attempts: 3, UpdatedAt: 1_700_000_100,
		}, {
			Id: "i-2", HostId: "h-2", Hostname: "web2", State: invv1.DeliveryState_DELIVERY_STATE_PENDING, HookExitCode: -1,
		}},
		UnknownHostIds: []string{"h-x"},
		Created:        true,
	}
}

func wantDelivery() inventoryclient.Delivery {
	return inventoryclient.Delivery{
		ID: "d-1", TenantID: "t-1", CertificateID: "c-1", Name: "web", KeyPolicy: "require", Created: true,
		CreatedAt: time.Unix(1_700_000_000, 0).UTC(), ExpiresAt: time.Unix(1_700_086_400, 0).UTC(),
		Items: []inventoryclient.DeliveryItem{{
			ID: "i-1", HostID: "h-1", Hostname: "web1", State: "hook_failed", Reason: "hook_exit_nonzero",
			Serial: "01", Fingerprint: "ab12", AgentOnline: true, HookExitCode: 2, Attempts: 3,
			UpdatedAt: time.Unix(1_700_000_100, 0).UTC(),
		}, {
			ID: "i-2", HostID: "h-2", Hostname: "web2", State: "pending", HookExitCode: -1,
		}},
		UnknownHostIDs: []string{"h-x"},
	}
}

func TestCreateCertificateDelivery(t *testing.T) {
	f := &fakeDelivery{delivery: cannedDelivery()}
	c := dialDelivery(t, f)
	got, err := c.CreateCertificateDelivery(context.Background(), inventoryclient.DeliveryRequest{
		TenantID: "t-1", IdempotencyKey: "job-1", ConfigurationID: "cfg-1", TargetID: "tgt-1", Trigger: "manual",
		CertificateID: "c-1", Name: "web", KeyPolicy: "require",
		HostIDs: []string{"h-1", "h-2"}, HostTags: []string{"role=web"}, RearmFailed: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantReq := &invv1.CreateCertificateDeliveryRequest{
		TenantId: "t-1", IdempotencyKey: "job-1", ConfigurationId: "cfg-1", TargetId: "tgt-1", Trigger: "manual",
		CertificateId: "c-1", Name: "web", KeyPolicy: "require",
		Selector:    &invv1.HostSelector{HostIds: []string{"h-1", "h-2"}, HostTags: []string{"role=web"}},
		RearmFailed: true,
	}
	if !proto.Equal(f.lastCreate, wantReq) {
		t.Fatalf("request = %v, want %v", f.lastCreate, wantReq)
	}
	if want := wantDelivery(); !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery = %+v\nwant %+v", got, want)
	}
}

func TestGetCertificateDelivery(t *testing.T) {
	f := &fakeDelivery{delivery: cannedDelivery()}
	c := dialDelivery(t, f)
	got, err := c.GetCertificateDelivery(context.Background(), "t-1", "d-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastGet.GetTenantId() != "t-1" || f.lastGet.GetId() != "d-1" {
		t.Fatalf("request = %v", f.lastGet)
	}
	if want := wantDelivery(); !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery = %+v\nwant %+v", got, want)
	}
}

func TestGetCertificateDeliveryZeroValues(t *testing.T) {
	f := &fakeDelivery{delivery: &invv1.CertificateDelivery{Id: "d-2", Items: []*invv1.CertificateDeliveryItem{{Id: "i-1"}}}}
	c := dialDelivery(t, f)
	got, err := c.GetCertificateDelivery(context.Background(), "t-1", "d-2")
	if err != nil {
		t.Fatal(err)
	}
	if got.Created || !got.CreatedAt.IsZero() || !got.ExpiresAt.IsZero() || got.UnknownHostIDs != nil {
		t.Fatalf("delivery = %+v", got)
	}
	if it := got.Items[0]; it.State != "" || !it.UpdatedAt.IsZero() || it.HookExitCode != 0 {
		t.Fatalf("item = %+v", it)
	}
}

func TestDeliveryStateMapping(t *testing.T) {
	cases := map[invv1.DeliveryState]string{
		invv1.DeliveryState_DELIVERY_STATE_UNSPECIFIED: "",
		invv1.DeliveryState_DELIVERY_STATE_PENDING:     "pending",
		invv1.DeliveryState_DELIVERY_STATE_DELIVERED:   "delivered",
		invv1.DeliveryState_DELIVERY_STATE_FETCHED:     "fetched",
		invv1.DeliveryState_DELIVERY_STATE_INSTALLED:   "installed",
		invv1.DeliveryState_DELIVERY_STATE_UNCHANGED:   "unchanged",
		invv1.DeliveryState_DELIVERY_STATE_FAILED:      "failed",
		invv1.DeliveryState_DELIVERY_STATE_HOOK_FAILED: "hook_failed",
		invv1.DeliveryState_DELIVERY_STATE_UNSUPPORTED: "unsupported",
		invv1.DeliveryState_DELIVERY_STATE_SUPERSEDED:  "superseded",
		invv1.DeliveryState_DELIVERY_STATE_EXPIRED:     "expired",
		invv1.DeliveryState_DELIVERY_STATE_CANCELLED:   "cancelled",
		invv1.DeliveryState(99):                        "", // unknown to this SDK
	}
	if len(cases) != len(invv1.DeliveryState_name)+1 {
		t.Fatalf("mapping test misses enum values: %d cases, %d enum values", len(cases), len(invv1.DeliveryState_name))
	}
	d := &invv1.CertificateDelivery{Id: "d-1"}
	order := make([]invv1.DeliveryState, 0, len(cases))
	for s := range cases {
		d.Items = append(d.Items, &invv1.CertificateDeliveryItem{Id: s.String(), State: s})
		order = append(order, s)
	}
	c := dialDelivery(t, &fakeDelivery{delivery: d})
	got, err := c.GetCertificateDelivery(context.Background(), "t-1", "d-1")
	if err != nil {
		t.Fatal(err)
	}
	for i, s := range order {
		if got.Items[i].State != cases[s] {
			t.Errorf("%v -> %q, want %q", s, got.Items[i].State, cases[s])
		}
	}
}

func TestPreviewCertificateTargets(t *testing.T) {
	f := &fakeDelivery{}
	c := dialDelivery(t, f)
	got, err := c.PreviewCertificateTargets(context.Background(), "t-1", []string{"h-1", "h-x"}, []string{"role"})
	if err != nil {
		t.Fatal(err)
	}
	wantReq := &invv1.PreviewCertificateTargetsRequest{
		TenantId: "t-1", Selector: &invv1.HostSelector{HostIds: []string{"h-1", "h-x"}, HostTags: []string{"role"}},
	}
	if !proto.Equal(f.lastPreview, wantReq) {
		t.Fatalf("request = %v", f.lastPreview)
	}
	want := inventoryclient.Preview{
		Hosts: []inventoryclient.TargetHost{
			{HostID: "h-1", Hostname: "web1", OSName: "linux", Tags: map[string]string{"role": "web"}, AgentOnline: true, Capability: "enabled"},
			{HostID: "h-2", Hostname: "win1", OSName: "windows", Capability: "not_supported_platform"},
		},
		UnknownHostIDs: []string{"h-x"},
		Truncated:      true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("preview = %+v\nwant %+v", got, want)
	}
}

func TestVerifyHostCertificates(t *testing.T) {
	f := &fakeDelivery{}
	c := dialDelivery(t, f)
	got, err := c.VerifyHostCertificates(context.Background(), "t-1", []string{"h-1"}, []string{"env=prod"}, "web", "ab12")
	if err != nil {
		t.Fatal(err)
	}
	wantReq := &invv1.VerifyHostCertificatesRequest{
		TenantId: "t-1", Selector: &invv1.HostSelector{HostIds: []string{"h-1"}, HostTags: []string{"env=prod"}},
		Name: "web", ExpectedFingerprintSha256: "ab12",
	}
	if !proto.Equal(f.lastVerify, wantReq) {
		t.Fatalf("request = %v", f.lastVerify)
	}
	want := inventoryclient.Verification{
		Hosts: []inventoryclient.HostCertificateStatus{
			{HostID: "h-1", Hostname: "web1", Status: "match", Fingerprint: "ab12", Serial: "01", LastDeliveredAt: time.Unix(1_700_000_000, 0).UTC()},
			{HostID: "h-2", Hostname: "web2", Status: "missing", Reason: "never_delivered"},
		},
		Matched: 1,
		Total:   2,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("verification = %+v\nwant %+v", got, want)
	}
}

func TestMarkCertificateRevoked(t *testing.T) {
	f := &fakeDelivery{}
	c := dialDelivery(t, f)
	cancelled, flagged, err := c.MarkCertificateRevoked(context.Background(), "t-1", "c-1")
	if err != nil {
		t.Fatal(err)
	}
	if f.lastRevoke.GetTenantId() != "t-1" || f.lastRevoke.GetCertificateId() != "c-1" {
		t.Fatalf("request = %v", f.lastRevoke)
	}
	if cancelled != 3 || flagged != 2 {
		t.Fatalf("cancelled=%d flagged=%d", cancelled, flagged)
	}
}

func TestCertificateDeliveryErrorsPassThrough(t *testing.T) {
	f := &fakeDelivery{fail: status.Error(codes.FailedPrecondition, "certificate delivery is disabled")}
	c := dialDelivery(t, f)
	ctx := context.Background()
	check := func(name string, err error) {
		t.Helper()
		if status.Code(err) != codes.FailedPrecondition || status.Convert(err).Message() != "certificate delivery is disabled" {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	d, err := c.CreateCertificateDelivery(ctx, inventoryclient.DeliveryRequest{TenantID: "t-1"})
	check("create", err)
	if !reflect.DeepEqual(d, inventoryclient.Delivery{}) {
		t.Errorf("create returned %+v on error", d)
	}
	d, err = c.GetCertificateDelivery(ctx, "t-1", "d-1")
	check("get", err)
	if !reflect.DeepEqual(d, inventoryclient.Delivery{}) {
		t.Errorf("get returned %+v on error", d)
	}
	p, err := c.PreviewCertificateTargets(ctx, "t-1", nil, nil)
	check("preview", err)
	if !reflect.DeepEqual(p, inventoryclient.Preview{}) {
		t.Errorf("preview returned %+v on error", p)
	}
	v, err := c.VerifyHostCertificates(ctx, "t-1", nil, nil, "web", "ab")
	check("verify", err)
	if !reflect.DeepEqual(v, inventoryclient.Verification{}) {
		t.Errorf("verify returned %+v on error", v)
	}
	n, m, err := c.MarkCertificateRevoked(ctx, "t-1", "c-1")
	check("revoke", err)
	if n != 0 || m != 0 {
		t.Errorf("revoke returned %d, %d on error", n, m)
	}
}
