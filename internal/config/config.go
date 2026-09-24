// Package config loads and validates the inventory service configuration: the
// Freya framework config plus the module's own sections. Every value is
// explicit; insecure opt-outs are named and surfaced at start (Constitution
// I/VII). The off-mesh ingest listener, the Valkey-backed live registry, the
// snapshot/retention jobs and the endpoint agent all read from here.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
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
// bearer credential rather than a mesh SVID, so insecure is an explicit opt-out.
type Ingest struct {
	Addr     string `yaml:"addr"`
	Insecure bool   `yaml:"insecure"`
}

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
		Config:    fconfig.Default(),
		DB:        DB{MaxConns: 16},
		KEK:       KEK{Source: "file"},
		Ingest:    Ingest{},
		Registry:  Registry{HeartbeatSeconds: 30},
		Retention: Retention{Days: 90},
		Stale:     Stale{AfterSeconds: 86400},
		Jobs:      Jobs{Workers: 4, IntervalSeconds: 60, PurgeIntervalSeconds: 3600},
		Events:    Events{Enabled: true},
		Gateway:   Gateway{Service: "gateway"},
		Enroll:    Enroll{TokenTTLSeconds: 3600},
		Limits:    Limits{MaxRequestBytes: 1 << 20, MaxSnapshotBytes: 8 << 20},
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
	if prod && c.Ingest.Insecure {
		return errors.New("config: ingest.insecure is not permitted in production")
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
	StateFile       string `yaml:"state_file"`      // last-snapshot hash / cursor
	Insecure        bool   `yaml:"insecure"`
}

// DefaultAgent returns the endpoint agent's secure defaults.
func DefaultAgent() AgentConfig {
	return AgentConfig{IntervalSeconds: 3600}
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
	if a.TokenFile == "" && a.CredentialFile == "" {
		return errors.New("config: agent token_file or credential_file is required")
	}
	return nil
}

// AgentInterval is the endpoint agent's collection tick.
func (a AgentConfig) AgentInterval() time.Duration {
	return time.Duration(a.IntervalSeconds) * time.Second
}
