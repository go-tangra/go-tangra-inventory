package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

// Report states and reason codes (contracts/inventory-grpc.md §3).
const (
	StateDownloading = "downloading"
	StateInstalling  = "installing"
	StateSucceeded   = "succeeded"
	StateFailed      = "failed"
	StateRolledBack  = "rolled_back"

	ReasonSignatureInvalid   = "signature_invalid"
	ReasonUnknownKey         = "unknown_key"
	ReasonChecksumMismatch   = "checksum_mismatch"
	ReasonSizeMismatch       = "size_mismatch"
	ReasonPlatformMismatch   = "platform_mismatch"
	ReasonVersionMismatch    = "version_mismatch"
	ReasonDowngradeRefused   = "downgrade_refused"
	ReasonDiskFull           = "disk_full"
	ReasonDownloadFailed     = "download_failed"
	ReasonInstallFailed      = "install_failed"
	ReasonStartTimeout       = "start_timeout"
	ReasonUnsupportedInstall = "unsupported_install"
	ReasonBusy               = "busy"
	ReasonPackageDBMismatch  = "package_db_mismatch"
)

// Bounds and names.
const (
	MaxArtifactSize = agentrelease.MaxArtifactSize
	stateFile       = "state.json"
	lockFile        = "lock"
	helperName      = "inventory-agent-helper"
	backupName      = "inventory-agent.prev"
	staleLock       = 30 * time.Minute
	pollInterval    = 2 * time.Second
)

// ErrBusy means another upgrade holds the lock.
var ErrBusy = errors.New("selfupdate: another upgrade is in progress")

// Command is an upgrade request received from the server (or created by the
// `update` command through CheckAgentUpdate).
type Command struct {
	RequestID      string
	TargetVersion  string
	AllowDowngrade bool
}

// CheckResult is the server's answer to an update check.
type CheckResult struct {
	Available      bool
	TargetVersion  string
	RequestID      string
	Reason         string
	AllowDowngrade bool
}

// Header is the release header of a download: the exact signed manifest,
// its signature and the entry the server chose for this agent.
type Header struct {
	Manifest  []byte
	Signature []byte
	KeyID     string
	File      string
	Size      int64
	SHA256    string
}

// Download is an artifact stream: the header, then chunks with their offset
// (io.EOF at the end).
type Download interface {
	Header() Header
	Next() (offset int64, data []byte, err error)
	Close() error
}

// Report is a progress report sent to the server.
type Report struct {
	RequestID, State, FromVersion, ToVersion, Reason, Detail string
}

// Client talks to the inventory module over the agent's authenticated
// ingest connection — the only upgrade source (SR-002).
type Client interface {
	Check(ctx context.Context, current string, p agentrelease.Platform, apply bool) (CheckResult, error)
	Download(ctx context.Context, requestID, version string) (Download, error)
	Report(ctx context.Context, r Report) error
}

// FileInfo is what the core needs to know about a file.
type FileInfo struct {
	Mode      fs.FileMode
	Size      int64
	ModTime   time.Time
	OwnerRoot bool // owned by root (Linux) / SYSTEM or Administrators (Windows)
	Private   bool // no access for anyone else (Unix: no group/other bits; Windows: DACL of SYSTEM/Administrators only)
}

// FS is the file system glue (internal/upgrader implements it on the host).
type FS interface {
	MkdirAll(path string, perm fs.FileMode) error
	Create(path string, perm fs.FileMode) (io.WriteCloser, error)
	CreateExclusive(path string, data []byte, perm fs.FileMode) error // fs.ErrExist when present
	Open(path string) (io.ReadCloser, error)
	ReadFile(path string) ([]byte, error)
	WriteFileAtomic(path string, data []byte, perm fs.FileMode) error
	Remove(path string) error
	RemoveAll(path string) error
	Stat(path string) (FileInfo, error)
	StatDir(path string) (FileInfo, error) // a directory, not a symlink to one
	ReadDir(path string) ([]string, error)
	CopyFile(src, dst string, perm fs.FileMode) error
	Free(path string) (uint64, error)
}

