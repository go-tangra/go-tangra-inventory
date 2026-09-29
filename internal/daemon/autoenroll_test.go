package daemon

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/ingest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// With no credential and no token file, the daemon enrolls with its
// auto-enrollment key and persists the issued credential.
func TestEnsureEnrolledAuto(t *testing.T) {
	mem := memstore.New()
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	auto := autoenroll.New(mem, env)
	srv := ingest.New(enroll.New(mem, env), snapshots.New(mem, hosts.New(mem), events.HubPublisher{}), registry.NewMemory(), mem, 0, "d").WithAutoEnroll(auto)
	gs := ingest.NewGRPCServer(srv)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	ctx := context.Background()
	const tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	actor := autoenroll.Actor{Kind: "user", ID: "u1"}
	_, _ = auto.SetEnabled(ctx, tenant, actor, true)
	k, secret, err := auto.CreateKey(ctx, tenant, actor, autoenroll.KeyInput{Name: "lo", AllowedCIDRs: []string{"127.0.0.1"}})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "auto.key")
	if err := os.WriteFile(keyFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultAgent()
	cfg.IngestEndpoint, cfg.Insecure = lis.Addr().String(), true
	cfg.CredentialFile = filepath.Join(dir, "cred")
	cfg.StateFile = filepath.Join(dir, "state")
	cfg.AutoEnroll = config.AgentAutoEnroll{KeyID: k.KeyID, KeyFile: keyFile}
	cfg.TokenFile = filepath.Join(dir, "enrollment.token") // named but absent: the key is used
	d := New(cfg, "4.4.0")
	if err := d.ensureEnrolled(ctx, store.Identity{Hostname: "h1", HardwareUUID: "u1"}); err != nil {
		t.Fatalf("ensureEnrolled: %v", err)
	}
	if id, cred, ok := d.Credentials(); !ok || id == "" || cred == "" {
		t.Fatal("credential not persisted")
	}
	if a, err := mem.GetAgent(ctx, tenant, d.agentID); err != nil || a.EnrolledVia != store.EnrolledViaAuto {
		t.Fatalf("agent %+v %v", a, err)
	}

	// Key file problems are reported before any call.
	for name, content := range map[string]*string{"missing": nil, "empty": ptr("  \n")} {
		c := cfg
		c.CredentialFile = filepath.Join(t.TempDir(), "cred")
		c.AutoEnroll.KeyFile = filepath.Join(t.TempDir(), "k")
		if content != nil {
			_ = os.WriteFile(c.AutoEnroll.KeyFile, []byte(*content), 0o600)
		}
		if err := New(c, "4.4.0").ensureEnrolled(ctx, store.Identity{Hostname: "h2"}); err == nil || !strings.Contains(err.Error(), "auto_enroll.key_file") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// A refused enrollment surfaces as an error.
	c := cfg
	c.CredentialFile = filepath.Join(t.TempDir(), "cred")
	bad := filepath.Join(t.TempDir(), "k")
	_ = os.WriteFile(bad, []byte("aks_wrong"), 0o600)
	c.AutoEnroll.KeyFile = bad
	if err := New(c, "4.4.0").ensureEnrolled(ctx, store.Identity{Hostname: "h3"}); err == nil {
		t.Error("wrong key accepted")
	}
}

func ptr(s string) *string { return &s }

// A present token file wins over the key.
func TestEnsureEnrolledTokenWins(t *testing.T) {
	dir := t.TempDir()
	tok := filepath.Join(dir, "enrollment.token")
	_ = os.WriteFile(tok, []byte("  "), 0o600)
	cfg := config.DefaultAgent()
	cfg.IngestEndpoint, cfg.Insecure = "127.0.0.1:1", true
	cfg.CredentialFile, cfg.StateFile, cfg.TokenFile = filepath.Join(dir, "c"), filepath.Join(dir, "s"), tok
	cfg.AutoEnroll = config.AgentAutoEnroll{KeyID: "ak_0123456789abcdef01234567", KeyFile: filepath.Join(dir, "missing.key")}
	err := New(cfg, "4.4.0").ensureEnrolled(context.Background(), store.Identity{Hostname: "h"})
	if err == nil || !strings.Contains(err.Error(), "token_file is empty") {
		t.Fatalf("token not preferred: %v", err)
	}
}
