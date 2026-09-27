//go:build integration

package ingest_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"log/slog"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/ingest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrader"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

const itTenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func startTimescale(t *testing.T) (adminDSN, appDSN string) {
	t.Helper()
	ctx := context.Background()
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"},
			Env:        map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "inventory"},
			WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute),
		}, Started: true,
	})
	if err != nil {
		t.Skipf("testcontainers unavailable: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	host, _ := c.Host(ctx)
	port, _ := c.MappedPort(ctx, "5432/tcp")
	adminDSN = "postgres://postgres:test@" + host + ":" + port.Port() + "/inventory?sslmode=disable"
	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Exec(ctx, "CREATE ROLE inventory_app LOGIN PASSWORD 'app' NOBYPASSRLS")
	_ = conn.Close(ctx)
	return adminDSN, "postgres://inventory_app:app@" + host + ":" + port.Port() + "/inventory?sslmode=disable"
}

// serverTLS writes a throwaway CA and a 127.0.0.1 server certificate.
func serverTLS(t *testing.T) (caFile, certFile, keyFile string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "it CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "ingest"}, NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	caFile, certFile, keyFile = filepath.Join(dir, "ca.pem"), filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	_ = os.WriteFile(caFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600)
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return
}

type repoAuditor struct{ st audit.Store }

func (r repoAuditor) Record(ctx context.Context, e audit.Event) error {
	row, err := audit.Row(e, time.Now().UTC())
	if err != nil {
		return err
	}
	return r.st.AppendAudit(ctx, row)
}

// recInstaller records the helper start instead of running it.
type recInstaller struct {
	mu      sync.Mutex
	started []string
}

func (r *recInstaller) Supported(string) error { return nil }
func (r *recInstaller) StartHelper(_ context.Context, helper, state, req string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, req)
	return nil
}
func (r *recInstaller) InstallPackage(context.Context, string, string, bool) error { return nil }
func (r *recInstaller) SwapBinary(context.Context, string, string) (string, error) { return "", nil }
func (r *recInstaller) RestoreBinary(context.Context, string, string) error        { return nil }
func (r *recInstaller) RestartService(context.Context) error                       { return nil }

