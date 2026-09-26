package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// --- test double: an in-memory repo.Store with only the agent/enrollment
// paths implemented (the rest satisfy the interface as no-ops). ---

type fakeStore struct {
	mu             sync.Mutex
	tokens         map[string]store.EnrollmentToken
	agents         map[string]store.Agent
	failCreateTok  bool
	failCreateAgnt bool
}

func newFake() *fakeStore {
	return &fakeStore{tokens: map[string]store.EnrollmentToken{}, agents: map[string]store.Agent{}}
}

func (f *fakeStore) CreateEnrollmentToken(_ context.Context, t store.EnrollmentToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreateTok {
		return errors.New("create token boom")
	}
	f.tokens[t.ID] = t
	return nil
}

func (f *fakeStore) ConsumeEnrollmentToken(_ context.Context, tokenHash string, now time.Time) (store.EnrollmentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, t := range f.tokens {
		if t.TokenHash != tokenHash {
			continue
		}
		if t.Revoked || t.UsedAt != nil || !now.Before(t.ExpiresAt) {
			return store.EnrollmentToken{}, repo.ErrConflict
		}
		used := now
		t.UsedAt = &used
		f.tokens[id] = t
		return t, nil
	}
	return store.EnrollmentToken{}, repo.ErrNotFound
}

func (f *fakeStore) RevokeEnrollmentToken(_ context.Context, tenantID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tokens[id]
	if !ok || t.TenantID != tenantID {
		return repo.ErrNotFound
	}
	t.Revoked = true
	f.tokens[id] = t
	return nil
}

func (f *fakeStore) CreateAgent(_ context.Context, a store.Agent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failCreateAgnt {
		return errors.New("create agent boom")
	}
	f.agents[a.ID] = a
	return nil
}

func (f *fakeStore) GetAgentByID(_ context.Context, id string) (store.Agent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok {
		return store.Agent{}, repo.ErrNotFound
	}
	return a, nil
}

func (f *fakeStore) RevokeAgent(_ context.Context, tenantID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.agents[id]
	if !ok || a.TenantID != tenantID {
		return repo.ErrNotFound
	}
	a.Revoked = true
	f.agents[id] = a
	return nil
}

// putAgent injects an agent directly (bypasses sealing) for negative tests.
func (f *fakeStore) putAgent(a store.Agent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.agents[a.ID] = a
}

// --- unused interface methods ---

func (f *fakeStore) ResolveHost(context.Context, string, store.Host) (store.Host, error) {
	return store.Host{}, nil
}
func (f *fakeStore) GetHost(context.Context, string, string) (store.Host, error) {
	return store.Host{}, nil
}
func (f *fakeStore) GetHostByIdentity(context.Context, string, store.Identity) (store.Host, error) {
	return store.Host{}, nil
}
func (f *fakeStore) ListHosts(context.Context, string, store.HostFilter) ([]store.Host, error) {
	return nil, nil
}
func (f *fakeStore) SetHostTags(context.Context, string, string, map[string]string) error { return nil }
func (f *fakeStore) RetireHost(context.Context, string, string) error                     { return nil }
func (f *fakeStore) DeleteHost(context.Context, string, string) error                     { return nil }
func (f *fakeStore) MarkStaleHosts(context.Context, time.Time) (int64, error)             { return 0, nil }
func (f *fakeStore) InsertSnapshot(context.Context, store.Snapshot) error                 { return nil }
func (f *fakeStore) GetSnapshot(context.Context, string, string) (store.Snapshot, error) {
	return store.Snapshot{}, nil
}
func (f *fakeStore) ListSnapshotsForHost(context.Context, string, string, int, string) ([]store.Snapshot, error) {
	return nil, nil
}
func (f *fakeStore) GetLatestForHost(context.Context, string, string) (store.Snapshot, error) {
	return store.Snapshot{}, nil
}
func (f *fakeStore) DeleteSnapshot(context.Context, string, string) error     { return nil }
func (f *fakeStore) PurgeSnapshots(context.Context, time.Time) (int64, error) { return 0, nil }
func (f *fakeStore) InsertChanges(context.Context, []store.Change) error      { return nil }
func (f *fakeStore) ListChangesForHost(context.Context, string, string, int) ([]store.Change, error) {
	return nil, nil
}
func (f *fakeStore) ListChangesForSnapshot(context.Context, string, string) ([]store.Change, error) {
	return nil, nil
}
func (f *fakeStore) GetAgent(context.Context, string, string) (store.Agent, error) {
	return store.Agent{}, nil
}
func (f *fakeStore) TouchAgent(context.Context, string, string, string, time.Time) error { return nil }
func (f *fakeStore) TenantStats(context.Context, string, time.Time) (repo.Stats, error) {
	return repo.Stats{}, nil
}
func (f *fakeStore) TenantIDs(context.Context) ([]string, error)       { return nil, nil }
func (f *fakeStore) AppendAudit(context.Context, store.AuditRow) error { return nil }
func (f *fakeStore) SetReportDigest(context.Context, string, string, string, time.Time) (bool, error) {
	return false, nil
}
func (f *fakeStore) ListReportTenants(context.Context, time.Time, int) ([]string, time.Time, error) {
	return nil, time.Time{}, nil
}
func (f *fakeStore) ListHostReportRows(context.Context, string, store.ReportRowFilter) ([]store.Host, error) {
	return nil, nil
}

