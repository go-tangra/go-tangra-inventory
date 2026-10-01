package config

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCertDeliveryDefaultsAndBounds(t *testing.T) {
	c := valid()
	d := c.CertDelivery
	if d.Enabled || len(d.Sources) != 1 || d.Sources[0] != "deployer" || d.LCMService != "lcm" || d.PendingTTLHours != 168 ||
		d.ReportTimeoutMinutes != 15 || d.MaxConcurrentFetches != 50 || d.LCMTimeoutSeconds != 10 || d.AllowPlaintextIngest {
		t.Fatalf("defaults = %+v (server delivery must be off by default)", d)
	}
	if d.PendingTTL() != 168*time.Hour || d.ReportTimeout() != 15*time.Minute || d.LCMTimeout() != 10*time.Second {
		t.Fatalf("durations = %s %s %s", d.PendingTTL(), d.ReportTimeout(), d.LCMTimeout())
	}
	if !d.IsSource("deployer") || d.IsSource("ipam") || d.IsSource("") {
		t.Fatal("IsSource")
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*CertDelivery){
		"ttl low":           func(d *CertDelivery) { d.PendingTTLHours = 0 },
		"ttl high":          func(d *CertDelivery) { d.PendingTTLHours = 721 },
		"report low":        func(d *CertDelivery) { d.ReportTimeoutMinutes = 4 },
		"report high":       func(d *CertDelivery) { d.ReportTimeoutMinutes = 121 },
		"fetches low":       func(d *CertDelivery) { d.MaxConcurrentFetches = 0 },
		"fetches high":      func(d *CertDelivery) { d.MaxConcurrentFetches = 501 },
		"lcm timeout low":   func(d *CertDelivery) { d.LCMTimeoutSeconds = 1 },
		"lcm timeout high":  func(d *CertDelivery) { d.LCMTimeoutSeconds = 61 },
		"lcm service empty": func(d *CertDelivery) { d.LCMService = "" },
		"lcm service bad":   func(d *CertDelivery) { d.LCMService = "LCM:9945" },
		"source bad":        func(d *CertDelivery) { d.Sources = []string{"svc/deployer"} },
		"sources too many": func(d *CertDelivery) {
			d.Sources = nil
			for i := 0; i < 33; i++ {
				d.Sources = append(d.Sources, "s")
			}
		},
	} {
		c := valid()
		mut(&c.CertDelivery)
		if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "cert_delivery") {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Enabled with no source is allowed (nothing can create deliveries).
	c = valid()
	c.CertDelivery.Enabled, c.CertDelivery.Sources = true, nil
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestCertDeliveryPlaintextIngest(t *testing.T) {
	c := valid()
	c.CertDelivery.AllowPlaintextIngest = true
	if err := c.Validate(); err != nil {
		t.Fatalf("dev: %v", err)
	}
	found := false
	for _, w := range c.Warnings() {
		if strings.Contains(w, "cert_delivery.allow_plaintext_ingest") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no startup warning: %v", c.Warnings())
	}
	c.Env = "production"
	c.DB.DSN = "postgres://h/db?sslmode=verify-full"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "allow_plaintext_ingest") {
		t.Fatalf("production: %v", err)
	}
	c.CertDelivery.AllowPlaintextIngest = false
	for _, w := range c.Warnings() {
		if strings.Contains(w, "cert_delivery") {
			t.Fatalf("warning without opt-out: %s", w)
		}
	}
}

func TestCertDeliveryLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inventory.yaml")
	if err := os.WriteFile(path, []byte("cert_delivery:\n  enabled: true\n  sources: [deployer, other]\n  max_concurrent_fetches: 7\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || !c.CertDelivery.Enabled || len(c.CertDelivery.Sources) != 2 || c.CertDelivery.MaxConcurrentFetches != 7 || c.CertDelivery.LCMService != "lcm" {
		t.Fatalf("loaded = %+v %v", c.CertDelivery, err)
	}
	if err := os.WriteFile(path, []byte("cert_delivery:\n  key_dir: /tmp\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("unknown cert_delivery key accepted")
	}
}

func agentWithCerts() AgentConfig {
	a := DefaultAgent()
	a.IngestEndpoint, a.TokenFile = "x:1", "/t"
	return a
}

func TestAgentCertificatesDefaults(t *testing.T) {
	a := agentWithCerts()
	c := a.Certificates
	if !c.Enabled || c.Directory != "/etc/inventory-agent/certs" || c.Owner != "root" || c.Group != "root" || c.DirMode != "0750" ||
		c.CertMode != "0644" || c.KeyMode != "0600" || c.KeepPrevious != 1 || c.DeployHook != "" || c.HookTimeoutSeconds != 300 ||
		c.AllowInsecureTransport {
		t.Fatalf("defaults = %+v", c)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	dm, cm, km := c.Modes()
	if dm != 0o750 || cm != 0o644 || km != 0o600 || c.HookTimeout() != 5*time.Minute {
		t.Fatalf("modes = %o %o %o %s", dm, cm, km, c.HookTimeout())
	}
	if len(a.Warnings()) != 0 {
		t.Fatalf("warnings by default: %v", a.Warnings())
	}
}

func TestAgentCertificatesRules(t *testing.T) {
	ok := map[string]func(*AgentCertificates){
		"disabled":        func(c *AgentCertificates) { c.Enabled = false },
		"other dir":       func(c *AgentCertificates) { c.Directory = "/srv/certs" },
		"var dir":         func(c *AgentCertificates) { c.Directory = "/var/lib/inventory-agent/certs" },
		"homes lookalike": func(c *AgentCertificates) { c.Directory = "/homes/certs" },
		"numeric owner":   func(c *AgentCertificates) { c.Owner, c.Group = "0", "33" },
		"named owner":     func(c *AgentCertificates) { c.Owner, c.Group = "nginx", "ssl-cert" },
		"dir 0700":        func(c *AgentCertificates) { c.DirMode = "0700" },
		"dir 0755":        func(c *AgentCertificates) { c.DirMode = "0755" },
		"dir no zero":     func(c *AgentCertificates) { c.DirMode = "750" },
		"cert 0600":       func(c *AgentCertificates) { c.CertMode = "0600" },
		"cert 0444":       func(c *AgentCertificates) { c.CertMode = "0444" },
		"key 0640":        func(c *AgentCertificates) { c.KeyMode = "0640" },
		"keep 0":          func(c *AgentCertificates) { c.KeepPrevious = 0 },
		"keep 5":          func(c *AgentCertificates) { c.KeepPrevious = 5 },
		"hook":            func(c *AgentCertificates) { c.DeployHook = "/usr/local/sbin/reload-nginx" },
		"hook 30s":        func(c *AgentCertificates) { c.HookTimeoutSeconds = 30 },
		"hook 1800s":      func(c *AgentCertificates) { c.HookTimeoutSeconds = 1800 },
	}
	for name, mut := range ok {
		a := agentWithCerts()
		mut(&a.Certificates)
		if err := a.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	bad := map[string]func(*AgentCertificates){
		"relative dir":   func(c *AgentCertificates) { c.Directory = "certs" },
		"empty dir":      func(c *AgentCertificates) { c.Directory = "" },
		"root dir":       func(c *AgentCertificates) { c.Directory = "/" },
		"unclean dir":    func(c *AgentCertificates) { c.Directory = "/etc/inventory-agent/../certs" },
		"trailing slash": func(c *AgentCertificates) { c.Directory = "/etc/certs/" },
		"home":           func(c *AgentCertificates) { c.Directory = "/home/alice/certs" },
		"home itself":    func(c *AgentCertificates) { c.Directory = "/home" },
		"root home":      func(c *AgentCertificates) { c.Directory = "/root/certs" },
		"tmp":            func(c *AgentCertificates) { c.Directory = "/tmp/certs" },
		"proc":           func(c *AgentCertificates) { c.Directory = "/proc/self" },
		"sys":            func(c *AgentCertificates) { c.Directory = "/sys/certs" },
		"dev":            func(c *AgentCertificates) { c.Directory = "/dev/shm/certs" },
		"bad owner":      func(c *AgentCertificates) { c.Owner = "Root" },
		"owner path":     func(c *AgentCertificates) { c.Owner = "../root" },
		"empty owner":    func(c *AgentCertificates) { c.Owner = "" },
		"bad group":      func(c *AgentCertificates) { c.Group = "a b" },
		"huge uid":       func(c *AgentCertificates) { c.Owner = "99999999999" },
		"dir not octal":  func(c *AgentCertificates) { c.DirMode = "0789" },
		"dir decimal":    func(c *AgentCertificates) { c.DirMode = "488" },
		"dir world w":    func(c *AgentCertificates) { c.DirMode = "0757" },
		"dir group w":    func(c *AgentCertificates) { c.DirMode = "0770" },
		"dir no owner x": func(c *AgentCertificates) { c.DirMode = "0600" },
		"dir empty":      func(c *AgentCertificates) { c.DirMode = "" },
		"dir setuid":     func(c *AgentCertificates) { c.DirMode = "4750" },
		"cert group w":   func(c *AgentCertificates) { c.CertMode = "0664" },
		"cert world w":   func(c *AgentCertificates) { c.CertMode = "0646" },
		"cert no read":   func(c *AgentCertificates) { c.CertMode = "0244" },
		"cert garbage":   func(c *AgentCertificates) { c.CertMode = "rw-r--r--" },
		"key 0644":       func(c *AgentCertificates) { c.KeyMode = "0644" },
		"key 0400":       func(c *AgentCertificates) { c.KeyMode = "0400" },
		"key 0660":       func(c *AgentCertificates) { c.KeyMode = "0660" },
		"keep -1":        func(c *AgentCertificates) { c.KeepPrevious = -1 },
		"keep 6":         func(c *AgentCertificates) { c.KeepPrevious = 6 },
		"hook relative":  func(c *AgentCertificates) { c.DeployHook = "reload.sh" },
		"hook unclean":   func(c *AgentCertificates) { c.DeployHook = "/usr/local/../bin/x" },
		"hook dir":       func(c *AgentCertificates) { c.DeployHook = "/" },
		"timeout low":    func(c *AgentCertificates) { c.HookTimeoutSeconds = 29 },
		"timeout high":   func(c *AgentCertificates) { c.HookTimeoutSeconds = 1801 },
	}
	for name, mut := range bad {
		a := agentWithCerts()
		mut(&a.Certificates)
		if err := a.Validate(); err == nil || !strings.Contains(err.Error(), "certificates.") {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	// A disabled section is still validated (a typo must not pass silently).
	a := agentWithCerts()
	a.Certificates.Enabled, a.Certificates.Directory = false, "/tmp/x"
	if err := a.Validate(); err == nil {
		t.Error("disabled section with a bad directory accepted")
	}
}

func TestAgentCertificatesTransport(t *testing.T) {
	a := agentWithCerts()
	a.Insecure = true
	if a.Certificates.Announce("linux", a.Insecure) {
		t.Fatal("cert.v1 announced over plaintext without the local opt-out")
	}
	a.Certificates.AllowInsecureTransport = true
	if !a.Certificates.Announce("linux", a.Insecure) {
		t.Fatal("opt-out not honoured")
	}
	if w := a.Warnings(); len(w) != 1 || !strings.Contains(w[0], "allow_insecure_transport") {
		t.Fatalf("warnings = %v", w)
	}
	if a.Certificates.Announce("windows", false) {
		t.Fatal("announced on windows")
	}
	a.Certificates.Enabled = false
	if a.Certificates.Announce("linux", false) {
		t.Fatal("announced while disabled")
	}
	b := agentWithCerts()
	if !b.Certificates.Announce("linux", false) {
		t.Fatal("not announced with TLS")
	}
	b.Certificates.AllowInsecureTransport = true // without insecure: no warning
	if len(b.Warnings()) != 0 {
		t.Fatalf("warnings = %v", b.Warnings())
	}
}

func TestAgentCertificatesLoad(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.yaml")
	body := "ingest_endpoint: x:1\ntoken_file: /t\ncertificates:\n  directory: /srv/certs\n  group: ssl-cert\n  key_mode: \"0640\"\n  deploy_hook: /usr/local/sbin/reload\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	a, err := LoadAgent(path)
	if err != nil || a.Certificates.Directory != "/srv/certs" || a.Certificates.Group != "ssl-cert" || a.Certificates.KeyMode != "0640" ||
		!a.Certificates.Enabled || a.Certificates.Owner != "root" {
		t.Fatalf("loaded = %+v %v", a.Certificates, err)
	}
	if err := a.Validate(); err != nil {
		t.Fatal(err)
	}
	_, _, km := a.Certificates.Modes()
	if km != fs.FileMode(0o640) {
		t.Fatalf("key mode %o", km)
	}
	for _, extra := range []string{"  hook_args: [x]\n", "  url: https://evil\n", "  owner_uid: 0\n"} {
		if err := os.WriteFile(path, []byte(body+extra), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadAgent(path); err == nil {
			t.Errorf("unknown certificates key accepted: %q", extra)
		}
	}
}
