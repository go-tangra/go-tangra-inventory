package autoenroll

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	proof "github.com/go-tangra/go-tangra-inventory/sdk/v4/pkg/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const (
	tA = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	tB = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
)

var (
	admin = Actor{Kind: audit.ActorUser, ID: "u1"}
	ident = store.Identity{HardwareUUID: "hw-1", MachineID: "m-1", Hostname: "web01"}
	lan   = netip.MustParseAddr("10.1.2.3")
)

type fx struct {
	s      *Service
	m      *memstore.Mem
	env    *sealed.Envelope
	now    time.Time
	secret string
}

func newFx(t *testing.T) *fx {
	t.Helper()
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := &fx{m: memstore.New(), env: env, now: time.Date(2026, 9, 30, 10, 0, 0, 0, time.UTC)}
	f.s = New(f.m, env)
	f.s.SetClock(func() time.Time { return f.now })
	f.s.SetClock(nil)
	return f
}

func (f *fx) key(t *testing.T, in KeyInput) (store.AutoEnrollKey, string) {
	t.Helper()
	if in.Name == "" {
		in.Name = "servers"
	}
	if in.AllowedCIDRs == nil {
		in.AllowedCIDRs = []string{"10.0.0.0/8"}
	}
	k, secret, err := f.s.CreateKey(context.Background(), tA, admin, in)
	if err != nil {
		t.Fatal(err)
	}
	return k, secret
}

func (f *fx) proof(t *testing.T, k store.AutoEnrollKey, secret string, at time.Time) *inventoryv1.AutoEnrollProof {
	t.Helper()
	p, err := proof.NewProof(secret, k.KeyID, proof.Identity{HardwareUUID: ident.HardwareUUID, MachineID: ident.MachineID, Hostname: ident.Hostname}, at)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (f *fx) on(t *testing.T, tenant string, v bool) {
	t.Helper()
	if _, err := f.s.SetEnabled(context.Background(), tenant, admin, v); err != nil {
		t.Fatal(err)
	}
}

func (f *fx) lastAudit(t *testing.T) store.AuditRow {
	t.Helper()
	rows := f.m.AuditRows()
	if len(rows) == 0 {
		t.Fatal("no audit")
	}
	return rows[len(rows)-1]
}

func TestEnrollAccepted(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	k, secret := f.key(t, KeyInput{MaxEnrollments: 2})
	if !strings.HasPrefix(secret, "aks_") || len(secret) != 47 || !proof.ValidKeyID(k.KeyID) || k.SecretSealed != nil || !k.Enabled {
		t.Fatalf("key %+v secret %q", k, secret)
	}
	f.on(t, tA, true)
	id, cred, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now.Add(-time.Minute)), ident, "4.4.0", netip.MustParseAddr("::ffff:10.1.2.3"))
	if err != nil || id == "" || cred == "" {
		t.Fatalf("enroll %v", err)
	}
	a, err := f.m.GetAgent(ctx, tA, id)
	if err != nil || a.EnrolledVia != store.EnrolledViaAuto || a.AutoEnrollKeyID != k.KeyID || a.IdentityHint != "web01" || a.AgentVersion != "4.4.0" {
		t.Fatalf("agent %+v %v", a, err)
	}
	// The issued credential verifies like a token-enrolled one.
	if _, err := enroll.New(f.m, f.env).Verify(ctx, id, cred); err != nil {
		t.Fatal("credential does not verify")
	}
	keys, _ := f.s.Keys(ctx, tA)
	if keys[0].Enrollments != 1 || keys[0].LastUsedIP != "10.1.2.3" || keys[0].SecretSealed != nil {
		t.Fatalf("key after %+v", keys[0])
	}
	if r := f.lastAudit(t); r.Action != string(audit.AgentAutoEnrolled) || r.Detail["key_id"] != k.KeyID || r.Detail["address"] != "10.1.2.3" {
		t.Fatalf("audit %+v", r)
	}
}

