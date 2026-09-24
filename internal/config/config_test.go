package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	fconfig "github.com/go-tangra/go-tangra/v4/config"
)

// valid returns a Config that passes both the framework and module Validate.
func valid() Config {
	c := Default()
	c.ServiceName = "inventory"
	c.TrustDomain = "example.org"
	c.Authz.Path = "/etc/inventory/policy.yaml"
	c.DB.DSN = "postgres://localhost/inv"
	c.Valkey.Addresses = []string{"valkey:6379"}
	c.KEK = KEK{Source: "file", Path: "/etc/inventory/kek"}
	c.Ingest.Addr = ":9500"
	c.Ingest.TLSCertFile = "/etc/inventory/ingest/tls.crt"
	c.Ingest.TLSKeyFile = "/etc/inventory/ingest/tls.key"
	c.Gateway.Issuer = "https://gw.example.org"
	return c
}

func TestDefaultSecure(t *testing.T) {
	d := Default()
	if d.KEK.Source != "file" {
		t.Errorf("kek.source default = %q, want file", d.KEK.Source)
	}
	if d.Valkey.AllowPlaintext {
		t.Error("valkey.allow_plaintext must default to false")
	}
	if d.Ingest.Insecure {
		t.Error("ingest.insecure must default to false")
	}
	if !d.Events.Enabled {
		t.Error("events.enabled must default to true")
	}
	if d.DB.MaxConns != 16 {
		t.Errorf("db.max_conns default = %d, want 16", d.DB.MaxConns)
	}
	if d.Registry.HeartbeatSeconds != 30 || d.Retention.Days != 90 || d.Stale.AfterSeconds != 86400 {
		t.Errorf("unexpected registry/retention/stale defaults: %+v %+v %+v", d.Registry, d.Retention, d.Stale)
	}
	if d.Jobs.Workers != 4 || d.Jobs.IntervalSeconds != 60 || d.Jobs.PurgeIntervalSeconds != 3600 {
		t.Errorf("unexpected jobs defaults: %+v", d.Jobs)
	}
	if d.Enroll.TokenTTLSeconds != 3600 {
		t.Errorf("enroll.token_ttl_seconds default = %d, want 3600", d.Enroll.TokenTTLSeconds)
	}
	if d.Gateway.Service != "gateway" {
		t.Errorf("gateway.service default = %q, want gateway", d.Gateway.Service)
	}
	if d.Limits.MaxRequestBytes != 1<<20 || d.Limits.MaxSnapshotBytes != 8<<20 {
		t.Errorf("unexpected limits defaults: %+v", d.Limits)
	}
	// A pristine Default() has no service_name/db and must not validate.
	if err := Default().Validate(); err == nil {
		t.Error("Default() must not validate without required fields")
	}
}

