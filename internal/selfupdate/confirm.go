package selfupdate

import (
	"context"
	"errors"
	"io/fs"
	"sort"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

// Resume finishes an upgrade in flight; the daemon calls it after its first
// successful command stream connect and inventory submit. A new version
// awaiting confirmation reports succeeded and marks the state confirmed
// (the helper then cleans up); an old version that was put back reports the
// rollback (or failure) with its reason; a confirmed state is cleaned up.
func (u *Updater) Resume(ctx context.Context) error {
	st, err := u.loadState()
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	r := Report{RequestID: st.RequestID, FromVersion: st.FromVersion, ToVersion: st.ToVersion, Reason: st.Reason}
	switch {
	case st.Phase == PhaseAwaitingConfirm && u.cfg.Version == st.ToVersion:
		r.State = StateSucceeded
		if err := u.client.Report(ctx, r); err != nil {
			return err // retried on the next connect; the helper keeps waiting
		}
		st.Phase = PhaseConfirmed
		return u.saveState(st)
	case (st.Phase == PhaseRolledBack || st.Phase == PhaseFailed) && u.cfg.Version == st.FromVersion:
		r.State = StateRolledBack
		if st.Phase == PhaseFailed {
			r.State = StateFailed
		}
		if err := u.client.Report(ctx, r); err != nil {
			return err
		}
		u.finish(st)
	case st.Phase == PhaseConfirmed && u.cfg.Version == st.ToVersion:
		u.finish(st)
	}
	return nil
}

// finish removes the state file and the lock and keeps only the staging
// directories of the two versions involved.
func (u *Updater) finish(st State) {
	_ = u.fs.Remove(u.path(stateFile))
	u.unlock()
	entries, err := u.fs.ReadDir(u.cfg.StagingDir)
	if err != nil {
		return
	}
	sort.Strings(entries)
	for _, e := range entries {
		if e == st.FromVersion || e == st.ToVersion || !agentrelease.IsRelease(e) {
			continue
		}
		_ = u.fs.RemoveAll(u.path(e))
	}
}