func TestEnrollRefusals(t *testing.T) {
	ctx := context.Background()
	type tc struct {
		name   string
		reason string // "" = not audited
		setup  func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr)
	}
	cases := []tc{
		{"tenant off", ReasonTenantDisabled, func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			f.on(t, tA, false)
			return nil, ident, lan
		}},
		{"key disabled", ReasonKeyDisabled, func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			off := false
			if _, err := f.s.UpdateKey(ctx, tA, admin, k.ID, KeyPatch{Enabled: &off}); err != nil {
				t.Fatal(err)
			}
			return nil, ident, lan
		}},
		{"expired", ReasonKeyExpired, func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			exp := f.now.Add(time.Hour)
			if _, err := f.s.UpdateKey(ctx, tA, admin, k.ID, KeyPatch{ExpiresAt: &exp}); err != nil {
				t.Fatal(err)
			}
			f.now = f.now.Add(2 * time.Hour)
			return nil, ident, lan
		}},
		{"outside networks", ReasonAddress, func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			return nil, ident, netip.MustParseAddr("192.168.1.1")
		}},
		{"other identity", ReasonSignature, func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			i := ident
			i.Hostname = "db01"
			return nil, i, lan
		}},
		{"stale", ReasonStale, func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			return f.proof(t, k, f.secret, f.now.Add(-10*time.Minute)), ident, lan
		}},
		{"future", ReasonStale, func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			return f.proof(t, k, f.secret, f.now.Add(10*time.Minute)), ident, lan
		}},
		{"unknown key", "", func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			return &inventoryv1.AutoEnrollProof{KeyId: "ak_000000000000000000000000", Timestamp: f.now.Unix(), Nonce: "nonce-nonce-nonce-1", Signature: "x"}, ident, lan
		}},
		{"malformed", "", func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			return &inventoryv1.AutoEnrollProof{KeyId: k.KeyID, Nonce: "bad"}, ident, lan
		}},
		{"no peer", "", func(f *fx, k store.AutoEnrollKey) (*inventoryv1.AutoEnrollProof, store.Identity, netip.Addr) {
			return nil, ident, netip.Addr{}
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newFx(t)
			k, secret := f.key(t, KeyInput{})
			f.secret = secret
			f.on(t, tA, true)
			p, id, peer := c.setup(f, k)
			if p == nil {
				p = f.proof(t, k, secret, f.now)
			}
			before := f.m.AuditRows()
			if _, _, err := f.s.Enroll(ctx, p, id, "4.4.0", peer); !errors.Is(err, ErrRejected) {
				t.Fatalf("err %v", err)
			}
			after := f.m.AuditRows()
			if c.reason == "" {
				if len(after) != len(before) {
					t.Fatal("unexpected audit")
				}
				return
			}
			r := f.lastAudit(t)
			if r.Action != string(audit.AutoEnrollRefused) || r.Reason != c.reason || r.Outcome != audit.OutcomeRefused {
				t.Fatalf("audit %+v", r)
			}
		})
	}
}

func TestReplayLimitAndThrottle(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	k, secret := f.key(t, KeyInput{MaxEnrollments: 2})
	f.on(t, tA, true)
	p := f.proof(t, k, secret, f.now)
	if _, _, err := f.s.Enroll(ctx, p, ident, "", lan); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Enroll(ctx, p, ident, "", lan); !errors.Is(err, ErrRejected) || f.lastAudit(t).Reason != ReasonReplay {
		t.Fatalf("replay: %v %+v", err, f.lastAudit(t))
	}
	if _, _, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan); !errors.Is(err, ErrRejected) || f.lastAudit(t).Reason != ReasonKeyExhausted {
		t.Fatalf("limit: %v", err)
	}
	// Repeated refusals of one key and reason are audited once per 10 s.
	before := f.m.AuditRows()
	for i := 0; i < 5; i++ {
		_, _, _ = f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan)
	}
	after := f.m.AuditRows()
	if len(after) != len(before) {
		t.Fatalf("throttle: %d new rows", len(after)-len(before))
	}
	f.now = f.now.Add(11 * time.Second)
	_, _, _ = f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan)
	if after2 := f.m.AuditRows(); len(after2) != len(after)+1 {
		t.Fatal("audit after the quiet period")
	}
	// Stale nonces are pruned: an old nonce is not held forever (the
	// timestamp check refuses its reuse instead).
	f.s.quiet = map[string]time.Time{}
	for i := 0; i < 10_001; i++ {
		f.s.quiet[string(rune(i))] = f.now
	}
	f.now = f.now.Add(time.Minute)
	_, _, _ = f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan)
	if len(f.s.quiet) != 1 {
		t.Fatalf("quiet map not reset: %d", len(f.s.quiet))
	}
}

