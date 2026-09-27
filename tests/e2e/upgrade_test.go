//go:build e2e

// Package e2e runs the agent self-upgrade end to end (feature 023, SC-005):
// real deb/rpm packages installed in Debian 12 and Rocky 9 containers that
// run systemd, an in-process inventory ingest edge serving dev-signed
// releases, and the agent's own upgrade path (download, verify, install
// through dpkg/rpm, confirm or roll back).
//
//	make e2e-upgrade     # needs Docker with privileged containers
package e2e

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	tcexec "github.com/testcontainers/testcontainers-go/exec"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/ingest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/memstore"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

const (
	tenant     = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"
	keyID      = "e2e-dev"
	gatewayDNS = "host.docker.internal"
	nfpm       = "github.com/goreleaser/nfpm/v2/cmd/nfpm@v2.47.0"
	pkgName    = "tangra-inventory-agent"
)

// Versions: N is installed by hand, N1 upgrades cleanly, NTampered is stored
// with a corrupted chunk, NBroken installs a binary that exits at start.
const (
	vN        = "4.4.0"
	vN1       = "4.4.1"
	vTampered = "4.4.2"
	vBroken   = "4.4.3"
)

type distro struct {
	name, image, install, format, packageDB string
}

var distros = []distro{
	{name: "debian12", format: "deb", image: `FROM debian:12
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends systemd systemd-sysv dbus ca-certificates && apt-get clean && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
`, install: "dpkg -i", packageDB: `dpkg-query -W -f='${Version}' ` + pkgName},
	{name: "rocky9", format: "rpm", image: `FROM rockylinux:9
RUN dnf -y install systemd procps-ng && dnf clean all
STOPSIGNAL SIGRTMIN+3
CMD ["/sbin/init"]
`, install: "rpm -i", packageDB: `rpm -q --qf %{VERSION} ` + pkgName},
}

type edge struct {
	mem   *memstore.Mem
	rel   *releases.Service
	upg   *upgrades.Service
	enr   *enroll.Service
	port  int
	ca    []byte
	priv  ed25519.PrivateKey
	pub   ed25519.PublicKey
	bdir  string
	build string
}

func TestAgentSelfUpgradeInSystemdContainers(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	e := startEdge(t)
	repoRoot := findRepoRoot(t)
	for _, v := range []string{vN, vN1, vTampered} {
		e.buildAgent(t, repoRoot, v)
	}
	e.brokenAgent(t, vBroken)
	for _, d := range distros {
		for _, v := range []string{vN, vN1, vTampered, vBroken} {
			e.pack(t, repoRoot, v, d.format)
		}
	}
	// The platform starts with N and N1 bundled: N1 is the current version.
	for _, v := range []string{vN, vN1} {
		e.bundle(t, v)
	}
	if n := e.rel.SeedBundles(context.Background()); n != 2 {
		t.Fatalf("seeded %d releases", n)
	}
	for _, d := range distros {
		t.Run(d.name, func(t *testing.T) { e.run(t, d) })
	}
}

