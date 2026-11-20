package agentteleport

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Report states and reasons (the closed set of ReportTeleportJoin).
const (
	StateJoined = "joined"
	StateFailed = "failed"

	ReasonUnsupportedPlatform = "unsupported_platform"
	ReasonAlreadyInstalled    = "already_installed"
	ReasonInvalidMaterial     = "invalid_material"
	reasonDownload            = "download_failed"
	reasonChecksum            = "checksum_mismatch"
	reasonInstall             = "install_failed"
	reasonJoin                = "join_failed"
	reasonTimeout             = "timeout"
)

// stagingRel is the private download directory (0700).
const stagingRel = "var/lib/inventory-agent/teleport-staging"

// pollInterval is how often the join is checked.
const pollInterval = 2 * time.Second

// recheckInterval is how often, while waiting for the join, the server is
// asked whether the item is still active (not cancelled or failed).
const recheckInterval = 30 * time.Second

// probeTimeout bounds one readiness probe.
const probeTimeout = 5 * time.Second

// maxDetail bounds a report detail.
const maxDetail = 200

// ErrUnsupported: Teleport joins are Linux (systemd) only.
var ErrUnsupported = errors.New("agentteleport: unsupported platform")

// Doer sends HTTP requests (an *http.Client).
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Systemctl runs systemctl with fixed arguments; a non-zero exit is an
// error.
type Systemctl interface {
	Run(ctx context.Context, args ...string) error
}

// Deps are the installer's environment. Production uses NewOS; the fields
// exist for tests.
type Deps struct {
	Root      string // filesystem root ("/")
	Arch      string // GOARCH
	BaseURL   string // download origin; tests only (Origin in production)
	HTTP      Doer
	Systemctl Systemctl
	Hostname  func() (string, error)
	Now       func() time.Time
	Sleep     func(ctx context.Context, d time.Duration) error
}

// Result is the outcome of a join (State joined or failed). It never holds
// the token or command output.
type Result struct {
	State, Reason, NodeHostUUID, Detail string
	// Aborted: the server no longer accepts the item (cancelled or
	// expired); nothing more is reported.
	Aborted bool
}

// Installer joins this host to a Teleport cluster as an SSH node.
type Installer struct {
	root, arch, base string
	http             Doer
	sys              Systemctl
	hostname         func() (string, error)
	now              func() time.Time
	sleep            func(ctx context.Context, d time.Duration) error
	timeout          time.Duration
	maxArchive       int64
}

// New builds an installer over d; timeout bounds the wait for the join.
func New(d Deps, timeout time.Duration) *Installer {
	base := d.BaseURL
	if base == "" {
		base = Origin
	}
	return &Installer{root: d.Root, arch: d.Arch, base: strings.TrimSuffix(base, "/"), http: d.HTTP, sys: d.Systemctl,
		hostname: d.Hostname, now: d.Now, sleep: d.Sleep, timeout: timeout, maxArchive: MaxArchiveBytes}
}

// failure is a failed step: a report reason and a token-free detail.
type failure struct{ reason, detail string }

func fail(reason, detail string) *failure { return &failure{reason: reason, detail: detail} }

func (f *failure) result() Result {
	return Result{State: StateFailed, Reason: f.reason, Detail: f.detail}
}

// path is rel below the filesystem root.
func (in *Installer) path(rel string) string { return filepath.Join(in.root, rel) }

// safeErr is an error text cut to printable ASCII and maxDetail bytes.
func safeErr(err error) string {
	b := []byte(err.Error())
	out := make([]byte, 0, len(b))
	for _, c := range b {
		if len(out) == maxDetail {
			break
		}
		if c < 0x20 || c > 0x7e {
			c = '?'
		}
		out = append(out, c)
	}
	return string(out)
}

// Preflight returns the reason this host cannot take a join, "" when it
// can: unsupported_platform without systemd or on another architecture,
// already_installed when Teleport is configured or its unit is active or
// enabled (a configuration the agent wrote whose token was removed after
// a failed join does not count).
func (in *Installer) Preflight(ctx context.Context) string {
	if in.arch != "amd64" && in.arch != "arm64" {
		return ReasonUnsupportedPlatform
	}
	if fi, err := os.Stat(in.path("run/systemd/system")); err != nil || !fi.IsDir() {
		return ReasonUnsupportedPlatform
	}
	if raw, err := os.ReadFile(in.path(ConfigPath)); err == nil && !ours(raw) {
		return ReasonAlreadyInstalled
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return ReasonAlreadyInstalled
	}
	for _, u := range packagedUnits {
		if _, err := os.Lstat(in.path(u)); err == nil {
			return ReasonAlreadyInstalled
		}
	}
	if in.sys.Run(ctx, "is-active", "--quiet", UnitName) == nil || in.sys.Run(ctx, "is-enabled", "--quiet", UnitName) == nil {
		return ReasonAlreadyInstalled
	}
	return ""
}