// A key of tenant A enrolls into tenant A only, whatever tenant B does.
func TestTenantBinding(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	k, secret := f.key(t, KeyInput{})
	f.on(t, tB, true) // only B on
	if _, _, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan); !errors.Is(err, ErrRejected) {
		t.Fatal("tenant B's switch enabled tenant A's key")
	}
	f.on(t, tA, true)
	id, _, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.m.GetAgent(ctx, tB, id); err == nil {
		t.Fatal("agent visible in tenant B")
	}
	if keys, _ := f.s.Keys(ctx, tB); len(keys) != 0 {
		t.Fatal("tenant B sees A's keys")
	}
	if _, err := f.s.UpdateKey(ctx, tB, admin, k.ID, KeyPatch{}); !errors.Is(err, ErrNotFound) {
		t.Fatal("tenant B updated A's key")
	}
	if err := f.s.DeleteKey(ctx, tB, admin, k.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("tenant B deleted A's key")
	}
}

func TestKeyLifecycle(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	st, err := f.s.Settings(ctx, tA)
	if err != nil || st.Enabled {
		t.Fatalf("default settings %+v %v", st, err)
	}
	f.on(t, tA, true)
	if st, _ := f.s.Settings(ctx, tA); !st.Enabled || st.UpdatedBy != "u1" {
		t.Fatalf("settings %+v", st)
	}
	k, secret := f.key(t, KeyInput{AllowedCIDRs: []string{"10.0.0.0/8", " 10.0.0.1/8 ", "192.168.5.7"}})
	if strings.Join(k.AllowedCIDRs, ",") != "10.0.0.0/8,192.168.5.7/32" {
		t.Fatalf("cidrs %v", k.AllowedCIDRs)
	}
	if _, _, err := f.s.CreateKey(ctx, tA, admin, KeyInput{Name: "servers", AllowedCIDRs: []string{"10.0.0.0/8"}}); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate name %v", err)
	}
	name, cidrs, max := "office", []string{"172.16.0.0/12"}, 5
	exp := f.now.Add(24 * time.Hour)
	u, err := f.s.UpdateKey(ctx, tA, admin, k.ID, KeyPatch{Name: &name, AllowedCIDRs: &cidrs, MaxEnrollments: &max, ExpiresAt: &exp})
	if err != nil || u.Name != "office" || u.AllowedCIDRs[0] != "172.16.0.0/12" || u.MaxEnrollments != 5 || u.ExpiresAt == nil || u.SecretSealed != nil {
		t.Fatalf("update %+v %v", u, err)
	}
	if u, _ = f.s.UpdateKey(ctx, tA, admin, k.ID, KeyPatch{ClearExpiry: true}); u.ExpiresAt != nil {
		t.Fatal("expiry not cleared")
	}
	r2, secret2, err := f.s.RotateKey(ctx, tA, admin, k.ID)
	if err != nil || secret2 == secret || r2.KeyID != k.KeyID || r2.SecretSealed != nil {
		t.Fatalf("rotate %v", err)
	}
	peer := netip.MustParseAddr("172.16.1.1")
	if _, _, err := f.s.Enroll(ctx, f.proof(t, r2, secret, f.now), ident, "", peer); !errors.Is(err, ErrRejected) {
		t.Fatal("old secret still works")
	}
	if _, _, err := f.s.Enroll(ctx, f.proof(t, r2, secret2, f.now), ident, "", peer); err != nil {
		t.Fatalf("new secret %v", err)
	}
	if r := f.lastAudit(t); r.Detail["key_id"] != k.KeyID {
		t.Fatalf("audit %+v", r)
	}
	if _, _, err := f.s.RotateKey(ctx, tA, admin, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal("rotate missing")
	}
	if err := f.s.DeleteKey(ctx, tA, admin, k.ID); err != nil {
		t.Fatal(err)
	}
	if keys, _ := f.s.Keys(ctx, tA); len(keys) != 0 {
		t.Fatal("key not deleted")
	}
	if _, _, err := f.s.Enroll(ctx, f.proof(t, r2, secret2, f.now), ident, "", peer); !errors.Is(err, ErrRejected) {
		t.Fatal("deleted key works")
	}
	// Audit rows never carry the secret.
	rows := f.m.AuditRows()
	for _, r := range rows {
		for _, v := range r.Detail {
			if s, ok := v.(string); ok && (strings.Contains(s, secret) || strings.Contains(s, secret2)) {
				t.Fatalf("secret in audit: %+v", r)
			}
		}
	}
}

