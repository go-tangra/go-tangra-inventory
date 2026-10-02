// Package config loads and validates the inventory service configuration: the
// Freya framework config plus the module's own sections. Every value is
// explicit; insecure opt-outs are named and surfaced at start (Constitution
// I/VII). The off-mesh ingest listener, the Valkey-backed live registry, the
// snapshot/retention jobs and the endpoint agent all read from here.
package config

import (
	"bytes"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	fconfig "github.com/go-tangra/go-tangra/v4/config"
	"gopkg.in/yaml.v3"
)

// Config is the inventory service configuration. The embedded framework config
// (inline) already carries the service_name, trust_domain, env, identity,
// authz, limits, admin, discovery and server (grpc_addr/http_addr) sections;
// the fields below are the module's own. The off-mesh ingest listener lives in
// its own "ingest" section because it is a second, non-mesh transport that the
// framework's single "server" section does not model.
type Config struct {
	fconfig.Config `yaml:",inline"`

	DB         DB         `yaml:"db"`
	Valkey     Valkey     `yaml:"valkey"`
	KEK        KEK        `yaml:"kek"`
	Ingest     Ingest     `yaml:"ingest"`
	Registry   Registry   `yaml:"registry"`
	Retention  Retention  `yaml:"retention"`
	Stale      Stale      `yaml:"stale"`
	Jobs       Jobs       `yaml:"jobs"`
	Events     Events     `yaml:"events"`
	Gateway    Gateway    `yaml:"gateway"`
	Enroll     Enroll     `yaml:"enroll"`
	MeshEnroll MeshEnroll `yaml:"mesh_enroll"`
	Limits     Limits     `yaml:"limits_inventory"`
	// HostReports configures the mesh-only HostReportService (feature 020).
	HostReports HostReports `yaml:"host_reports"`
	// AgentReleases configures the signed agent releases offered to agents
	// for self-upgrade and the upgrade request lifecycle (feature 023).
	AgentReleases AgentReleases `yaml:"agent_releases"`
	// CertDelivery relays lcm certificates to inventory agents for the
	// deployer (feature 033); off by default.
	CertDelivery CertDelivery `yaml:"cert_delivery"`
}

// CertDelivery configures the certificate delivery relay (feature 033).
// Sources are the mesh service names (last SPIFFE path segment) allowed to
// create deliveries, on top of the inbound mesh policy; LCMService is the
// discovery name of lcm for Certificates/Download. AllowPlaintextIngest lets
// FetchCertificate serve material over a plaintext ingest edge (development
// stack only; refused in production).
type CertDelivery struct {
	Enabled              bool     `yaml:"enabled"`
	Sources              []string `yaml:"sources"`
	LCMService           string   `yaml:"lcm_service"`
	PendingTTLHours      int      `yaml:"pending_ttl_hours"`
	ReportTimeoutMinutes int      `yaml:"report_timeout_minutes"`
	MaxConcurrentFetches int      `yaml:"max_concurrent_fetches"`
	LCMTimeoutSeconds    int      `yaml:"lcm_timeout_seconds"`
	AllowPlaintextIngest bool     `yaml:"allow_plaintext_ingest"`
}

// IsSource reports whether the mesh service may create deliveries.
func (d CertDelivery) IsSource(service string) bool {
	for _, s := range d.Sources {
		if s == service && service != "" {
			return true
		}
	}
	return false
}

// PendingTTL bounds how long a delivery item waits for its agent.
func (d CertDelivery) PendingTTL() time.Duration {
	return time.Duration(d.PendingTTLHours) * time.Hour
}

// ReportTimeout fails a fetched item whose agent does not report.
func (d CertDelivery) ReportTimeout() time.Duration {
	return time.Duration(d.ReportTimeoutMinutes) * time.Minute
}

// LCMTimeout bounds one Certificates/Download call.
func (d CertDelivery) LCMTimeout() time.Duration {
	return time.Duration(d.LCMTimeoutSeconds) * time.Second
}