// Join validates m, reports installing (installing returns false when the
// server no longer accepts the item), installs Teleport m.Version, writes
// the configuration and the unit, starts it and waits until the node
// joined: the unit active, Teleport's /readyz answering 200 and the host
// uuid written. While waiting it asks installing again every
// recheckInterval; once the item is no longer accepted the join is rolled
// back and Aborted. m's token is wiped before Join returns. After a
// failure past the configuration, the token is removed from it and the
// unit stopped.
func (in *Installer) Join(ctx context.Context, m Material, installing func() bool) Result {
	defer m.Wipe()
	if err := m.Validate(); err != nil {
		return Result{State: StateFailed, Reason: ReasonInvalidMaterial, Detail: safeErr(err)}
	}
	if r := in.Preflight(ctx); r != "" {
		return Result{State: StateFailed, Reason: r}
	}
	nodename, err := in.hostname()
	if err != nil || !validDNSName(nodename) {
		return Result{State: StateFailed, Reason: reasonInstall, Detail: "hostname is not a valid node name"}
	}
	if !installing() {
		return Result{Aborted: true}
	}
	if f := in.installBinary(ctx, m.Version); f != nil {
		return f.result()
	}
	raw := renderConfig(nodename, m, string(m.Token))
	err = replace(in.path(ConfigPath), 0o600, raw)
	clear(raw)
	if err != nil {
		return in.rollback(ctx, m, nodename, fail(reasonInstall, "write /etc/teleport.yaml"))
	}
	uuid, f := in.start(ctx, installing)
	if f == errAborted {
		in.rollback(ctx, m, nodename, fail(reasonJoin, "no longer accepted"))
		return Result{Aborted: true}
	}
	if f != nil {
		return in.rollback(ctx, m, nodename, f)
	}
	return Result{State: StateJoined, NodeHostUUID: uuid}
}

// installBinary downloads, verifies and installs the teleport binary.
func (in *Installer) installBinary(ctx context.Context, version string) *failure {
	archive, f := in.download(ctx, version)
	if f != nil {
		return f
	}
	defer func() { _ = os.Remove(archive) }()
	dst := in.path(BinaryPath)
	_ = os.MkdirAll(filepath.Dir(dst), 0o755) // #nosec G301 -- /usr/local/bin is world-readable by design; extraction fails if it could not be made
	tmp, err := extractBinary(archive, filepath.Dir(dst), in.maxArchive)
	if err != nil {
		return fail(reasonInstall, "extract teleport: "+safeErr(err))
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return fail(reasonInstall, "install /usr/local/bin/teleport")
	}
	return nil
}

// errAborted: the server no longer accepts the item.
var errAborted = &failure{}

// start writes the unit, enables and starts it and waits for the join
// (errAborted once installing returns false).
func (in *Installer) start(ctx context.Context, installing func() bool) (string, *failure) {
	if err := replace(in.path(UnitPath), 0o644, []byte(unitFile)); err != nil {
		return "", fail(reasonInstall, "write the teleport unit")
	}
	if err := in.sys.Run(ctx, "daemon-reload"); err != nil {
		return "", fail(reasonJoin, "systemctl daemon-reload failed")
	}
	if err := in.sys.Run(ctx, "enable", "--now", UnitName); err != nil {
		return "", fail(reasonJoin, "systemctl enable --now teleport failed")
	}
	deadline := in.now().Add(in.timeout)
	recheck := in.now().Add(recheckInterval)
	for {
		if in.sys.Run(ctx, "is-active", "--quiet", UnitName) == nil && in.ready(ctx) {
			if raw, err := os.ReadFile(in.path(HostUUIDRel)); err == nil {
				if u := strings.TrimSpace(string(raw)); uuidRE.MatchString(u) {
					return u, nil
				}
			}
		}
		if !in.now().Before(recheck) {
			if !installing() {
				return "", errAborted
			}
			recheck = in.now().Add(recheckInterval)
		}
		if !in.now().Before(deadline) {
			return "", fail(reasonTimeout, "teleport did not join in time")
		}
		if err := in.sleep(ctx, pollInterval); err != nil {
			return "", fail(reasonTimeout, "join wait cancelled")
		}
	}
}

// ready reports whether the node's /readyz answers 200: it joined and is
// connected to the cluster.
func (in *Installer) ready(ctx context.Context) bool {
	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	// ReadyURL is a constant, valid URL: the request cannot fail to build.
	req, _ := http.NewRequestWithContext(pctx, http.MethodGet, ReadyURL, nil)
	res, err := in.http.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = res.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
	return res.StatusCode == http.StatusOK
}

// rollback removes the token from the configuration and stops the unit
// (best effort; the configuration rewrite is what matters).
func (in *Installer) rollback(ctx context.Context, m Material, nodename string, f *failure) Result {
	_ = in.sys.Run(context.WithoutCancel(ctx), "disable", "--now", UnitName)
	if err := replace(in.path(ConfigPath), 0o600, renderConfig(nodename, m, "")); err != nil {
		// The token may still be on disk: remove the file instead.
		if rerr := os.Remove(in.path(ConfigPath)); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			f.detail += "; could not remove the join token from /etc/teleport.yaml"
		}
	}
	return f.result()
}