var _ repo.Store = (*fakeStore)(nil)

// --- helpers ---

func newEnv(t *testing.T) *sealed.Envelope {
	t.Helper()
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		t.Fatalf("NewEnvelope: %v", err)
	}
	return env
}

// --- tests ---

func TestMintEnrollVerifyHappyPath(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))
	svc.SetClock(nil) // ignored; keeps default clock

	secret, tok, err := svc.MintToken(ctx, "tenant-1", "admin@example.com", "laptops", time.Hour)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if secret == "" {
		t.Fatal("empty secret")
	}
	if tok.TokenHash != "" {
		t.Fatal("returned token row still carries the stored hash")
	}
	if tok.TenantID != "tenant-1" || tok.ID == "" {
		t.Fatalf("unexpected token row: %+v", tok)
	}

	agentID, cred, err := svc.Enroll(ctx, secret, store.Identity{Hostname: "host-a"}, "1.2.3")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if agentID == "" || cred == "" {
		t.Fatal("empty agentID or credential")
	}

	agent, err := svc.Verify(ctx, agentID, cred)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if agent.TenantID != "tenant-1" {
		t.Fatalf("tenant scope lost: %q", agent.TenantID)
	}
	if agent.IdentityHint != "host-a" || agent.AgentVersion != "1.2.3" {
		t.Fatalf("unexpected agent: %+v", agent)
	}
	if agent.CredentialSealed != nil {
		t.Fatal("Verify returned sealed credential bytes")
	}
}

func TestReusedTokenRefused(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))

	secret, _, err := svc.MintToken(ctx, "t", "a", "", time.Hour)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	if _, _, err := svc.Enroll(ctx, secret, store.Identity{}, "v"); err != nil {
		t.Fatalf("first Enroll: %v", err)
	}
	if _, _, err := svc.Enroll(ctx, secret, store.Identity{}, "v"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("reuse: want ErrTokenInvalid, got %v", err)
	}
}

func TestExpiredTokenRefused(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	svc.SetClock(func() time.Time { return base })

	secret, _, err := svc.MintToken(ctx, "t", "a", "", time.Minute)
	if err != nil {
		t.Fatalf("MintToken: %v", err)
	}
	svc.SetClock(func() time.Time { return base.Add(2 * time.Minute) })
	if _, _, err := svc.Enroll(ctx, secret, store.Identity{}, "v"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("expired: want ErrTokenInvalid, got %v", err)
	}
}

func TestUnknownSecretRefused(t *testing.T) {
	ctx := context.Background()
	svc := New(newFake(), newEnv(t))
	if _, _, err := svc.Enroll(ctx, "never-minted", store.Identity{}, "v"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("unknown secret: want ErrTokenInvalid, got %v", err)
	}
}

func TestRevokedAgentRefused(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))

	secret, _, _ := svc.MintToken(ctx, "tenant-1", "a", "", time.Hour)
	agentID, cred, err := svc.Enroll(ctx, secret, store.Identity{}, "v")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if err := svc.RevokeAgent(ctx, "tenant-1", agentID); err != nil {
		t.Fatalf("RevokeAgent: %v", err)
	}
	if _, err := svc.Verify(ctx, agentID, cred); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked: want ErrUnauthenticated, got %v", err)
	}
}

func TestWrongCredentialRefused(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))

	secret, _, _ := svc.MintToken(ctx, "t", "a", "", time.Hour)
	agentID, _, err := svc.Enroll(ctx, secret, store.Identity{}, "v")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if _, err := svc.Verify(ctx, agentID, "not-the-credential"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong cred: want ErrUnauthenticated, got %v", err)
	}
}

func TestVerifyUnknownAgent(t *testing.T) {
	svc := New(newFake(), newEnv(t))
	if _, err := svc.Verify(context.Background(), "nope", "x"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown agent: want ErrUnauthenticated, got %v", err)
	}
}