func (d CertDelivery) validate(prod bool) error {
	if len(d.Sources) > 32 {
		return errors.New("config: cert_delivery.sources allows at most 32 services")
	}
	for _, s := range d.Sources {
		if !serviceNameRE.MatchString(s) {
			return fmt.Errorf("config: cert_delivery.sources entry %q is not a service name", s)
		}
	}
	switch {
	case !serviceNameRE.MatchString(d.LCMService):
		return errors.New("config: cert_delivery.lcm_service must be a service name")
	case d.PendingTTLHours < 1 || d.PendingTTLHours > 720:
		return errors.New("config: cert_delivery.pending_ttl_hours must be within [1, 720]")
	case d.ReportTimeoutMinutes < 5 || d.ReportTimeoutMinutes > 120:
		return errors.New("config: cert_delivery.report_timeout_minutes must be within [5, 120]")
	case d.MaxConcurrentFetches < 1 || d.MaxConcurrentFetches > 500:
		return errors.New("config: cert_delivery.max_concurrent_fetches must be within [1, 500]")
	case d.LCMTimeoutSeconds < 2 || d.LCMTimeoutSeconds > 60:
		return errors.New("config: cert_delivery.lcm_timeout_seconds must be within [2, 60]")
	case prod && d.AllowPlaintextIngest:
		return errors.New("config: cert_delivery.allow_plaintext_ingest is not permitted in production")
	}
	return nil
}

// AgentReleases configures agent self-upgrade on the server. BundleDir holds
// the signed releases shipped in the image (<version>/ with manifest,
// signature and artifacts; "" = none), verified and copied into PostgreSQL
// at start; KeepVersions bounds the stored releases (the platform current
// version, tenant pins and active targets are always kept).
type AgentReleases struct {
	BundleDir              string `yaml:"bundle_dir"`
	KeepVersions           int    `yaml:"keep_versions"`
	MaxConcurrentDownloads int    `yaml:"max_concurrent_downloads"`
	ChunkBytes             int    `yaml:"chunk_bytes"`
	RequestTTLHours        int    `yaml:"request_ttl_hours"`
	ProgressTimeoutMinutes int    `yaml:"progress_timeout_minutes"`
}

func (r AgentReleases) validate() error {
	switch {
	case r.BundleDir != "" && !filepath.IsAbs(r.BundleDir):
		return errors.New("config: agent_releases.bundle_dir must be an absolute path")
	case r.KeepVersions < 2 || r.KeepVersions > 50:
		return errors.New("config: agent_releases.keep_versions must be within [2, 50]")
	case r.MaxConcurrentDownloads < 1 || r.MaxConcurrentDownloads > 500:
		return errors.New("config: agent_releases.max_concurrent_downloads must be within [1, 500]")
	case r.ChunkBytes < 64<<10 || r.ChunkBytes > 1<<20:
		return errors.New("config: agent_releases.chunk_bytes must be within [65536, 1048576]")
	case r.RequestTTLHours < 1 || r.RequestTTLHours > 720:
		return errors.New("config: agent_releases.request_ttl_hours must be within [1, 720]")
	case r.ProgressTimeoutMinutes < 5 || r.ProgressTimeoutMinutes > 120:
		return errors.New("config: agent_releases.progress_timeout_minutes must be within [5, 120]")
	}
	return nil
}

// HostReports configures HostReportService. Consumers are the mesh service
// names (last SPIFFE path segment, e.g. "ipam") allowed to call it; the
// inbound policy must admit them as well. MaxPageBytes bounds the encoded
// size of one ListHostReports page.
type HostReports struct {
	Consumers    []string `yaml:"consumers"`
	MaxPageBytes int      `yaml:"max_page_bytes"`
}

// IsConsumer reports whether service may call HostReportService.
func (h HostReports) IsConsumer(service string) bool {
	for _, c := range h.Consumers {
		if c == service && service != "" {
			return true
		}
	}
	return false
}

var serviceNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

func (h HostReports) validate() error {
	if len(h.Consumers) > 32 {
		return errors.New("config: host_reports.consumers allows at most 32 services")
	}
	for _, c := range h.Consumers {
		if !serviceNameRE.MatchString(c) {
			return fmt.Errorf("config: host_reports.consumers entry %q is not a service name", c)
		}
	}
	if h.MaxPageBytes < 64<<10 || h.MaxPageBytes > 7<<20 {
		return errors.New("config: host_reports.max_page_bytes must be within [64 KiB, 7 MiB]")
	}
	return nil
}

// DB configures TimescaleDB.
type DB struct {
	DSN        string `yaml:"dsn"`
	MigrateDSN string `yaml:"migrate_dsn"`
	MaxConns   int32  `yaml:"max_conns"`
}