func (e *edge) run(t *testing.T, d distro) {
	ctx := context.Background()
	// Drop the tampered and broken releases afterwards so the next distro starts from N1.
	t.Cleanup(func() { e.forget(vBroken); e.forget(vTampered) })
	c := startSystemd(t, d)
	sh := func(cmd string) string {
		t.Helper()
		code, out := execIn(t, c, cmd)
		if code != 0 {
			t.Fatalf("%s: exit %d\n%s", cmd, code, out)
		}
		return strings.TrimSpace(out)
	}

	// Manual first install of N, configured to reach the in-process edge.
	copyTo(t, c, e.pkgPath(vN, d.format), "/root/agent-n."+d.format)
	sh(d.install + " /root/agent-n." + d.format)
	secret, _, err := e.enr.MintToken(ctx, tenant, "e2e", d.name, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	cfg := fmt.Sprintf(`ingest_endpoint: "%s:%d"
token_file: /etc/inventory-agent/enrollment.token
credential_file: /var/lib/inventory-agent/credential
state_file: /var/lib/inventory-agent/state.json
ca_file: /etc/inventory-agent/ca.pem
interval_seconds: 3600
collect_bmc: false
collect_updates: false
collect_disks: true
upgrade:
  enabled: true
  confirm_timeout_seconds: 60
`, gatewayDNS, e.port)
	copyBytes(t, c, []byte(cfg), "/etc/inventory-agent/agent.yaml", 0o640)
	copyBytes(t, c, e.ca, "/etc/inventory-agent/ca.pem", 0o644)
	copyBytes(t, c, []byte(secret+"\n"), "/etc/inventory-agent/enrollment.token", 0o600)
	sh("systemctl start inventory-agent.service")

	agentID := e.waitAgent(t, func(f upgrades.FleetEntry) bool {
		return f.Online && f.Version == vN && f.InstallType == d.format
	}, 2*time.Minute, "agent N online with its platform").AgentID
	onN1 := func(f upgrades.FleetEntry) bool { return f.AgentID == agentID && f.Online && f.Version == vN1 }

	// 1. N -> N1 through the package manager; the agent reports N1.
	u := e.request(t, agentID, vN1)
	e.waitUpgrade(t, c, u, store.UpgradeSucceeded, 4*time.Minute)
	if got := sh(d.packageDB); got != vN1 {
		t.Fatalf("package DB after upgrade = %q, want %s", got, vN1)
	}
	e.waitAgent(t, onN1, time.Minute, "agent reports N1")

	// 2. Tampered artifact: refused by the agent, N1 keeps running.
	e.bundle(t, vTampered)
	e.rel.SeedBundles(ctx)
	e.corrupt(t, vTampered, d.format)
	u = e.request(t, agentID, vTampered)
	got := e.waitUpgrade(t, c, u, store.UpgradeFailed, 3*time.Minute)
	if got.Reason != "checksum_mismatch" {
		t.Fatalf("tampered artifact reason = %q", got.Reason)
	}
	if v := sh(d.packageDB); v != vN1 {
		t.Fatalf("package DB after tampered artifact = %q", v)
	}
	if sh("systemctl is-active inventory-agent.service") != "active" {
		t.Fatal("agent stopped after a tampered artifact")
	}

	// 3. A release whose binary exits at start: rolled back to N1 within the
	// confirm timeout, the package DB shows N1 again.
	e.bundle(t, vBroken)
	e.rel.SeedBundles(ctx)
	u = e.request(t, agentID, vBroken)
	e.waitUpgrade(t, c, u, store.UpgradeRolledBack, 5*time.Minute)
	if v := sh(d.packageDB); v != vN1 {
		t.Fatalf("package DB after rollback = %q, want %s", v, vN1)
	}
	e.waitAgent(t, onN1, 2*time.Minute, "agent back on N1")
}

// ---- in-process edge ----

type memAuditor struct{ m *memstore.Mem }

func (a memAuditor) Record(ctx context.Context, ev audit.Event) error {
	row, err := audit.Row(ev, time.Now().UTC())
	if err != nil {
		return err
	}
	return a.m.AppendAudit(ctx, row)
}

func startEdge(t *testing.T) *edge {
	t.Helper()
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	e := &edge{mem: memstore.New(), priv: priv, pub: pub, bdir: t.TempDir(), build: t.TempDir()}
	keys := agentrelease.Keyring{keyID: pub}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	e.rel = releases.New(e.mem, keys, memAuditor{e.mem}, log, releases.Config{BundleDir: e.bdir, KeepVersions: 10, ChunkBytes: 1 << 20})
	env, _ := sealed.NewEnvelope(make([]byte, 32))
	e.enr = enroll.New(e.mem, env)
	reg := registry.NewMemory()
	e.upg = upgrades.New(e.mem, e.rel, reg, events.HubPublisher{}, upgrades.Config{RequestTTL: time.Hour, ProgressTimeout: 15 * time.Minute})
	srv := ingest.New(e.enr, snapshots.New(e.mem, hosts.New(e.mem), events.HubPublisher{}), reg, e.mem, 0, "e2e").WithUpgrades(e.upg, e.rel, 4, 5*time.Minute)
	caPEM, certFile, keyFile := tlsFiles(t)
	e.ca = caPEM
	loader, err := ingest.NewCertLoader(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	gs := ingest.NewGRPCServer(srv, loader.TransportOption())
	// Reachable from the containers through the Docker host gateway.
	lis, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	e.port = lis.Addr().(*net.TCPAddr).Port
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return e
}

func tlsFiles(t *testing.T) (caPEM []byte, certFile, keyFile string) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "e2e CA"}, NotBefore: time.Now().Add(-time.Hour),
		NotAfter: time.Now().Add(3 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, _ := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	ca, _ := x509.ParseCertificate(caDER)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: gatewayDNS}, NotBefore: time.Now().Add(-time.Minute),
		NotAfter: time.Now().Add(3 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{gatewayDNS}}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	keyDER, _ := x509.MarshalECPrivateKey(key)
	dir := t.TempDir()
	certFile, keyFile = filepath.Join(dir, "tls.crt"), filepath.Join(dir, "tls.key")
	_ = os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	_ = os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), certFile, keyFile
}