func TestVerifyCorruptSealed(t *testing.T) {
	st := newFake()
	svc := New(st, newEnv(t))
	st.putAgent(store.Agent{ID: "a1", TenantID: "t", CredentialSealed: []byte("not a valid envelope")})
	if _, err := svc.Verify(context.Background(), "a1", "x"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("corrupt sealed: want ErrUnauthenticated, got %v", err)
	}
}

func TestCredentialNeverInAgentJSON(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))

	secret, _, _ := svc.MintToken(ctx, "t", "a", "", time.Hour)
	agentID, cred, err := svc.Enroll(ctx, secret, store.Identity{Hostname: "h"}, "v")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	agent, err := svc.Verify(ctx, agentID, cred)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	b, err := json.Marshal(agent)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	if strings.Contains(js, cred) {
		t.Fatalf("credential leaked into agent JSON: %s", js)
	}
	if strings.Contains(js, secret) {
		t.Fatalf("token secret leaked into agent JSON: %s", js)
	}
	if strings.Contains(js, "CredentialSealed") || strings.Contains(js, "credential_sealed") {
		t.Fatalf("sealed field leaked into agent JSON: %s", js)
	}
}

func TestMintTokenValidation(t *testing.T) {
	svc := New(newFake(), newEnv(t))
	ctx := context.Background()
	if _, _, err := svc.MintToken(ctx, "", "a", "", time.Hour); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("empty tenant: want ErrTokenInvalid, got %v", err)
	}
	if _, _, err := svc.MintToken(ctx, "t", "a", "", 0); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("zero ttl: want ErrTokenInvalid, got %v", err)
	}
}

func TestMintTokenStoreError(t *testing.T) {
	st := newFake()
	st.failCreateTok = true
	svc := New(st, newEnv(t))
	if _, _, err := svc.MintToken(context.Background(), "t", "a", "", time.Hour); err == nil {
		t.Fatal("want store error")
	}
}

func TestEnrollCreateAgentError(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))
	secret, _, _ := svc.MintToken(ctx, "t", "a", "", time.Hour)
	st.failCreateAgnt = true
	if _, _, err := svc.Enroll(ctx, secret, store.Identity{}, "v"); err == nil {
		t.Fatal("want create-agent error")
	}
}

func TestRandomSecretError(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))

	orig := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	defer func() { randRead = orig }()

	if _, _, err := svc.MintToken(ctx, "t", "a", "", time.Hour); err == nil {
		t.Fatal("MintToken: want rng error")
	}

	// Enroll: token consumed before secret gen, so mint one with entropy on.
	randRead = orig
	secret, _, _ := svc.MintToken(ctx, "t", "a", "", time.Hour)
	randRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	if _, _, err := svc.Enroll(ctx, secret, store.Identity{}, "v"); err == nil {
		t.Fatal("Enroll: want rng error")
	}
}

func TestEnrollSealError(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))
	secret, _, _ := svc.MintToken(ctx, "t", "a", "", time.Hour)

	origSeal := sealFn
	sealFn = func(*sealed.Envelope, []byte, []byte) ([]byte, error) { return nil, errors.New("seal boom") }
	defer func() { sealFn = origSeal }()

	if _, _, err := svc.Enroll(ctx, secret, store.Identity{}, "v"); err == nil {
		t.Fatal("want seal error")
	}
}

func TestRevokeToken(t *testing.T) {
	ctx := context.Background()
	st := newFake()
	svc := New(st, newEnv(t))
	secret, tok, _ := svc.MintToken(ctx, "tenant-1", "a", "", time.Hour)
	if err := svc.RevokeToken(ctx, "tenant-1", tok.ID); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, _, err := svc.Enroll(ctx, secret, store.Identity{}, "v"); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("after revoke: want ErrTokenInvalid, got %v", err)
	}
	// revoking an unknown token surfaces the store error
	if err := svc.RevokeToken(ctx, "tenant-1", "missing"); err == nil {
		t.Fatal("want error revoking unknown token")
	}
}

func TestRevokeAgentUnknown(t *testing.T) {
	svc := New(newFake(), newEnv(t))
	if err := svc.RevokeAgent(context.Background(), "t", "missing"); err == nil {
		t.Fatal("want error revoking unknown agent")
	}
}

func TestHashSecretDeterministicNotSecret(t *testing.T) {
	h1 := hashSecret("abc")
	h2 := hashSecret("abc")
	if h1 != h2 || h1 == "abc" || len(h1) != 64 {
		t.Fatalf("hashSecret misbehaving: %q", h1)
	}
	if string(agentAD("x")) != "agent:x" {
		t.Fatal("agentAD format changed")
	}
}