// Valkey configures the platform event bus and the live agent registry.
type Valkey struct {
	Addresses      []string `yaml:"addresses"`
	Username       string   `yaml:"username"`
	Password       string   `yaml:"password"`
	AllowPlaintext bool     `yaml:"allow_plaintext"`
	CAFile         string   `yaml:"ca_file"`
}

// KEK names where the 32-byte key-encryption key (agent credentials, token
// secrets) comes from.
type KEK struct {
	Source string `yaml:"source"` // file | env
	Path   string `yaml:"path"`
	Env    string `yaml:"env"`
}

// Ingest is the off-mesh listener endpoint agents post snapshots to. It is a
// second transport separate from the mesh grpc_addr/http_addr; agents present a
// bearer credential rather than a mesh SVID, so the listener serves ordinary
// server-authenticated TLS (no client certificate) from tls_cert_file and
// tls_key_file. The pair is re-read every tls_reload_seconds (default 60), so a
// renewed certificate needs no restart. insecure is the explicit plaintext
// opt-out for the development stack and is refused in production.
type Ingest struct {
	Addr             string `yaml:"addr"`
	Insecure         bool   `yaml:"insecure"`
	TLSCertFile      string `yaml:"tls_cert_file"`
	TLSKeyFile       string `yaml:"tls_key_file"`
	TLSReloadSeconds int    `yaml:"tls_reload_seconds"`
}

// defaultIngestTLSReload is how often the ingest certificate is re-read when
// tls_reload_seconds is unset.
const defaultIngestTLSReload = time.Minute

// Registry configures the Valkey-backed live-connection registry (online/offline
// keyed by agent id). instance_id names this service replica.
type Registry struct {
	InstanceID       string `yaml:"instance_id"`
	HeartbeatSeconds int    `yaml:"heartbeat_seconds"`
}

// Retention bounds how long snapshots are kept (each host keeps its latest).
type Retention struct {
	Days int `yaml:"days"`
}

// Stale marks a host with no snapshot within after_seconds as "stale".
type Stale struct {
	AfterSeconds int `yaml:"after_seconds"`
}

// Jobs configures the background worker pool (stale marking, retention purge).
type Jobs struct {
	Workers              int `yaml:"workers"`
	IntervalSeconds      int `yaml:"interval_seconds"`
	PurgeIntervalSeconds int `yaml:"purge_interval_seconds"`
}

// Events toggles the realtime publisher.
type Events struct {
	Enabled bool `yaml:"enabled"`
}

// Gateway names the application gateway and the platform token issuer.
type Gateway struct {
	Service string `yaml:"service"`
	Issuer  string `yaml:"issuer"`
}

// Enroll carries enrollment-token defaults (minted token lifetime).
type Enroll struct {
	TokenTTLSeconds int `yaml:"token_ttl_seconds"`
}

// MeshEnroll configures how the inventory SERVER obtains its own mesh SPIFFE
// SVID by enrolling with lcm over the network (identity.provider=provided). This
// is distinct from Enroll, which is the lifetime of enrollment tokens minted for
// off-mesh endpoint agents.
type MeshEnroll struct {
	Enabled       bool   `yaml:"enabled"`
	EnrollURL     string `yaml:"enroll_url"`
	LCMGRPCTarget string `yaml:"lcm_grpc"`
	TenantID      string `yaml:"tenant_id"`
	TokenFile     string `yaml:"token_file"`
	StateFile     string `yaml:"state_file"`
	Insecure      bool   `yaml:"insecure"`
}

// Limits bound the module's request shapes.
type Limits struct {
	MaxRequestBytes  int64 `yaml:"max_request_bytes"`
	MaxSnapshotBytes int64 `yaml:"max_snapshot_bytes"`
}

// Default returns secure defaults on top of the Freya defaults.
func Default() Config {
	return Config{
		Config:      fconfig.Default(),
		DB:          DB{MaxConns: 16},
		KEK:         KEK{Source: "file"},
		Ingest:      Ingest{},
		Registry:    Registry{HeartbeatSeconds: 30},
		Retention:   Retention{Days: 90},
		Stale:       Stale{AfterSeconds: 86400},
		Jobs:        Jobs{Workers: 4, IntervalSeconds: 60, PurgeIntervalSeconds: 3600},
		Events:      Events{Enabled: true},
		Gateway:     Gateway{Service: "gateway"},
		Enroll:      Enroll{TokenTTLSeconds: 3600},
		Limits:      Limits{MaxRequestBytes: 1 << 20, MaxSnapshotBytes: 8 << 20},
		HostReports: HostReports{Consumers: []string{"ipam", "asset"}, MaxPageBytes: 3 << 20},
		AgentReleases: AgentReleases{BundleDir: "/app/agent-releases", KeepVersions: 5, MaxConcurrentDownloads: 20,
			ChunkBytes: 1 << 20, RequestTTLHours: 168, ProgressTimeoutMinutes: 15},
		CertDelivery: CertDelivery{Sources: []string{"deployer"}, LCMService: "lcm", PendingTTLHours: 168,
			ReportTimeoutMinutes: 15, MaxConcurrentFetches: 50, LCMTimeoutSeconds: 10},
	}
}