// Installer is the operating-system glue that replaces the agent.
type Installer interface {
	// Supported refuses install types this host cannot upgrade (e.g. no
	// systemd on Linux): reason unsupported_install.
	Supported(installType string) error
	// StartHelper runs `<helper> upgrade-apply -state <stateFile>` outside
	// the agent's service (systemd transient unit / detached process).
	StartHelper(ctx context.Context, helper, stateFile, requestID string) error
	// InstallPackage installs a deb or rpm through dpkg/rpm (downgrade:
	// rpm --oldpackage); the package scripts restart the service.
	InstallPackage(ctx context.Context, installType, artifact string, downgrade bool) error
	// SwapBinary atomically replaces target with newBinary, keeping the
	// previous file, and returns its path.
	SwapBinary(ctx context.Context, newBinary, target string) (previous string, err error)
	// RestoreBinary puts previous back in place of target.
	RestoreBinary(ctx context.Context, previous, target string) error
	// RestartService restarts the agent service.
	RestartService(ctx context.Context) error
}

// Clock is time for the core (tests use a fake).
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now().UTC() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// RealClock is the wall clock.
func RealClock() Clock { return realClock{} }

// Config describes the running agent.
type Config struct {
	Version        string                // own version
	Platform       agentrelease.Platform // own platform and install type
	Keys           agentrelease.Keyring  // compiled release keyring
	StagingDir     string                // private staging directory (0700)
	Executable     string                // path of the running agent binary
	ConfirmTimeout time.Duration         // new version must confirm within it (FR-013)
}

// Updater is the agent-side upgrade core.
type Updater struct {
	cfg    Config
	client Client
	fs     FS
	inst   Installer
	clock  Clock
	log    *slog.Logger
}

// New builds an Updater.
func New(cfg Config, c Client, f FS, inst Installer, clock Clock, log *slog.Logger) *Updater {
	return &Updater{cfg: cfg, client: c, fs: f, inst: inst, clock: clock, log: log}
}

func (u *Updater) path(elem ...string) string {
	return filepath.Join(append([]string{u.cfg.StagingDir}, elem...)...)
}

// failure is a refusal with a reason code.
type failure struct {
	reason string
	err    error
}

func (f *failure) Error() string { return f.reason + ": " + f.err.Error() }

func fail(reason string, err error) error { return &failure{reason: reason, err: err} }

// reasonOf maps an error to its reason code (download_failed by default).
func reasonOf(err error) string {
	var f *failure
	if errors.As(err, &f) {
		return f.reason
	}
	switch {
	case errors.Is(err, agentrelease.ErrUnknownKey):
		return ReasonUnknownKey
	case errors.Is(err, agentrelease.ErrSignature), errors.Is(err, agentrelease.ErrManifest):
		return ReasonSignatureInvalid
	case errors.Is(err, agentrelease.ErrPlatform):
		return ReasonPlatformMismatch
	case errors.Is(err, agentrelease.ErrSize):
		return ReasonSizeMismatch
	case errors.Is(err, agentrelease.ErrChecksum):
		return ReasonChecksumMismatch
	}
	return ReasonDownloadFailed
}

