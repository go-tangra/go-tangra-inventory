//go:build integration

package app_test

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"io"
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
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"

	"github.com/go-tangra/go-tangra/v4/authn"
	"github.com/go-tangra/go-tangra/v4/identity"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certdelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/grpcapi"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/ingest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stream"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stream/valkeykv"
)

const itTenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "certmaterial", "testdata", name)) // #nosec G304 -- test fixture
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// fixtureLCM serves cert-1 from the certmaterial test vectors (valid until
// 2036) and records the key buffers it handed out.
type fixtureLCM struct {
	t    *testing.T
	mu   sync.Mutex
	keys [][]byte
}

func (l *fixtureLCM) Download(_ context.Context, tenantID, certID string, includeKey bool) (lcmclient.Bundle, error) {
	if tenantID != itTenant || certID != "cert-1" {
		return lcmclient.Bundle{}, lcmclient.ErrNotFound
	}
	b := lcmclient.Bundle{CertificateID: certID, CertPEM: fixture(l.t, "rsa.crt"), ChainPEM: fixture(l.t, "chain.pem"),
		NotAfter: time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC), Status: lcmclient.StatusActive}
	if includeKey {
		b.KeyPEM = []byte(fixture(l.t, "rsa.pkcs8.key"))
		l.mu.Lock()
		l.keys = append(l.keys, b.KeyPEM)
		l.mu.Unlock()
	}
	return b, nil
}