// Load reads YAML over Default(); unknown fields are rejected.
func Load(path string) (Config, error) {
	cfg := Default()
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return cfg, fmt.Errorf("config: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// Validate checks the Freya config and every module section. It refuses a
// missing db, kek or ingest listener outright.
func (c Config) Validate() error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	prod := c.IsProduction()
	if c.DB.DSN == "" {
		return errors.New("config: db.dsn is required")
	}
	if prod && !strings.Contains(c.DB.DSN, "sslmode=verify-full") && !strings.Contains(c.DB.DSN, "sslmode=verify-ca") {
		return errors.New("config: db.dsn must use sslmode=verify-full (or verify-ca) in production")
	}
	if len(c.Valkey.Addresses) == 0 {
		return errors.New("config: valkey.addresses is required")
	}
	if prod && c.Valkey.AllowPlaintext {
		return errors.New("config: valkey.allow_plaintext is not permitted in production")
	}
	switch c.KEK.Source {
	case "file":
		if c.KEK.Path == "" {
			return errors.New("config: kek.path is required for kek.source file")
		}
	case "env":
		if c.KEK.Env == "" {
			return errors.New("config: kek.env is required for kek.source env")
		}
	default:
		return errors.New("config: kek.source must be file or env")
	}
	if c.Ingest.Addr == "" {
		return errors.New("config: ingest.addr is required (the off-mesh ingest listener)")
	}
	if err := c.Ingest.validate(prod); err != nil {
		return err
	}
	if c.Registry.HeartbeatSeconds < 1 || c.Registry.HeartbeatSeconds > 3600 {
		return errors.New("config: registry.heartbeat_seconds must be within [1, 3600]")
	}
	if c.Retention.Days < 1 || c.Retention.Days > 3650 {
		return errors.New("config: retention.days must be within [1, 3650]")
	}
	if c.Stale.AfterSeconds < 60 || c.Stale.AfterSeconds > 31536000 {
		return errors.New("config: stale.after_seconds must be within [60, 31536000]")
	}
	if c.Jobs.Workers < 1 || c.Jobs.Workers > 64 {
		return errors.New("config: jobs.workers must be within [1, 64]")
	}
	if c.Jobs.IntervalSeconds < 1 || c.Jobs.IntervalSeconds > 3600 {
		return errors.New("config: jobs.interval_seconds must be within [1, 3600]")
	}
	if c.Jobs.PurgeIntervalSeconds < 1 || c.Jobs.PurgeIntervalSeconds > 86400 {
		return errors.New("config: jobs.purge_interval_seconds must be within [1, 86400]")
	}
	if c.Gateway.Service == "" {
		return errors.New("config: gateway.service is required")
	}
	if iu, err := url.Parse(c.Gateway.Issuer); err != nil || iu.Scheme != "https" || iu.Host == "" {
		return errors.New("config: gateway.issuer must be an https origin")
	}
	if c.Enroll.TokenTTLSeconds < 60 || c.Enroll.TokenTTLSeconds > 604800 {
		return errors.New("config: enroll.token_ttl_seconds must be within [60, 604800]")
	}
	if c.Limits.MaxRequestBytes < 1<<10 || c.Limits.MaxRequestBytes > 64<<20 {
		return errors.New("config: limits_inventory.max_request_bytes must be within [1 KiB, 64 MiB]")
	}
	if c.Limits.MaxSnapshotBytes < 1<<10 || c.Limits.MaxSnapshotBytes > 128<<20 {
		return errors.New("config: limits_inventory.max_snapshot_bytes must be within [1 KiB, 128 MiB]")
	}
	if err := c.HostReports.validate(); err != nil {
		return err
	}
	if err := c.CertDelivery.validate(prod); err != nil {
		return err
	}
	return c.AgentReleases.validate()
}

// RequestTTL is how long an upgrade request waits for its agent (FR-011).
func (c Config) RequestTTL() time.Duration {
	return time.Duration(c.AgentReleases.RequestTTLHours) * time.Hour
}

// ProgressTimeout fails an upgrade whose agent stops reporting.
func (c Config) ProgressTimeout() time.Duration {
	return time.Duration(c.AgentReleases.ProgressTimeoutMinutes) * time.Minute
}

// validate checks the ingest transport: plaintext only as a non-production
// opt-out, otherwise a complete certificate/key pair.
func (i Ingest) validate(prod bool) error {
	if i.Insecure {
		if prod {
			return errors.New("config: ingest.insecure is not permitted in production (set ingest.tls_cert_file and ingest.tls_key_file)")
		}
		if i.TLSCertFile != "" || i.TLSKeyFile != "" {
			return errors.New("config: ingest.insecure contradicts ingest.tls_cert_file/tls_key_file; set one or the other")
		}
		return nil
	}
	if i.TLSCertFile == "" {
		return errors.New("config: ingest.tls_cert_file is required unless ingest.insecure is set (development only)")
	}
	if i.TLSKeyFile == "" {
		return errors.New("config: ingest.tls_key_file is required with ingest.tls_cert_file")
	}
	if i.TLSReloadSeconds < 0 || i.TLSReloadSeconds > 86400 {
		return errors.New("config: ingest.tls_reload_seconds must be within [0, 86400] (0 = 60)")
	}
	return nil
}

// Warnings lists accepted insecure opt-outs (surfaced at start).
func (c Config) Warnings() []string {
	w := c.Config.Warnings()
	if c.Valkey.AllowPlaintext {
		w = append(w, "valkey.allow_plaintext: event-bus/registry traffic without TLS (development only)")
	}
	if c.Ingest.Insecure {
		w = append(w, "ingest.insecure: off-mesh ingest listener without TLS (development only)")
	}
	if c.CertDelivery.AllowPlaintextIngest {
		w = append(w, "cert_delivery.allow_plaintext_ingest: certificate material served to agents without TLS (development only)")
	}
	return w
}

// GRPCAddr is the mesh gRPC listener (framework server section).
func (c Config) GRPCAddr() string { return c.Config.Server.GRPCAddr }

// HTTPAddr is the mesh HTTP listener (framework server section).
func (c Config) HTTPAddr() string { return c.Config.Server.HTTPAddr }

// IngestAddr is the off-mesh ingest listener.
func (c Config) IngestAddr() string { return c.Ingest.Addr }

// IngestTLSReload is how often the ingest certificate pair is re-read.
func (c Config) IngestTLSReload() time.Duration {
	if c.Ingest.TLSReloadSeconds <= 0 {
		return defaultIngestTLSReload
	}
	return time.Duration(c.Ingest.TLSReloadSeconds) * time.Second
}

// AdminAddr is the framework admin/operations listener.
func (c Config) AdminAddr() string { return c.Config.Admin.Addr }

// Interval is the background worker tick.
func (c Config) Interval() time.Duration {
	return time.Duration(c.Jobs.IntervalSeconds) * time.Second
}

// PurgeInterval is how often the retention purge runs.
func (c Config) PurgeInterval() time.Duration {
	return time.Duration(c.Jobs.PurgeIntervalSeconds) * time.Second
}

// StaleAfter is the no-snapshot window after which a host is stale.
func (c Config) StaleAfter() time.Duration {
	return time.Duration(c.Stale.AfterSeconds) * time.Second
}

// Heartbeat is the live-registry heartbeat/TTL.
func (c Config) Heartbeat() time.Duration {
	return time.Duration(c.Registry.HeartbeatSeconds) * time.Second
}

// EnrollTTL is the default lifetime of a minted enrollment token.
func (c Config) EnrollTTL() time.Duration {
	return time.Duration(c.Enroll.TokenTTLSeconds) * time.Second
}

// RetentionWindow is how long snapshots are kept before purge.
func (c Config) RetentionWindow() time.Duration {
	return time.Duration(c.Retention.Days) * 24 * time.Hour
}

// AgentConfig is the endpoint agent's own configuration. The agent runs off the
// mesh: it authenticates to the ingest listener with a bearer credential
// (obtained by consuming an enrollment token) and posts snapshots on a timer.
type AgentConfig struct {
	IngestEndpoint  string `yaml:"ingest_endpoint"`
	IntervalSeconds int    `yaml:"interval_seconds"`
	TokenFile       string `yaml:"token_file"`      // one-time enrollment token
	CredentialFile  string `yaml:"credential_file"` // persisted agent credential after enroll
	// AutoEnroll enrolls with a tenant's auto-enrollment key instead of a
	// token (feature 029): used when no credential is persisted and the
	// token_file does not exist.
	AutoEnroll AgentAutoEnroll `yaml:"auto_enroll"`
	StateFile  string          `yaml:"state_file"` // last-snapshot hash / cursor
	// Insecure dials the ingest edge in plaintext (development stack only).
	Insecure bool `yaml:"insecure"`
	// CAFile pins the ingest server's issuing CA (PEM bundle). When set, ONLY
	// these CAs are trusted; when empty the operating system roots are used.
	CAFile string `yaml:"ca_file"`
	// ServerName overrides the name verified against the server certificate
	// (default: the host part of ingest_endpoint).
	ServerName string `yaml:"server_name"`

	// Host report collection (feature 020). CollectBMC reads the BMC LAN
	// settings (IPMI LAN parameters 3/4/5/6/12/20 only, never credentials;
	// Linux, root, ipmi_devintf + ipmi_si). CollectUpdates reads the package
	// update state without refreshing the package lists unless
	// RefreshPackageLists is set (then at most once per 24 h).
	// UpdateTimeoutSeconds bounds the whole update collection.
	CollectBMC           bool `yaml:"collect_bmc"`
	CollectUpdates       bool `yaml:"collect_updates"`
	RefreshPackageLists  bool `yaml:"refresh_package_lists"`
	UpdateTimeoutSeconds int  `yaml:"update_timeout_seconds"`

	// CollectDisks reports the physical disks (feature 023: model, serial,
	// size, media, interface; Linux sysfs, Windows Get-PhysicalDisk).
	CollectDisks bool `yaml:"collect_disks"`

	// Upgrade configures self-upgrade (feature 023). Upgrades are only
	// ever downloaded from the configured ingest endpoint; there is
	// deliberately no other source setting.
	Upgrade AgentUpgrade `yaml:"upgrade"`

	// Certificates configures the certificates the platform delivers to
	// this host (feature 033). Files go only to Directory; the server
	// chooses only the certificate name; nothing runs unless DeployHook is
	// set here.
	Certificates AgentCertificates `yaml:"certificates"`
}

// AgentCertificates is the agent's certificate store configuration
// (contracts/agent-config.md §1). Modes are octal strings ("0640").
type AgentCertificates struct {
	Enabled                bool   `yaml:"enabled"`
	Directory              string `yaml:"directory"`
	Owner                  string `yaml:"owner"`
	Group                  string `yaml:"group"`
	DirMode                string `yaml:"dir_mode"`
	CertMode               string `yaml:"cert_mode"`
	KeyMode                string `yaml:"key_mode"`
	KeepPrevious           int    `yaml:"keep_previous"`
	DeployHook             string `yaml:"deploy_hook"`
	HookTimeoutSeconds     int    `yaml:"hook_timeout_seconds"`
	AllowInsecureTransport bool   `yaml:"allow_insecure_transport"`
}

// forbiddenCertDirs are trees the certificate directory may not live in:
// virtual filesystems, and trees systemd's ProtectHome/PrivateTmp hide from
// the agent service.
var forbiddenCertDirs = []string{"/proc", "/sys", "/dev", "/home", "/root", "/tmp"}

var (
	accountRE = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}|[0-9]{1,10})$`)
	modeRE    = regexp.MustCompile(`^0?[0-7]{3}$`)
)

func parseMode(s string) (fs.FileMode, bool) {
	if !modeRE.MatchString(s) {
		return 0, false
	}
	v, _ := strconv.ParseUint(s, 8, 32) // cannot fail: modeRE admits 3-4 octal digits
	return fs.FileMode(v), true         // #nosec G115 -- at most 0o7777
}

func validAccount(s string) bool {
	if !accountRE.MatchString(s) {
		return false
	}
	if s[0] >= '0' && s[0] <= '9' {
		v, err := strconv.ParseUint(s, 10, 32)
		return err == nil && v <= 0xFFFFFFFE
	}
	return true
}

// Validate checks the certificates section (also when disabled). Paths use
// POSIX rules on every platform: the store is Linux-only (Windows agents
// never announce cert.v1).
func (c AgentCertificates) Validate() error {
	d := c.Directory
	if d == "" || !path.IsAbs(d) || path.Clean(d) != d || d == "/" {
		return errors.New("config: agent certificates.directory must be an absolute, clean path other than /")
	}
	for _, p := range forbiddenCertDirs {
		if d == p || strings.HasPrefix(d, p+"/") {
			return fmt.Errorf("config: agent certificates.directory must not be under %s", p)
		}
	}
	if !validAccount(c.Owner) || !validAccount(c.Group) {
		return errors.New("config: agent certificates.owner and certificates.group must be a user/group name or a numeric id")
	}
	dm, ok := parseMode(c.DirMode)
	if !ok || dm < 0o700 || dm > 0o755 || dm&0o022 != 0 {
		return errors.New("config: agent certificates.dir_mode must be an octal mode within 0700-0755 without group/world write")
	}
	cm, ok := parseMode(c.CertMode)
	if !ok || cm&0o022 != 0 || cm&0o400 == 0 {
		return errors.New("config: agent certificates.cert_mode must be an octal mode readable by the owner without group/world write")
	}
	if km, ok := parseMode(c.KeyMode); !ok || (km != 0o600 && km != 0o640) {
		return errors.New("config: agent certificates.key_mode must be 0600 or 0640")
	}
	if c.KeepPrevious < 0 || c.KeepPrevious > 5 {
		return errors.New("config: agent certificates.keep_previous must be within [0, 5]")
	}
	if h := c.DeployHook; h != "" && (!path.IsAbs(h) || path.Clean(h) != h || h == "/") {
		return errors.New("config: agent certificates.deploy_hook must be empty or an absolute, clean file path")
	}
	if c.HookTimeoutSeconds < 30 || c.HookTimeoutSeconds > 1800 {
		return errors.New("config: agent certificates.hook_timeout_seconds must be within [30, 1800]")
	}
	return nil
}

// Modes returns the validated directory, certificate and key file modes.
func (c AgentCertificates) Modes() (dir, cert, key fs.FileMode) {
	dir, _ = parseMode(c.DirMode)
	cert, _ = parseMode(c.CertMode)
	key, _ = parseMode(c.KeyMode)
	return dir, cert, key
}

// HookTimeout bounds one deploy hook run.
func (c AgentCertificates) HookTimeout() time.Duration {
	return time.Duration(c.HookTimeoutSeconds) * time.Second
}

// Announce reports whether the agent announces cert.v1: enabled, on Linux,
// and over TLS unless the plaintext opt-out is set locally.
func (c AgentCertificates) Announce(goos string, insecureTransport bool) bool {
	return c.Enabled && goos == "linux" && (!insecureTransport || c.AllowInsecureTransport)
}

// AgentAutoEnroll names an auto-enrollment key: KeyID is its public id
// (ak_…), KeyFile the path of a file holding its secret (aks_…, readable by
// the agent only).
type AgentAutoEnroll struct {
	KeyID   string `yaml:"key_id"`
	KeyFile string `yaml:"key_file"`
}

// Configured reports whether automatic enrollment is set up.
func (a AgentAutoEnroll) Configured() bool { return a.KeyID != "" || a.KeyFile != "" }

// AgentUpgrade: Enabled accepts upgrade requests pushed by the server
// (false: only the manual `update` command upgrades); ConfirmTimeoutSeconds
// bounds how long a new version has to start and report before the host
// rolls back (FR-013); StagingDir overrides the private staging directory
// (default /var/lib/inventory-agent/upgrade, on Windows
// %ProgramData%\go-tangra\inventory-agent\upgrade).
type AgentUpgrade struct {
	Enabled               bool   `yaml:"enabled"`
	ConfirmTimeoutSeconds int    `yaml:"confirm_timeout_seconds"`
	StagingDir            string `yaml:"staging_dir"`
}

// autoKeyIDRE matches an auto-enrollment key id (sdk/pkg/autoenroll).
var autoKeyIDRE = regexp.MustCompile(`^ak_[0-9a-f]{24}$`)

// DefaultAgent returns the endpoint agent's secure defaults.
func DefaultAgent() AgentConfig {
	return AgentConfig{IntervalSeconds: 3600, CollectBMC: true, CollectUpdates: true, UpdateTimeoutSeconds: 120, CollectDisks: true,
		Upgrade: AgentUpgrade{Enabled: true, ConfirmTimeoutSeconds: 300},
		Certificates: AgentCertificates{Enabled: true, Directory: "/etc/inventory-agent/certs", Owner: "root", Group: "root",
			DirMode: "0750", CertMode: "0644", KeyMode: "0600", KeepPrevious: 1, HookTimeoutSeconds: 300}}
}

// LoadAgent reads the endpoint agent's YAML over DefaultAgent(); unknown fields
// are rejected.
func LoadAgent(path string) (AgentConfig, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied config path
	if err != nil {
		return DefaultAgent(), fmt.Errorf("config: %w", err)
	}
	cfg, err := decodeAgent(raw)
	if err != nil {
		return cfg, fmt.Errorf("config: %s: %w", path, err)
	}
	return cfg, nil
}

// decodeAgent decodes strict YAML over DefaultAgent().
func decodeAgent(raw []byte) (AgentConfig, error) {
	cfg := DefaultAgent()
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// Validate checks the endpoint agent configuration.
func (a AgentConfig) Validate() error {
	if a.IngestEndpoint == "" {
		return errors.New("config: agent ingest_endpoint is required")
	}
	if a.IntervalSeconds < 60 || a.IntervalSeconds > 604800 {
		return errors.New("config: agent interval_seconds must be within [60, 604800]")
	}
	if a.UpdateTimeoutSeconds < 30 || a.UpdateTimeoutSeconds > 600 {
		return errors.New("config: agent update_timeout_seconds must be within [30, 600]")
	}
	if a.TokenFile == "" && a.CredentialFile == "" && !a.AutoEnroll.Configured() {
		return errors.New("config: agent token_file, credential_file or auto_enroll is required")
	}
	if a.AutoEnroll.Configured() {
		if !autoKeyIDRE.MatchString(a.AutoEnroll.KeyID) {
			return errors.New("config: agent auto_enroll.key_id must look like ak_ followed by 24 hex characters")
		}
		if a.AutoEnroll.KeyFile == "" {
			return errors.New("config: agent auto_enroll.key_file is required with auto_enroll.key_id")
		}
		if a.CredentialFile == "" || a.StateFile == "" {
			return errors.New("config: agent credential_file and state_file are required with auto_enroll (the issued credential is persisted there)")
		}
	}
	if a.Upgrade.ConfirmTimeoutSeconds < 60 || a.Upgrade.ConfirmTimeoutSeconds > 1800 {
		return errors.New("config: agent upgrade.confirm_timeout_seconds must be within [60, 1800]")
	}
	if a.Upgrade.StagingDir != "" && !filepath.IsAbs(a.Upgrade.StagingDir) {
		return errors.New("config: agent upgrade.staging_dir must be an absolute path")
	}
	if a.Insecure && (a.CAFile != "" || a.ServerName != "") {
		return errors.New("config: agent insecure contradicts ca_file/server_name (TLS settings)")
	}
	if a.CAFile != "" {
		if err := checkCABundle(a.CAFile); err != nil {
			return err
		}
	}
	return a.Certificates.Validate()
}

// Warnings lists accepted insecure opt-outs of the agent (logged at start).
func (a AgentConfig) Warnings() []string {
	var w []string
	if a.Insecure && a.Certificates.AllowInsecureTransport {
		w = append(w, "certificates.allow_insecure_transport: certificates and keys received over a plaintext ingest connection (development only)")
	}
	return w
}

// checkCABundle fails fast on an unreadable ca_file or one without a PEM
// certificate.
func checkCABundle(path string) error {
	raw, err := os.ReadFile(path) // #nosec G304 -- operator-supplied CA path
	if err != nil {
		return fmt.Errorf("config: agent ca_file: %w", err)
	}
	if !x509.NewCertPool().AppendCertsFromPEM(raw) {
		return fmt.Errorf("config: agent ca_file %s: no PEM certificate found", path)
	}
	return nil
}

// UpdateTimeout bounds the agent's whole package update collection.
func (a AgentConfig) UpdateTimeout() time.Duration {
	return time.Duration(a.UpdateTimeoutSeconds) * time.Second
}

// ConfirmTimeout bounds how long a new agent version has to confirm itself.
func (a AgentConfig) ConfirmTimeout() time.Duration {
	return time.Duration(a.Upgrade.ConfirmTimeoutSeconds) * time.Second
}

// AgentInterval is the endpoint agent's collection tick.
func (a AgentConfig) AgentInterval() time.Duration {
	return time.Duration(a.IntervalSeconds) * time.Second
}