func (e *edge) request(t *testing.T, agentID, want string) store.AgentUpgrade {
	t.Helper()
	res, err := e.upg.Request(context.Background(), tenant, upgrades.Actor{Kind: upgrades.ActorUser, ID: "e2e"}, []string{agentID}, false)
	if err != nil || len(res.Created) != 1 || res.TargetVersion != want {
		t.Fatalf("request to %s = %+v, %v", want, res, err)
	}
	return res.Created[0]
}

func (e *edge) waitUpgrade(t *testing.T, c testcontainers.Container, u store.AgentUpgrade, want string, limit time.Duration) store.AgentUpgrade {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		got, err := e.mem.GetAgentUpgrade(context.Background(), tenant, u.ID)
		if err == nil && got.State == want {
			return got
		}
		if err == nil && !got.Active() {
			dumpJournal(t, c)
			t.Fatalf("upgrade to %s ended %s (%s), want %s", u.TargetVersion, got.State, got.Reason, want)
		}
		if time.Now().After(deadline) {
			dumpJournal(t, c)
			t.Fatalf("upgrade to %s still %s (%s) after %s, want %s", u.TargetVersion, got.State, got.Reason, limit, want)
		}
		time.Sleep(time.Second)
	}
}

func (e *edge) waitAgent(t *testing.T, ok func(upgrades.FleetEntry) bool, limit time.Duration, what string) upgrades.FleetEntry {
	t.Helper()
	deadline := time.Now().Add(limit)
	for {
		fleet, _, err := e.upg.Fleet(context.Background(), tenant, upgrades.FleetFilter{})
		if err == nil {
			for _, f := range fleet {
				if ok(f) {
					return f
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: not reached within %s (fleet %+v, err %v)", what, limit, fleet, err)
		}
		time.Sleep(time.Second)
	}
}

// ---- release artifacts ----

func findRepoRoot(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(strings.TrimSpace(string(out)))
}

func (e *edge) binPath(v string) string { return filepath.Join(e.build, "bin-"+v) }
func (e *edge) pkgPath(v, format string) string {
	return filepath.Join(e.build, "pkg-"+v+"."+format)
}

// buildAgent builds a linux/amd64 agent of version v that trusts the e2e key.
func (e *edge) buildAgent(t *testing.T, root, v string) {
	t.Helper()
	keys := keyID + ":" + base64.StdEncoding.EncodeToString(e.pub)
	cmd := exec.Command("go", "build", "-trimpath", "-o", e.binPath(v), "-ldflags",
		"-s -w -X main.version="+v+" -X github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease.productionKeys="+keys, "./cmd/inventory-agent")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build agent %s: %v\n%s", v, err, out)
	}
}

// brokenAgent is a "binary" that exits at start and never confirms.
func (e *edge) brokenAgent(t *testing.T, v string) {
	t.Helper()
	if err := os.WriteFile(e.binPath(v), []byte("#!/bin/sh\necho broken agent >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// pack builds the deb/rpm of version v with the repository's nfpm definition.
func (e *edge) pack(t *testing.T, root, v, format string) {
	t.Helper()
	stage := t.TempDir()
	if out, err := exec.Command("cp", "-r", filepath.Join(root, "packaging"), stage).CombinedOutput(); err != nil {
		t.Fatalf("stage packaging: %v %s", err, out)
	}
	_ = os.MkdirAll(filepath.Join(stage, "bin", "pkg"), 0o755)
	b, err := os.ReadFile(e.binPath(v))
	if err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(stage, "bin", "pkg", "inventory-agent"), b, 0o755)
	cmd := exec.Command("go", "run", nfpm, "package", "-f", "packaging/nfpm.yaml", "-p", format, "-t", e.pkgPath(v, format))
	cmd.Dir = stage
	cmd.Env = append(os.Environ(), "VERSION="+v, "ARCH=amd64")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("nfpm %s %s: %v\n%s", v, format, err, out)
	}
}

// bundle writes the dev-signed release directory of version v.
func (e *edge) bundle(t *testing.T, v string) {
	t.Helper()
	d := filepath.Join(e.bdir, v)
	_ = os.MkdirAll(d, 0o755)
	m := agentrelease.Manifest{Schema: 1, Version: v, CreatedAt: time.Now().UTC().Truncate(time.Second), KeyID: keyID}
	for _, format := range []string{"deb", "rpm"} {
		data, err := os.ReadFile(e.pkgPath(v, format))
		if err != nil {
			t.Fatal(err)
		}
		name := "inventory-agent-linux-amd64." + format
		_ = os.WriteFile(filepath.Join(d, name), data, 0o644)
		sum := sha256.Sum256(data)
		m.Artifacts = append(m.Artifacts, agentrelease.Artifact{OS: "linux", Arch: "amd64", InstallType: format, File: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	b := m.Encode()
	_ = os.WriteFile(filepath.Join(d, agentrelease.ManifestFile), b, 0o644)
	_ = os.WriteFile(filepath.Join(d, agentrelease.SignatureFile), []byte(agentrelease.EncodeSignature(ed25519.Sign(e.priv, b))), 0o644)
}

func (e *edge) corrupt(t *testing.T, v, format string) {
	t.Helper()
	rel, err := e.mem.GetAgentRelease(context.Background(), v)
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range rel.Artifacts {
		if a.InstallType == format {
			e.mem.CorruptArtifactChunk(a, 0)
			return
		}
	}
	t.Fatalf("no %s artifact in %s", format, v)
}

// forget removes a release from the bundle and the store (if present).
func (e *edge) forget(v string) {
	_ = os.RemoveAll(filepath.Join(e.bdir, v))
	_ = e.mem.DeleteAgentRelease(context.Background(), v)
	e.rel.SeedBundles(context.Background())
}

// ---- containers ----

func startSystemd(t *testing.T, d distro) testcontainers.Container {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte(d.image), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			FromDockerfile: testcontainers.FromDockerfile{Context: dir, Repo: "inventory-e2e-" + d.name, Tag: "latest", KeepImage: true},
			HostConfigModifier: func(hc *container.HostConfig) {
				hc.Privileged = true
				hc.Tmpfs = map[string]string{"/run": "rw", "/run/lock": "rw"}
				hc.CgroupnsMode = "host"
				hc.Binds = append(hc.Binds, "/sys/fs/cgroup:/sys/fs/cgroup:rw")
				hc.ExtraHosts = append(hc.ExtraHosts, gatewayDNS+":host-gateway")
			},
			WaitingFor: wait.ForExec([]string{"systemctl", "is-system-running", "--wait"}).
				WithExitCodeMatcher(func(int) bool { return true }).WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("systemd container unavailable (Docker with privileged containers is required): %v", err)
	}
	t.Cleanup(func() { _ = c.Terminate(ctx) })
	return c
}

func execIn(t *testing.T, c testcontainers.Container, cmd string) (int, string) {
	t.Helper()
	code, r, err := c.Exec(context.Background(), []string{"sh", "-c", cmd}, tcexec.Multiplexed())
	if err != nil {
		t.Fatalf("exec %s: %v", cmd, err)
	}
	out, _ := io.ReadAll(r)
	return code, string(out)
}

func copyTo(t *testing.T, c testcontainers.Container, src, dst string) {
	t.Helper()
	if err := c.CopyFileToContainer(context.Background(), src, dst, 0o644); err != nil {
		t.Fatal(err)
	}
}

func copyBytes(t *testing.T, c testcontainers.Container, b []byte, dst string, mode int64) {
	t.Helper()
	if err := c.CopyToContainer(context.Background(), b, dst, mode); err != nil {
		t.Fatal(err)
	}
}

func dumpJournal(t *testing.T, c testcontainers.Container) {
	t.Helper()
	_, out := execIn(t, c, "journalctl --no-pager -n 200 -u inventory-agent.service -u 'inventory-agent-upgrade-*'")
	t.Log("journal:\n" + out)
}