// report sends a progress report; a failed report is logged (the server's
// stale-progress timeout covers a lost one).
func (u *Updater) report(ctx context.Context, cmd Command, state, reason string, detail error) {
	r := Report{RequestID: cmd.RequestID, State: state, FromVersion: u.cfg.Version, ToVersion: cmd.TargetVersion, Reason: reason}
	if detail != nil {
		r.Detail = clip(detail.Error(), 256)
	}
	if err := u.client.Report(ctx, r); err != nil {
		u.log.Warn("upgrade: report failed", "state", state, "err", err)
	}
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// Upgrade runs an upgrade request up to the start of the helper: checks,
// verified download into the private staging directory, rollback package,
// state file. Nothing on the host is replaced before the artifact verified
// (SR-004); every refusal is reported with its reason and the installer is
// never called. A duplicate of the request in progress is ignored; another
// upgrade holding the lock is reported busy (ErrBusy).
func (u *Updater) Upgrade(ctx context.Context, cmd Command) error {
	if err := u.fs.MkdirAll(u.cfg.StagingDir, 0o700); err != nil {
		return err
	}
	held, err := u.lock(cmd.RequestID)
	if err != nil {
		return err
	}
	if held == cmd.RequestID {
		u.log.Info("upgrade: request already in progress", "request_id", cmd.RequestID)
		return nil
	}
	if held != "" {
		u.report(ctx, cmd, StateFailed, ReasonBusy, ErrBusy)
		return ErrBusy
	}
	err = u.prepare(ctx, cmd)
	if err != nil {
		reason := reasonOf(err)
		state := StateFailed
		if reason == "" { // already on the target
			state = StateSucceeded
		}
		u.report(ctx, cmd, state, reason, err)
		u.unlock()
		if state == StateSucceeded {
			return nil
		}
		return err
	}
	return nil
}

// errAlreadyCurrent ends a request for the version already running.
var errAlreadyCurrent = &failure{reason: "", err: errors.New("already running the target version")}

func (u *Updater) prepare(ctx context.Context, cmd Command) error {
	if !u.cfg.Platform.Valid() {
		return fail(ReasonUnsupportedInstall, fmt.Errorf("platform %s", u.cfg.Platform))
	}
	if err := u.inst.Supported(u.cfg.Platform.InstallType); err != nil {
		return fail(ReasonUnsupportedInstall, err)
	}
	if err := u.checkVersion(cmd); err != nil {
		return err
	}
	u.report(ctx, cmd, StateDownloading, "", nil)
	artifact, entry, h, err := u.fetch(ctx, cmd.RequestID, cmd.TargetVersion)
	if err != nil {
		return err
	}
	st := State{RequestID: cmd.RequestID, FromVersion: u.cfg.Version, ToVersion: cmd.TargetVersion, InstallType: u.cfg.Platform.InstallType,
		Platform: u.cfg.Platform, Artifact: artifact, ArtifactSHA256: entry.SHA256, Manifest: h.Manifest, Signature: h.Signature, KeyID: h.KeyID,
		Executable: u.cfg.Executable, AllowDowngrade: cmd.AllowDowngrade, ConfirmTimeout: u.cfg.ConfirmTimeout, Phase: PhaseInstalling}
	if st.InstallType != agentrelease.InstallBinary {
		// The package of the running version is the rollback path; the
		// binary copy is the last resort (package_db_mismatch).
		if rb, rbEntry, _, err := u.fetch(ctx, "", u.cfg.Version); err == nil {
			st.RollbackArtifact, st.RollbackSHA256 = rb, rbEntry.SHA256
		} else {
			u.log.Warn("upgrade: no rollback package for the running version", "err", err)
		}
		st.PreviousBinary = u.path(u.cfg.Version, backupName)
		if err := u.fs.CopyFile(u.cfg.Executable, st.PreviousBinary, 0o700); err != nil {
			return fail(ReasonInstallFailed, err)
		}
	}
	helper := u.path(cmd.TargetVersion, helperName)
	if err := u.fs.CopyFile(u.cfg.Executable, helper, 0o700); err != nil {
		return fail(ReasonInstallFailed, err)
	}
	st.Deadline = u.clock.Now().Add(u.cfg.ConfirmTimeout)
	if err := u.saveState(st); err != nil {
		return fail(ReasonInstallFailed, err)
	}
	u.report(ctx, cmd, StateInstalling, "", nil)
	if err := u.inst.StartHelper(ctx, helper, u.path(stateFile), cmd.RequestID); err != nil {
		_ = u.fs.Remove(u.path(stateFile))
		return fail(ReasonInstallFailed, err)
	}
	return nil
}

// checkVersion refuses downgrades without an administrator pin, targets
// below the first self-upgrading version and non-release targets.
func (u *Updater) checkVersion(cmd Command) error {
	if !agentrelease.IsRelease(cmd.TargetVersion) {
		return fail(ReasonVersionMismatch, fmt.Errorf("target %q is not a release version", cmd.TargetVersion))
	}
	if agentrelease.BelowFloor(cmd.TargetVersion) {
		return fail(ReasonDowngradeRefused, fmt.Errorf("target %s is older than %s", cmd.TargetVersion, agentrelease.Floor))
	}
	c, ok := agentrelease.Compare(u.cfg.Version, cmd.TargetVersion)
	switch {
	case ok && c == 0:
		return errAlreadyCurrent
	case ok && c > 0 && !cmd.AllowDowngrade:
		return fail(ReasonDowngradeRefused, fmt.Errorf("%s is older than the running %s", cmd.TargetVersion, u.cfg.Version))
	}
	return nil
}

// Check asks the enrolled platform whether an upgrade is available
// (`inventory-agent update -check`).
func (u *Updater) Check(ctx context.Context) (CheckResult, error) {
	return u.client.Check(ctx, u.cfg.Version, u.cfg.Platform, false)
}

// Update is `inventory-agent update`: the platform creates an (audited)
// request for this agent when an upgrade is available, which then runs like
// a server-pushed one.
func (u *Updater) Update(ctx context.Context) (CheckResult, error) {
	res, err := u.client.Check(ctx, u.cfg.Version, u.cfg.Platform, true)
	if err != nil || !res.Available || res.RequestID == "" {
		return res, err
	}
	return res, u.Upgrade(ctx, Command{RequestID: res.RequestID, TargetVersion: res.TargetVersion, AllowDowngrade: res.AllowDowngrade})
}