func TestValidation(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	past := f.now.Add(-time.Hour)
	bad := []KeyInput{
		{Name: " ", AllowedCIDRs: []string{"10.0.0.0/8"}},
		{Name: strings.Repeat("x", 101), AllowedCIDRs: []string{"10.0.0.0/8"}},
		{Name: "a\tb", AllowedCIDRs: []string{"10.0.0.0/8"}},
		{Name: "a", AllowedCIDRs: nil},
		{Name: "a", AllowedCIDRs: []string{"0.0.0.0/0"}},
		{Name: "a", AllowedCIDRs: []string{"::/0"}},
		{Name: "a", AllowedCIDRs: []string{"::ffff:0.0.0.0/80"}},
		{Name: "a", AllowedCIDRs: []string{"10.0.0.0/33"}},
		{Name: "a", AllowedCIDRs: []string{"nonsense"}},
		{Name: "a", AllowedCIDRs: []string{"fe80::1%eth0"}},
		{Name: "a", AllowedCIDRs: []string{"10.0.0.0/8"}, ExpiresAt: &past},
		{Name: "a", AllowedCIDRs: []string{"10.0.0.0/8"}, MaxEnrollments: -1},
		{Name: "a", AllowedCIDRs: []string{"10.0.0.0/8"}, MaxEnrollments: MaxEnrollments + 1},
	}
	for i, in := range bad {
		var ie *InvalidError
		if _, _, err := f.s.CreateKey(ctx, tA, admin, in); !errors.As(err, &ie) || ie.Error() == "" {
			t.Errorf("case %d: %v", i, err)
		}
	}
	many := make([]string, MaxCIDRs+1)
	for i := range many {
		many[i] = netip.AddrFrom4([4]byte{10, byte(i), 0, 0}).String() + "/16"
	}
	if _, _, err := f.s.CreateKey(ctx, tA, admin, KeyInput{Name: "a", AllowedCIDRs: many}); err == nil {
		t.Error("too many networks")
	}
	if got, err := NormalizeCIDRs([]string{"::ffff:10.0.0.0/104", "2001:db8::1/32"}); err != nil || strings.Join(got, ",") != "10.0.0.0/8,2001:db8::/32" {
		t.Errorf("normalize %v %v", got, err)
	}
	k, _ := f.key(t, KeyInput{})
	blank, badCIDR, badMax := " ", []string{"x"}, -1
	for i, p := range []KeyPatch{{Name: &blank}, {AllowedCIDRs: &badCIDR}, {MaxEnrollments: &badMax}, {ExpiresAt: &past}} {
		if _, err := f.s.UpdateKey(ctx, tA, admin, k.ID, p); err == nil {
			t.Errorf("patch %d accepted", i)
		}
	}
	f.s.SetWindow(0)
	f.s.SetWindow(time.Minute)
	if f.s.Window() != time.Minute {
		t.Error("window")
	}
}

