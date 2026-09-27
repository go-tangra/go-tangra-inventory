package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

// Refusals of the helper.
var (
	ErrStateLocation = errors.New("selfupdate: state file outside the staging directory")
	ErrStateMode     = errors.New("selfupdate: state file must be owned by root/SYSTEM with mode 0600")
)

// Apply is the helper (`inventory-agent upgrade-apply -state <file>`),
// started outside the agent's service by the agent itself. It refuses a
// state file outside the staging directory or not owned by root/SYSTEM
// with mode 0600, re-verifies the artifact (signature, platform, sha256),
// installs it (dpkg/rpm, or an atomic binary swap and a restart), waits for
// the new version to confirm within the deadline and otherwise rolls back:
// the previous package, or the previous binary (package_db_mismatch).
func (u *Updater) Apply(ctx context.Context, statePath string) error {
	if filepath.Clean(statePath) != u.path(stateFile) {
		return ErrStateLocation
	}
	info, err := u.fs.Stat(statePath)
	if err != nil {
		return err
	}
	if info.Mode.Perm() != 0o600 || !info.OwnerRoot {
		return ErrStateMode
	}
	st, err := u.loadState()
	if err != nil {
		return err
	}
	if err := u.reverify(st); err != nil {
		st.Phase, st.Reason = PhaseFailed, reasonOf(err)
		_ = u.saveState(st)
		_ = u.inst.RestartService(ctx) // the running agent re-reads the state and reports
		return err
	}
	st.Phase, st.Deadline = PhaseAwaitingConfirm, u.clock.Now().Add(st.ConfirmTimeout)
	if err := u.saveState(st); err != nil {
		return err
	}
	if err := u.install(ctx, &st); err != nil {
		return u.rollback(ctx, st, ReasonInstallFailed, err)
	}
	for u.clock.Now().Before(st.Deadline) {
		if cur, err := u.loadState(); err == nil && cur.Phase == PhaseConfirmed {
			u.finish(cur)
			return nil
		}
		if err := u.clock.Sleep(ctx, pollInterval); err != nil {
			return err
		}
	}
	return u.rollback(ctx, st, ReasonStartTimeout, errors.New("new version did not confirm in time"))
}

// inside reports whether p lies in the staging directory.
func (u *Updater) inside(p string) bool {
	rel, err := filepath.Rel(u.cfg.StagingDir, filepath.Clean(p))
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..")
}

// reverify checks the staged artifact again before anything is replaced.
func (u *Updater) reverify(st State) error {
	if !u.inside(st.Artifact) {
		return fail(ReasonChecksumMismatch, errors.New("artifact outside the staging directory"))
	}
	m, err := u.cfg.Keys.Verify(st.Manifest, st.Signature, st.KeyID)
	if err != nil {
		return err
	}
	if m.Version != st.ToVersion {
		return fail(ReasonVersionMismatch, fmt.Errorf("manifest %s, state %s", m.Version, st.ToVersion))
	}
	entry, err := m.Artifact(st.Platform)
	if err != nil {
		return err
	}
	if entry.SHA256 != st.ArtifactSHA256 || st.Platform.InstallType != st.InstallType {
		return fail(ReasonChecksumMismatch, errors.New("state disagrees with the signed manifest"))
	}
	return u.fileMatches(st.Artifact, entry)
}

func (u *Updater) install(ctx context.Context, st *State) error {
	if st.InstallType != agentrelease.InstallBinary {
		return u.inst.InstallPackage(ctx, st.InstallType, st.Artifact, st.AllowDowngrade)
	}
	prev, err := u.inst.SwapBinary(ctx, st.Artifact, st.Executable)
	if err != nil {
		return err
	}
	st.PreviousBinary = prev
	if err := u.saveState(*st); err != nil {
		return err
	}
	return u.inst.RestartService(ctx)
}

// rollback restores the previous version and records the outcome; the old
// agent reports it when it starts.
func (u *Updater) rollback(ctx context.Context, st State, reason string, cause error) error {
	st.Phase, st.Reason = PhaseRollingBack, reason
	_ = u.saveState(st)
	restored := false
	if st.InstallType != agentrelease.InstallBinary && st.RollbackArtifact != "" && u.inside(st.RollbackArtifact) &&
		u.fileMatches(st.RollbackArtifact, agentrelease.Artifact{Size: u.sizeOf(st.RollbackArtifact), SHA256: st.RollbackSHA256}) == nil {
		restored = u.inst.InstallPackage(ctx, st.InstallType, st.RollbackArtifact, true) == nil
	}
	if !restored && st.PreviousBinary != "" {
		if st.InstallType != agentrelease.InstallBinary {
			st.Reason = ReasonPackageDBMismatch
		}
		restored = u.inst.RestoreBinary(ctx, st.PreviousBinary, st.Executable) == nil
	}
	st.Phase = PhaseRolledBack
	if !restored {
		st.Phase, st.Reason = PhaseFailed, ReasonInstallFailed
	}
	_ = u.saveState(st)
	if err := u.inst.RestartService(ctx); err != nil {
		return fmt.Errorf("selfupdate: rollback (%s): restart: %w", reason, err)
	}
	return fmt.Errorf("selfupdate: rolled back (%s): %w", st.Reason, cause)
}

func (u *Updater) sizeOf(p string) int64 {
	st, err := u.fs.Stat(p)
	if err != nil {
		return 0
	}
	return st.Size
}
