package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
)

// fetch downloads version (for requestID, or the running version's package
// without one) into staging/<version>/<file>, verifying in the documented
// order: key id, signature over the exact manifest, manifest version and
// the entry for this agent's platform, free space, then size and sha256 of
// the streamed bytes. A partial or failed file is removed.
func (u *Updater) fetch(ctx context.Context, requestID, version string) (string, agentrelease.Artifact, Header, error) {
	var none agentrelease.Artifact
	dl, err := u.client.Download(ctx, requestID, version)
	if err != nil {
		return "", none, Header{}, fail(ReasonDownloadFailed, err)
	}
	defer dl.Close()
	h := dl.Header()
	m, err := u.cfg.Keys.Verify(h.Manifest, h.Signature, h.KeyID)
	if err != nil {
		return "", none, h, err
	}
	if m.Version != version {
		return "", none, h, fail(ReasonVersionMismatch, fmt.Errorf("release %s, requested %s", m.Version, version))
	}
	entry, err := m.Artifact(u.cfg.Platform)
	if err != nil {
		return "", none, h, err
	}
	if h.File != entry.File || h.Size != entry.Size || h.SHA256 != entry.SHA256 {
		return "", none, h, fail(ReasonPlatformMismatch, errors.New("header disagrees with the signed manifest"))
	}
	if free, ferr := u.fs.Free(u.cfg.StagingDir); ferr == nil && free < uint64(2*entry.Size) { // #nosec G115 -- 1..150 MiB
		return "", none, h, fail(ReasonDiskFull, fmt.Errorf("%d bytes free, %d needed", free, 2*entry.Size))
	}
	dir := u.path(version)
	if err := u.fs.MkdirAll(dir, 0o700); err != nil {
		return "", none, h, fail(ReasonDiskFull, err)
	}
	dst := filepath.Join(dir, entry.File)
	if err := u.stream(dl, dst, entry); err != nil {
		_ = u.fs.Remove(dst)
		return "", none, h, err
	}
	return dst, entry, h, nil
}

func (u *Updater) stream(dl Download, dst string, entry agentrelease.Artifact) error {
	w, err := u.fs.Create(dst, 0o600)
	if err != nil {
		return fail(ReasonDiskFull, err)
	}
	v := agentrelease.NewArtifactVerifier(entry)
	var next int64
	for {
		off, data, err := dl.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			_ = w.Close()
			return fail(ReasonDownloadFailed, err)
		}
		if off != next {
			_ = w.Close()
			return fail(ReasonDownloadFailed, fmt.Errorf("chunk at %d, expected %d", off, next))
		}
		if _, err := v.Write(data); err != nil {
			_ = w.Close()
			return err
		}
		if _, err := w.Write(data); err != nil {
			_ = w.Close()
			return fail(ReasonDiskFull, err)
		}
		next += int64(len(data))
	}
	if err := w.Close(); err != nil {
		return fail(ReasonDiskFull, err)
	}
	return v.Finish()
}

// fileMatches reports whether path holds exactly size bytes with sha256.
func (u *Updater) fileMatches(path string, a agentrelease.Artifact) error {
	r, err := u.fs.Open(path)
	if err != nil {
		return err
	}
	defer r.Close()
	v := agentrelease.NewArtifactVerifier(a)
	if _, err := io.Copy(v, r); err != nil {
		return err
	}
	return v.Finish()
}
