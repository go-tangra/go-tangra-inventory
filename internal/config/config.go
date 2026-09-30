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
	"net/url"
	"os"
	"path/filepath"
	"regexp"
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
		Upgrade: AgentUpgrade{Enabled: true, ConfirmTimeoutSeconds: 300}}
}

// LoadAgent reads the endpoint agent's YAML over DefaultAgent(); unknown fields
// are rejected.
func LoadAgent(path string) (AgentConfig, error) {
	cfg := DefaultAgent()
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
	return nil
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