func startContainer(t *testing.T, req testcontainers.ContainerRequest) testcontainers.Container {
	t.Helper()
	c, err := testcontainers.GenericContainer(context.Background(), testcontainers.GenericContainerRequest{ContainerRequest: req, Started: true})
	if err != nil {
		t.Skipf("testcontainers unavailable: %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(context.Background()) })
	return c
}

func endpoint(t *testing.T, c testcontainers.Container, port string) string {
	t.Helper()
	host, _ := c.Host(context.Background())
	p, err := c.MappedPort(context.Background(), port)
	if err != nil {
		t.Fatal(err)
	}
	return host + ":" + p.Port()
}

// tlsFiles writes a throwaway CA and a 127.0.0.1 server certificate.
func tlsFiles(t *testing.T) (caFile, certFile, keyFile string) {
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

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestCertificateDeliveryEndToEnd (T041, SC-003): PostgreSQL + Valkey
// registry and event stream + TLS ingest edge with a real agent credential
// + fake lcm. The mesh request reaches the agent's command stream through
// Valkey, the agent fetches the bundle and reports it installed, Get and
// Verify show it; afterwards no database row (pg_dump), Valkey key, event,
// audit row or captured log line carries the private key.
func TestCertificateDeliveryEndToEnd(t *testing.T) {
	ctx := context.Background()
	logs := &syncBuffer{}
	log := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	prevLog := slog.Default()
	slog.SetDefault(log)
	defer slog.SetDefault(prevLog)

	pg := startContainer(t, testcontainers.ContainerRequest{Image: "timescale/timescaledb:latest-pg16", ExposedPorts: []string{"5432/tcp"},
		Env:        map[string]string{"POSTGRES_PASSWORD": "test", "POSTGRES_DB": "inventory"},
		WaitingFor: wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(2 * time.Minute)})
	vk := startContainer(t, testcontainers.ContainerRequest{Image: "valkey/valkey:8-alpine", ExposedPorts: []string{"6379/tcp"},
		WaitingFor: wait.ForLog("Ready to accept connections").WithStartupTimeout(time.Minute)})
	pgAddr := endpoint(t, pg, "5432/tcp")
	adminDSN := "postgres://postgres:test@" + pgAddr + "/inventory?sslmode=disable"
	admin, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(ctx)
	_, _ = admin.Exec(ctx, "CREATE ROLE inventory_app LOGIN PASSWORD 'app' NOBYPASSRLS")
	if err := store.Migrate(ctx, adminDSN); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(ctx, "postgres://inventory_app:app@"+pgAddr+"/inventory?sslmode=disable", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	db := repodb.New(st)

	vkAddr := endpoint(t, vk, "6379/tcp")
	vcfg := valkeykv.Config{Addresses: []string{vkAddr}, AllowPlaintext: true}
	opt, err := valkeykv.ClientOption(vcfg)
	if err != nil {
		t.Fatal(err)
	}
	vc, err := valkey.NewClient(opt)
	if err != nil {
		t.Fatal(err)
	}
	defer vc.Close()
	streamClient, err := valkeykv.New(vcfg)
	if err != nil {
		t.Fatal(err)
	}
	hub := stream.NewHub(streamClient, stream.Config{}, log)
	defer hub.Close()
	pub := events.HubPublisher{Hub: hub}
	reg := registry.NewValkey(vc, "it-1")

	lcm := &fixtureLCM{t: t}
	svc := certdelivery.New(db, lcm, reg, pub, certdelivery.Config{Enabled: true, PendingTTL: 168 * time.Hour, ReportTimeout: 15 * time.Minute, InstanceID: "it-1"})
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	enr := enroll.New(db, env)
	srv := ingest.New(enr, snapshots.New(db, hosts.New(db), pub), reg, db, 0, "it-1").WithCertDelivery(svc, 10, false)
	caFile, certFile, keyFile := tlsFiles(t)
	loader, err := ingest.NewCertLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	gs := ingest.NewGRPCServer(srv, loader.TransportOption())
	lis, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = gs.Serve(lis) }()
	defer gs.Stop()
	caPEM, _ := os.ReadFile(caFile) // #nosec G304 -- test file
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(caPEM)
	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12})))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	agent := invv1.NewIngestServiceClient(conn)

	// Enroll, submit (binds the host), open the command stream with cert.v1.
	secret, _, err := enr.MintToken(ctx, itTenant, "admin", "it", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	er, err := agent.Enroll(ctx, &invv1.EnrollRequest{EnrollmentToken: secret, Identity: &invv1.Identity{Hostname: "web-1", MachineId: "m-web-1"}, AgentVersion: "4.7.0"})
	if err != nil {
		t.Fatal(err)
	}
	actx := metadata.AppendToOutgoingContext(ctx, "x-agent-id", er.GetAgentId(), "x-agent-credential", er.GetAgentCredential())
	sub, err := agent.SubmitInventory(actx, &invv1.SubmitRequest{Inventory: &invv1.Inventory{Identity: &invv1.Identity{Hostname: "web-1", MachineId: "m-web-1"},
		AgentVersion: "4.7.0", CollectedAt: time.Now().Unix()}})
	if err != nil {
		t.Fatal(err)
	}
	sctx, stop := context.WithCancel(actx)
	defer stop()
	cmdStream, err := agent.StreamCommands(sctx, &invv1.StreamRequest{AgentId: er.GetAgentId(), AgentVersion: "4.7.0",
		Platform: &invv1.AgentPlatform{Os: "linux", Arch: "amd64", InstallType: "deb"}, Capabilities: []string{"upgrade.v1", "cert.v1"}})
	if err != nil {
		t.Fatal(err)
	}
	cmds := make(chan *invv1.Command, 8)
	go func() {
		for {
			c, err := cmdStream.Recv()
			if err != nil {
				return
			}
			cmds <- c
		}
	}()
	deadline := time.Now().Add(10 * time.Second)
	for on, _ := reg.IsOnline(ctx, er.GetAgentId()); !on; on, _ = reg.IsOnline(ctx, er.GetAgentId()) {
		if time.Now().After(deadline) {
			t.Fatal("agent not online")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Mesh request of the deployer.
	deployer, _ := identity.ParseSPIFFEID("spiffe://example.org/svc/deployer")
	mctx := authn.WithPeer(ctx, authn.PeerIdentity{ID: deployer, ServiceName: "deployer"})
	mesh := &grpcapi.CertDeliveryServer{Svc: svc, Sources: []string{"deployer"}}
	d, err := mesh.CreateCertificateDelivery(mctx, &invv1.CreateCertificateDeliveryRequest{TenantId: itTenant, IdempotencyKey: "job-1",
		ConfigurationId: "cfg-1", Trigger: "manual", CertificateId: "cert-1", Name: "www", KeyPolicy: "require",
		Selector: &invv1.HostSelector{HostIds: []string{sub.GetHostId()}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(d.GetItems()) != 1 || d.GetItems()[0].GetState() != invv1.DeliveryState_DELIVERY_STATE_DELIVERED {
		t.Fatalf("delivery = %v", d)
	}
	var cmd *invv1.Command
	select {
	case cmd = <-cmds:
	case <-time.After(10 * time.Second):
		t.Fatal("certificate command not delivered")
	}
	itemID := cmd.GetCertificate().GetItemId()
	if cmd.GetType() != invv1.CommandType_COMMAND_TYPE_CERTIFICATE || itemID != d.GetItems()[0].GetId() {
		t.Fatalf("command = %v", cmd)
	}

	// Agent pulls and reports.
	b, err := agent.FetchCertificate(actx, &invv1.FetchCertificateRequest{ItemId: itemID})
	if err != nil || !b.GetHasKey() || !strings.Contains(b.GetKeyPem(), "PRIVATE KEY") {
		t.Fatalf("fetch: %v", err)
	}
	if r, err := agent.ReportCertificate(actx, &invv1.ReportCertificateRequest{ItemId: itemID, State: "installed",
		FingerprintSha256: b.GetFingerprintSha256(), Serial: b.GetSerial(), HookExitCode: 0, Detail: "nginx reloaded"}); err != nil || !r.GetAccepted() {
		t.Fatalf("report: %v %v", r, err)
	}
	g, err := mesh.GetCertificateDelivery(mctx, &invv1.GetCertificateDeliveryRequest{TenantId: itTenant, Id: d.GetId()})
	if err != nil || g.GetItems()[0].GetState() != invv1.DeliveryState_DELIVERY_STATE_INSTALLED {
		t.Fatalf("get: %v %v", g, err)
	}
	v, err := mesh.VerifyHostCertificates(mctx, &invv1.VerifyHostCertificatesRequest{TenantId: itTenant,
		Selector: &invv1.HostSelector{HostIds: []string{sub.GetHostId()}}, Name: "www", ExpectedFingerprintSha256: b.GetFingerprintSha256()})
	if err != nil || v.GetMatched() != 1 {
		t.Fatalf("verify: %v %v", v, err)
	}
	// A second agent cannot fetch it (another tenant's or agent's item).
	secret2, _, _ := enr.MintToken(ctx, itTenant, "admin", "it", time.Hour)
	er2, _ := agent.Enroll(ctx, &invv1.EnrollRequest{EnrollmentToken: secret2, Identity: &invv1.Identity{Hostname: "web-2"}, AgentVersion: "4.7.0"})
	actx2 := metadata.AppendToOutgoingContext(ctx, "x-agent-id", er2.GetAgentId(), "x-agent-credential", er2.GetAgentCredential())
	if _, err := agent.FetchCertificate(actx2, &invv1.FetchCertificateRequest{ItemId: itemID}); err == nil {
		t.Fatal("another agent fetched the item")
	}
	time.Sleep(200 * time.Millisecond) // let the event stream settle

	// Key scan (SC-003).
	keyPEM := fixture(t, "rsa.pkcs8.key")
	blk, _ := pem.Decode([]byte(keyPEM))
	needles := []string{"PRIVATE KEY", base64.StdEncoding.EncodeToString(blk.Bytes)[:64], string(blk.Bytes[:48])}
	for _, line := range strings.Split(strings.TrimSpace(keyPEM), "\n")[1:4] {
		needles = append(needles, line)
	}
	for _, n := range needles[3:] { // the scan looks for what the agent really received
		if !strings.Contains(b.GetKeyPem(), n) {
			t.Fatal("needle not in the served key")
		}
	}
	scan := func(where, text string) {
		t.Helper()
		for _, n := range needles {
			if strings.Contains(text, n) {
				t.Errorf("key material in %s (%q)", where, n[:min(len(n), 20)])
			}
		}
	}
	// pg_dump of the whole database (schema + data, audit included).
	code, out, err := pg.Exec(ctx, []string{"pg_dump", "-U", "postgres", "inventory"})
	if err != nil || code != 0 {
		t.Fatalf("pg_dump: %d %v", code, err)
	}
	dump, _ := io.ReadAll(out)
	if !bytes.Contains(dump, []byte("inventory_cert_delivery_items")) || !bytes.Contains(dump, []byte("cert_delivery_fetched")) {
		t.Fatal("pg_dump incomplete")
	}
	scan("pg_dump", string(dump))
	// Every Valkey key, whatever its type (registry, event streams).
	keys, err := vc.Do(ctx, vc.B().Keys().Pattern("*").Build()).AsStrSlice()
	if err != nil {
		t.Fatal(err)
	}
	sawEvents := false
	for _, k := range keys {
		typ, _ := vc.Do(ctx, vc.B().Type().Key(k).Build()).ToString()
		var text string
		switch typ {
		case "string":
			text, _ = vc.Do(ctx, vc.B().Get().Key(k).Build()).ToString()
		case "stream":
			entries, _ := vc.Do(ctx, vc.B().Xrange().Key(k).Start("-").End("+").Build()).AsXRange()
			for _, e := range entries {
				for f, val := range e.FieldValues {
					text += f + "=" + val + "\n"
				}
			}
			if strings.HasPrefix(k, "platform:events:") && strings.Contains(text, "inventory.certificate.delivery") {
				sawEvents = true
			}
		case "hash":
			m, _ := vc.Do(ctx, vc.B().Hgetall().Key(k).Build()).AsStrMap()
			for f, val := range m {
				text += f + "=" + val + "\n"
			}
		case "set":
			s, _ := vc.Do(ctx, vc.B().Smembers().Key(k).Build()).AsStrSlice()
			text = strings.Join(s, "\n")
		case "list":
			s, _ := vc.Do(ctx, vc.B().Lrange().Key(k).Start(0).Stop(-1).Build()).AsStrSlice()
			text = strings.Join(s, "\n")
		case "zset":
			s, _ := vc.Do(ctx, vc.B().Zrange().Key(k).Min("0").Max("-1").Build()).AsStrSlice()
			text = strings.Join(s, "\n")
		}
		scan("valkey "+k, text)
	}
	if !sawEvents {
		t.Errorf("no inventory.certificate.delivery event in the platform stream (keys %v)", keys)
	}
	scan("logs", logs.String())
	// The relay zeroed every key buffer it received from lcm.
	lcm.mu.Lock()
	for _, k := range lcm.keys {
		if bytes.ContainsFunc(k, func(r rune) bool { return r != 0 }) {
			t.Error("lcm key buffer not zeroed")
		}
	}
	lcm.mu.Unlock()
}