// TestDownloadThroughRealIngest: a seeded release is downloaded over the
// real ingest gRPC server (TLS, per-agent credential, PostgreSQL) by the
// agent's own client and verifier; a chunk corrupted in the database is
// refused by the agent (checksum_mismatch).
func TestDownloadThroughRealIngest(t *testing.T) {
	adminDSN, appDSN := startTimescale(t)
	ctx := context.Background()
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, appDSN, 8)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := repodb.New(st)

	// Signed release 4.5.0 (and 4.4.0 as rollback package), 3 MiB deb.
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	keys := agentrelease.Keyring{"it-key": pub}
	bundle := t.TempDir()
	p := agentrelease.Platform{OS: "linux", Arch: "amd64", InstallType: "deb"}
	for _, v := range []string{"4.4.0", "4.5.0"} {
		d := filepath.Join(bundle, v)
		_ = os.MkdirAll(d, 0o755)
		data := make([]byte, 3<<20+123)
		_, _ = rand.Read(data)
		sum := sha256.Sum256(data)
		_ = os.WriteFile(filepath.Join(d, "agent.deb"), data, 0o644)
		m := agentrelease.Manifest{Schema: 1, Version: v, CreatedAt: time.Now().UTC().Truncate(time.Second), KeyID: "it-key",
			Artifacts: []agentrelease.Artifact{{OS: p.OS, Arch: p.Arch, InstallType: p.InstallType, File: "agent.deb", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}}}
		b := m.Encode()
		_ = os.WriteFile(filepath.Join(d, agentrelease.ManifestFile), b, 0o644)
		_ = os.WriteFile(filepath.Join(d, agentrelease.SignatureFile), []byte(agentrelease.EncodeSignature(ed25519.Sign(priv, b))), 0o644)
	}
	rel := releases.New(db, keys, repoAuditor{db}, slog.New(slog.DiscardHandler), releases.Config{BundleDir: bundle, KeepVersions: 5, ChunkBytes: 1 << 20})
	if n := rel.SeedBundles(ctx); n != 2 {
		t.Fatalf("seeded %d", n)
	}

	env, _ := sealed.NewEnvelope(make([]byte, 32))
	enr := enroll.New(db, env)
	reg := registry.NewMemory()
	upg := upgrades.New(db, rel, reg, events.HubPublisher{}, upgrades.Config{RequestTTL: time.Hour, ProgressTimeout: 15 * time.Minute})
	srv := ingest.New(enr, snapshots.New(db, hosts.New(db), events.HubPublisher{}), reg, db, 0, "it").WithUpgrades(upg, rel, 4, time.Minute)
	caFile, certFile, keyFile := serverTLS(t)
	loader, err := ingest.NewCertLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	gs := ingest.NewGRPCServer(srv, loader.TransportOption())
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)

	// Enroll an agent and submit as 4.4.0 over TLS.
	secret, _, err := enr.MintToken(ctx, itTenant, "admin", "it", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	s := sender.New(lis.Addr().String(), sender.Options{CAFile: caFile})
	agentID, cred, err := s.Enroll(ctx, secret, store.Identity{Hostname: "node-1"}, "4.4.0")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Submit(ctx, agentID, cred, store.Inventory{Identity: store.Identity{Hostname: "node-1"}, AgentVersion: "4.4.0", CollectedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	// Announce the platform on the command stream.
	client, conn, err := s.Dial()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sctx, stop := context.WithCancel(sender.AuthContext(ctx, agentID, cred))
	stream, err := client.StreamCommands(sctx, &invv1.StreamRequest{AgentId: agentID, AgentVersion: "4.4.0",
		Platform: &invv1.AgentPlatform{Os: "linux", Arch: "amd64", InstallType: "deb"}, Capabilities: []string{"upgrade.v1"}})
	if err != nil {
		t.Fatal(err)
	}
	cmds := make(chan *invv1.Command, 4)
	go func() {
		for {
			c, err := stream.Recv()
			if err != nil {
				return
			}
			cmds <- c
		}
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if on, _ := reg.IsOnline(ctx, agentID); on {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent not online")
		}
		time.Sleep(20 * time.Millisecond)
	}
	res, err := upg.Request(ctx, itTenant, upgrades.Actor{Kind: upgrades.ActorUser, ID: "admin"}, []string{agentID}, false)
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("request = %+v %v", res, err)
	}
	var cmd *invv1.Command
	select {
	case cmd = <-cmds:
	case <-time.After(5 * time.Second):
		t.Fatal("upgrade command not delivered")
	}
	stop()

	// The agent side: real client over TLS, real file system, recorded installer.
	staging := filepath.Join(t.TempDir(), "upgrade")
	exe := filepath.Join(t.TempDir(), "inventory-agent")
	_ = os.WriteFile(exe, []byte("agent 4.4.0"), 0o755)
	inst := &recInstaller{}
	u := selfupdate.New(selfupdate.Config{Version: "4.4.0", Platform: p, Keys: keys, StagingDir: staging, Executable: exe, ConfirmTimeout: 5 * time.Minute},
		s.UpgradeClient(agentID, cred), upgrader.OSFS{}, inst, selfupdate.RealClock(), slog.New(slog.DiscardHandler))
	if err := u.Upgrade(ctx, selfupdate.Command{RequestID: cmd.GetUpgrade().GetRequestId(), TargetVersion: cmd.GetUpgrade().GetTargetVersion()}); err != nil {
		t.Fatalf("upgrade: %v", err)
	}
	staged, _ := os.ReadFile(filepath.Join(staging, "4.5.0", "agent.deb"))
	want, _ := os.ReadFile(filepath.Join(bundle, "4.5.0", "agent.deb"))
	if !bytes.Equal(staged, want) || len(inst.started) != 1 {
		t.Fatalf("staged %d bytes (want %d), helper starts %v", len(staged), len(want), inst.started)
	}
	if rb, _ := os.ReadFile(filepath.Join(staging, "4.4.0", "agent.deb")); len(rb) != 3<<20+123 {
		t.Fatalf("rollback package = %d bytes", len(rb))
	}
	got, err := db.GetAgentUpgrade(ctx, itTenant, res.Created[0].ID)
	if err != nil || got.State != store.UpgradeInstalling {
		t.Fatalf("request = %+v %v", got, err)
	}

	// Tamper with a stored chunk: the next upgrade is refused by the agent.
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	if _, err := admin.Exec(ctx, `UPDATE inventory_agent_artifact_chunks SET data = overlay(data placing '\xff'::bytea from 10 for 1)
		WHERE version='4.5.0' AND seq=1`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE inventory_agent_upgrades SET state='failed' WHERE id=$1`, got.ID); err != nil {
		t.Fatal(err)
	}
	_ = os.RemoveAll(staging)
	res, err = upg.Request(ctx, itTenant, upgrades.Actor{Kind: upgrades.ActorUser, ID: "admin"}, []string{agentID}, false)
	if err != nil || len(res.Created) != 1 {
		t.Fatalf("second request = %+v %v", res, err)
	}
	err = u.Upgrade(ctx, selfupdate.Command{RequestID: res.Created[0].ID, TargetVersion: "4.5.0"})
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("tampered artifact = %v", err)
	}
	got, _ = db.GetAgentUpgrade(ctx, itTenant, res.Created[0].ID)
	if got.State != store.UpgradeFailed || got.Reason != "checksum_mismatch" || len(inst.started) != 1 {
		t.Fatalf("after tampering: %+v, helper starts %v", got, inst.started)
	}
}