func TestValidateOK(t *testing.T) {
	if err := valid().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// env source is also accepted.
	c := valid()
	c.KEK = KEK{Source: "env", Env: "INV_KEK"}
	if err := c.Validate(); err != nil {
		t.Fatalf("env kek rejected: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*Config)
		want string
	}{
		{"framework", func(c *Config) { c.ServiceName = "" }, "service_name"},
		{"db missing", func(c *Config) { c.DB.DSN = "" }, "db.dsn"},
		{"db prod tls", func(c *Config) { c.Env = "production"; c.Valkey.CAFile = "ca" }, "sslmode"},
		{"valkey missing", func(c *Config) { c.Valkey.Addresses = nil }, "valkey.addresses"},
		{"valkey prod plaintext", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://h/db?sslmode=verify-full"
			c.Valkey.AllowPlaintext = true
		}, "allow_plaintext"},
		{"kek file no path", func(c *Config) { c.KEK = KEK{Source: "file"} }, "kek.path"},
		{"kek env no env", func(c *Config) { c.KEK = KEK{Source: "env"} }, "kek.env"},
		{"kek bad source", func(c *Config) { c.KEK = KEK{Source: "vault"} }, "kek.source"},
		{"ingest missing", func(c *Config) { c.Ingest.Addr = "" }, "ingest.addr"},
		{"ingest prod insecure", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://h/db?sslmode=verify-ca"
			c.Ingest.Insecure = true
		}, "ingest.insecure"},
		{"ingest prod insecure without cert", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://h/db?sslmode=verify-full"
			c.Ingest = Ingest{Addr: ":9977", Insecure: true}
		}, "ingest.insecure"},
		{"ingest prod tls without cert", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://h/db?sslmode=verify-full"
			c.Ingest = Ingest{Addr: ":9977"}
		}, "ingest.tls_cert_file"},
		{"ingest prod tls without key", func(c *Config) {
			c.Env = "production"
			c.DB.DSN = "postgres://h/db?sslmode=verify-full"
			c.Ingest.TLSKeyFile = ""
		}, "ingest.tls_key_file"},
		{"ingest dev tls without cert", func(c *Config) { c.Ingest = Ingest{Addr: ":9977"} }, "ingest.tls_cert_file"},
		{"ingest dev cert without key", func(c *Config) { c.Ingest.TLSKeyFile = "" }, "ingest.tls_key_file"},
		{"ingest dev key without cert", func(c *Config) { c.Ingest.TLSCertFile = "" }, "ingest.tls_cert_file"},
		{"ingest insecure with cert", func(c *Config) { c.Ingest.Insecure = true }, "contradict"},
		{"ingest reload negative", func(c *Config) { c.Ingest.TLSReloadSeconds = -1 }, "tls_reload_seconds"},
		{"ingest reload too long", func(c *Config) { c.Ingest.TLSReloadSeconds = 86401 }, "tls_reload_seconds"},
		{"heartbeat range", func(c *Config) { c.Registry.HeartbeatSeconds = 0 }, "heartbeat_seconds"},
		{"retention range", func(c *Config) { c.Retention.Days = 0 }, "retention.days"},
		{"stale range", func(c *Config) { c.Stale.AfterSeconds = 1 }, "stale.after_seconds"},
		{"workers range", func(c *Config) { c.Jobs.Workers = 0 }, "jobs.workers"},
		{"interval range", func(c *Config) { c.Jobs.IntervalSeconds = 0 }, "jobs.interval_seconds"},
		{"purge range", func(c *Config) { c.Jobs.PurgeIntervalSeconds = 0 }, "jobs.purge_interval_seconds"},
		{"gateway service", func(c *Config) { c.Gateway.Service = "" }, "gateway.service"},
		{"gateway issuer", func(c *Config) { c.Gateway.Issuer = "http://gw" }, "gateway.issuer"},
		{"enroll ttl", func(c *Config) { c.Enroll.TokenTTLSeconds = 1 }, "token_ttl_seconds"},
		{"max request", func(c *Config) { c.Limits.MaxRequestBytes = 1 }, "max_request_bytes"},
		{"max snapshot", func(c *Config) { c.Limits.MaxSnapshotBytes = 1 }, "max_snapshot_bytes"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mut(&c)
			err := c.Validate()
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestValidateProdOK(t *testing.T) {
	c := valid()
	c.Env = "production"
	c.DB.DSN = "postgres://h/db?sslmode=verify-full"
	if err := c.Validate(); err != nil {
		t.Fatalf("valid production config rejected: %v", err)
	}
}

func TestValidateIngestModes(t *testing.T) {
	// Development stack: plaintext ingest is an accepted, warned opt-out.
	c := valid()
	c.Ingest = Ingest{Addr: ":9977", Insecure: true}
	if err := c.Validate(); err != nil {
		t.Fatalf("dev insecure ingest rejected: %v", err)
	}
	// Production with a certificate and key is accepted.
	c = valid()
	c.Env = "production"
	c.DB.DSN = "postgres://h/db?sslmode=verify-full"
	c.Ingest.TLSReloadSeconds = 300
	if err := c.Validate(); err != nil {
		t.Fatalf("production TLS ingest rejected: %v", err)
	}
	if c.IngestTLSReload() != 5*time.Minute {
		t.Errorf("IngestTLSReload=%v", c.IngestTLSReload())
	}
	c.Ingest.TLSReloadSeconds = 0
	if c.IngestTLSReload() != time.Minute {
		t.Errorf("default IngestTLSReload=%v, want 1m", c.IngestTLSReload())
	}
}

func TestWarnings(t *testing.T) {
	c := valid()
	if w := c.Warnings(); len(w) != 0 {
		t.Fatalf("secure config produced warnings: %v", w)
	}
	c.Valkey.AllowPlaintext = true
	c.Ingest = Ingest{Addr: ":9977", Insecure: true}
	c.Admin.EnablePprof = true // framework warning path
	w := c.Warnings()
	joined := strings.Join(w, "\n")
	if !strings.Contains(joined, "valkey.allow_plaintext") {
		t.Errorf("missing valkey warning: %v", w)
	}
	if !strings.Contains(joined, "ingest.insecure") {
		t.Errorf("missing ingest warning: %v", w)
	}
	if !strings.Contains(joined, "pprof") {
		t.Errorf("framework warnings not surfaced: %v", w)
	}
}

func TestDurationHelpers(t *testing.T) {
	c := valid()
	c.Jobs.IntervalSeconds = 30
	c.Jobs.PurgeIntervalSeconds = 120
	c.Stale.AfterSeconds = 3600
	c.Registry.HeartbeatSeconds = 15
	c.Enroll.TokenTTLSeconds = 900
	c.Retention.Days = 7
	if c.Interval() != 30*time.Second {
		t.Errorf("Interval=%v", c.Interval())
	}
	if c.PurgeInterval() != 120*time.Second {
		t.Errorf("PurgeInterval=%v", c.PurgeInterval())
	}
	if c.StaleAfter() != time.Hour {
		t.Errorf("StaleAfter=%v", c.StaleAfter())
	}
	if c.Heartbeat() != 15*time.Second {
		t.Errorf("Heartbeat=%v", c.Heartbeat())
	}
	if c.EnrollTTL() != 15*time.Minute {
		t.Errorf("EnrollTTL=%v", c.EnrollTTL())
	}
	if c.RetentionWindow() != 7*24*time.Hour {
		t.Errorf("RetentionWindow=%v", c.RetentionWindow())
	}
}

func TestAddrHelpers(t *testing.T) {
	c := valid()
	c.Config.Server = fconfig.Server{GRPCAddr: ":1", HTTPAddr: ":2"}
	c.Config.Admin.Addr = ":3"
	c.Ingest.Addr = ":4"
	if c.GRPCAddr() != ":1" || c.HTTPAddr() != ":2" || c.AdminAddr() != ":3" || c.IngestAddr() != ":4" {
		t.Errorf("addr helpers: grpc=%q http=%q admin=%q ingest=%q", c.GRPCAddr(), c.HTTPAddr(), c.AdminAddr(), c.IngestAddr())
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "inventory.yaml")
	yaml := `
service_name: inventory
trust_domain: example.org
authz:
  source: file
  path: /etc/inventory/policy.yaml
db:
  dsn: postgres://localhost/inv
  max_conns: 8
valkey:
  addresses: ["valkey:6379"]
  allow_plaintext: true
kek:
  source: env
  env: INV_KEK
ingest:
  addr: ":9500"
  tls_cert_file: /etc/inventory/ingest/tls.crt
  tls_key_file: /etc/inventory/ingest/tls.key
  tls_reload_seconds: 120
registry:
  instance_id: inv-1
  heartbeat_seconds: 10
retention:
  days: 30
stale:
  after_seconds: 7200
jobs:
  workers: 8
  interval_seconds: 15
  purge_interval_seconds: 600
events:
  enabled: false
gateway:
  service: gateway
  issuer: https://gw.example.org
enroll:
  token_ttl_seconds: 1800
limits_inventory:
  max_request_bytes: 2097152
  max_snapshot_bytes: 16777216
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.DB.MaxConns != 8 || c.Registry.InstanceID != "inv-1" || c.Jobs.Workers != 8 {
		t.Errorf("unexpected loaded values: %+v %+v %+v", c.DB, c.Registry, c.Jobs)
	}
	if c.Ingest.TLSCertFile != "/etc/inventory/ingest/tls.crt" || c.Ingest.TLSKeyFile != "/etc/inventory/ingest/tls.key" || c.IngestTLSReload() != 2*time.Minute {
		t.Errorf("ingest tls fields not loaded: %+v", c.Ingest)
	}
	if !c.Valkey.AllowPlaintext || c.Ingest.Insecure || c.Events.Enabled {
		t.Errorf("bool fields not loaded: %+v %+v %+v", c.Valkey, c.Ingest, c.Events)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("loaded config invalid: %v", err)
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("Load of missing file must error")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("db:\n  unknown_field: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(bad); err == nil {
		t.Error("Load with unknown field must error")
	}
}

func TestAgentConfig(t *testing.T) {
	d := DefaultAgent()
	if d.IntervalSeconds != 3600 || d.Insecure {
		t.Errorf("unexpected agent defaults: %+v", d)
	}
	if err := d.Validate(); err == nil {
		t.Error("empty agent config must not validate")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "agent.yaml")
	yaml := `
ingest_endpoint: https://inv.example.org:9500
interval_seconds: 300
token_file: /var/lib/inv/token
credential_file: /var/lib/inv/cred
state_file: /var/lib/inv/state
insecure: true
`
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAgent(path)
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if a.IngestEndpoint == "" || a.IntervalSeconds != 300 || !a.Insecure {
		t.Errorf("unexpected agent values: %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("agent config invalid: %v", err)
	}
	if a.AgentInterval() != 5*time.Minute {
		t.Errorf("AgentInterval=%v", a.AgentInterval())
	}

	// Validate reject cases.
	bad := DefaultAgent()
	bad.IngestEndpoint = "https://x"
	bad.IntervalSeconds = 1
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "interval_seconds") {
		t.Errorf("interval reject: %v", err)
	}
	bad = DefaultAgent()
	bad.IngestEndpoint = "https://x"
	if err := bad.Validate(); err == nil || !strings.Contains(err.Error(), "token_file") {
		t.Errorf("credential reject: %v", err)
	}
}

func TestAgentConfigTLS(t *testing.T) {
	dir := t.TempDir()
	caFile := filepath.Join(dir, "ca.pem")
	// Any well-formed PEM CERTIFICATE block is enough for Validate's parse check;
	// generate one in-test rather than committing a certificate.
	if err := os.WriteFile(caFile, selfSignedPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	notPEM := filepath.Join(dir, "junk.pem")
	if err := os.WriteFile(notPEM, []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, "agent.yaml")
	yaml := "ingest_endpoint: inv.example.org:9977\ntoken_file: /t\nca_file: " + caFile + "\nserver_name: inv.example.org\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAgent(path)
	if err != nil {
		t.Fatalf("LoadAgent: %v", err)
	}
	if a.CAFile != caFile || a.ServerName != "inv.example.org" {
		t.Fatalf("tls fields not loaded: %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("pinned-CA agent config rejected: %v", err)
	}

	cases := []struct {
		name string
		mut  func(*AgentConfig)
		want string
	}{
		{"ca_file missing", func(a *AgentConfig) { a.CAFile = filepath.Join(dir, "missing.pem") }, "ca_file"},
		{"ca_file not pem", func(a *AgentConfig) { a.CAFile = notPEM }, "ca_file"},
		{"insecure with ca_file", func(a *AgentConfig) { a.Insecure = true }, "insecure"},
		{"insecure with server_name", func(a *AgentConfig) { a.Insecure = true; a.CAFile = "" }, "insecure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := a
			tc.mut(&b)
			if err := b.Validate(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want containing %q", err, tc.want)
			}
		})
	}
}

func TestLoadAgentErrors(t *testing.T) {
	if _, err := LoadAgent(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("LoadAgent of missing file must error")
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("nope: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadAgent(bad); err == nil {
		t.Error("LoadAgent with unknown field must error")
	}
}

// selfSignedPEM returns a freshly generated self-signed CA certificate as PEM.
func selfSignedPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "agent test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
