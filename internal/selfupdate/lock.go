package selfupdate

import (
	"errors"
	"io/fs"
	"strings"
)

// lock takes the upgrade lock for requestID. It returns the request id of
// the holder when another upgrade (or the same request) already holds it;
// a lock older than the stale bound plus the confirm timeout is taken over.
func (u *Updater) lock(requestID string) (held string, err error) {
	path := u.path(lockFile)
	err = u.fs.CreateExclusive(path, []byte(requestID), 0o600)
	if err == nil || !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	b, rerr := u.fs.ReadFile(path)
	st, serr := u.fs.Stat(path)
	holder := strings.TrimSpace(string(b))
	if rerr != nil || serr != nil || holder == "" {
		return "unknown", nil
	}
	if cur, err := u.loadState(); err == nil && cur.RequestID == holder && cur.Phase == PhaseConfirmed && u.cfg.Version == cur.ToVersion {
		// This version confirmed that upgrade; only the helper's cleanup
		// (at most one poll interval away) still holds the lock.
		u.finish(cur)
		return u.retake(path, requestID)
	}
	if u.clock.Now().Sub(st.ModTime) <= staleLock+u.cfg.ConfirmTimeout {
		return holder, nil
	}
	u.log.Warn("upgrade: taking over a stale lock", "holder", holder)
	if err := u.fs.Remove(path); err != nil {
		return "", err
	}
	return u.retake(path, requestID)
}

// retake creates the lock again after its previous holder was removed.
func (u *Updater) retake(path, requestID string) (string, error) {
	err := u.fs.CreateExclusive(path, []byte(requestID), 0o600)
	if errors.Is(err, fs.ErrExist) {
		return "unknown", nil // another upgrade took it first
	}
	return "", err
}

func (u *Updater) unlock() { _ = u.fs.Remove(u.path(lockFile)) }
