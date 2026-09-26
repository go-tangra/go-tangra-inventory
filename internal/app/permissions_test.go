package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/protobuf/proto"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
)

// fakeAuth answers Authorization/RegisterPermissions in memory, failing the
// first `fail` calls.
type fakeAuth struct {
	mu    sync.Mutex
	fail  int
	calls int
	got   *authv1.RegisterPermissionsRequest
}

func (f *fakeAuth) Invoke(_ context.Context, method string, args, _ any, _ ...grpc.CallOption) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if method != authv1.Authorization_RegisterPermissions_FullMethodName {
		return errors.New("unexpected method " + method)
	}
	if f.calls <= f.fail {
		return errors.New("unavailable")
	}
	f.got = proto.Clone(args.(*authv1.RegisterPermissionsRequest)).(*authv1.RegisterPermissionsRequest)
	return nil
}

func (f *fakeAuth) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("no streams")
}

func (f *fakeAuth) state() (int, *authv1.RegisterPermissionsRequest) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls, f.got
}

// The loop retries until auth accepts the registration, then re-registers
// periodically; the request carries the module identity, its roles and the
// built-in grants.
func TestRegisterLoop(t *testing.T) {
	auth := &fakeAuth{fail: 2}
	dials := 0
	dial := func(context.Context) (grpc.ClientConnInterface, error) {
		dials++
		if dials == 1 {
			return nil, errors.New("auth not resolvable yet")
		}
		return auth, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		registerLoop(ctx, nil, dial, time.Millisecond, 5*time.Millisecond)
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		calls, got := auth.state()
		if got != nil && calls >= 5 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("calls=%d registered=%v", calls, got != nil)
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done

	_, req := auth.state()
	if req.GetModule() != "inventory" || req.GetModuleDisplayName() != "Inventory" || !req.GetDeclaresRoles() {
		t.Fatalf("%v", req)
	}
	roles := map[string]bool{}
	for _, r := range req.GetRoles() {
		roles[r.GetSlug()] = len(r.GetPermissions()) > 0
	}
	if len(roles) != 3 || !roles["administrator"] || !roles["editor"] || !roles["viewer"] {
		t.Fatalf("roles %v", req.GetRoles())
	}
	grants := map[string]int{}
	for _, g := range req.GetBuiltinGrants() {
		grants[g.GetRole()] = len(g.GetPermissions())
	}
	for _, slug := range []string{"owner", "admin", "member", "auditor", "operator"} {
		if grants[slug] == 0 {
			t.Fatalf("grant %s missing: %v", slug, req.GetBuiltinGrants())
		}
	}
}