func TestFailures(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	f := newFx(t)
	k, secret := f.key(t, KeyInput{})
	f.on(t, tA, true)

	for _, m := range []string{"LookupAutoEnrollKey", "EnrollWithAutoKey"} {
		f.m.FailNext(m)
		if _, _, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan); err == nil || errors.Is(err, ErrRejected) {
			t.Errorf("%s: %v", m, err)
		}
	}
	f.m.FailNext("PutAutoEnrollSettings")
	if _, err := f.s.SetEnabled(ctx, tA, admin, true); err == nil {
		t.Error("settings failure swallowed")
	}
	f.m.FailNext("CreateAutoEnrollKey")
	if _, _, err := f.s.CreateKey(ctx, tA, admin, KeyInput{Name: "x", AllowedCIDRs: []string{"10.0.0.0/8"}}); err == nil {
		t.Error("create failure swallowed")
	}
	// Invalid audit input (empty tenant) surfaces as an error.
	if _, err := f.s.SetEnabled(ctx, "", admin, true); err == nil {
		t.Error("empty tenant accepted")
	}
	if _, _, err := f.s.CreateKey(ctx, "", admin, KeyInput{Name: "x", AllowedCIDRs: []string{"10.0.0.0/8"}}); err == nil {
		t.Error("empty tenant key accepted")
	}
	if err := f.s.DeleteKey(ctx, "", admin, k.ID); err == nil {
		t.Error("empty tenant delete accepted")
	}

	defer func() { randRead = cryptoRand; sealFn = defaultSeal }()
	randRead = func([]byte) (int, error) { return 0, boom }
	if _, _, err := f.s.CreateKey(ctx, tA, admin, KeyInput{Name: "y", AllowedCIDRs: []string{"10.0.0.0/8"}}); !errors.Is(err, boom) {
		t.Error("entropy failure")
	}
	if _, _, err := f.s.RotateKey(ctx, tA, admin, k.ID); !errors.Is(err, boom) {
		t.Error("rotate entropy failure")
	}
	randRead = cryptoRand
	sealFn = func(*sealed.Envelope, []byte, []byte) ([]byte, error) { return nil, boom }
	if _, _, err := f.s.CreateKey(ctx, tA, admin, KeyInput{Name: "y", AllowedCIDRs: []string{"10.0.0.0/8"}}); !errors.Is(err, boom) {
		t.Error("seal failure")
	}
	if _, _, err := f.s.RotateKey(ctx, tA, admin, k.ID); !errors.Is(err, boom) {
		t.Error("rotate seal failure")
	}
	sealFn = defaultSeal

	// A sealed secret that no longer opens (other KEK) is a server fault.
	other, _ := sealed.NewEnvelope(append(make([]byte, 31), 1))
	s2 := New(f.m, other)
	s2.SetClock(func() time.Time { return f.now })
	if _, _, err := s2.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan); err == nil || errors.Is(err, ErrRejected) {
		t.Errorf("open failure: %v", err)
	}
}

var (
	cryptoRand  = randRead
	defaultSeal = sealFn
)

// conflictStore reports a key that changed between lookup and enrollment.
type conflictStore struct{ *memstore.Mem }

func (conflictStore) EnrollWithAutoKey(context.Context, store.AutoEnrollment) error {
	return repo.ErrConflict
}

func TestConcurrentChange(t *testing.T) {
	f := newFx(t)
	k, secret := f.key(t, KeyInput{AllowedCIDRs: []string{"", "10.0.0.0/8"}})
	f.on(t, tA, true)
	s := New(conflictStore{f.m}, f.env)
	s.SetClock(func() time.Time { return f.now })
	if _, _, err := s.Enroll(context.Background(), f.proof(t, k, secret, f.now), ident, "", lan); !errors.Is(err, ErrRejected) {
		t.Fatalf("err %v", err)
	}
	if r := f.lastAudit(t); r.Reason != ReasonStateChanged {
		t.Fatalf("audit %+v", r)
	}
}

func TestRateLimit(t *testing.T) {
	f := newFx(t)
	ctx := context.Background()
	k, secret := f.key(t, KeyInput{})
	f.on(t, tA, true)
	for i := 0; i < RatePerMinute; i++ {
		if _, _, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan); err != nil {
			t.Fatalf("enroll %d: %v", i, err)
		}
	}
	if _, _, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan); !errors.Is(err, ErrRejected) || f.lastAudit(t).Reason != ReasonRateLimited {
		t.Fatalf("rate limit: %v", err)
	}
	f.now = f.now.Add(time.Minute)
	if _, _, err := f.s.Enroll(ctx, f.proof(t, k, secret, f.now), ident, "", lan); err != nil {
		t.Fatalf("after a minute: %v", err)
	}
}
