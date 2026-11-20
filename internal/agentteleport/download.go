package agentteleport

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Origin is the only place Teleport is downloaded from. It is not
// configurable, by the server or locally.
const Origin = "https://cdn.teleport.dev"

// Download bounds.
const (
	MaxArchiveBytes = 512 << 20
	maxSumBytes     = 4 << 10
	// archiveBinary is the one archive entry taken (matched, never used as
	// a path).
	archiveBinary = "teleport/teleport"
)

var sumRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// archiveName is the release archive of version for arch.
func archiveName(version, arch string) string {
	return fmt.Sprintf("teleport-v%s-linux-%s-bin.tar.gz", version, arch)
}

// get fetches url with ctx; a status other than 200 is an error.
func (in *Installer) get(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := in.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// expectedSum reads the published SHA-256 of the archive (the first field
// of its .sha256 file).
func (in *Installer) expectedSum(ctx context.Context, base string) (string, error) {
	body, err := in.get(ctx, base+".sha256")
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(body, maxSumBytes))
	if err != nil {
		return "", err
	}
	f := strings.Fields(string(raw))
	if len(f) == 0 || !sumRE.MatchString(f[0]) {
		return "", errors.New("malformed checksum file")
	}
	return f[0], nil
}

// download stores the release archive of version in the staging directory
// (at most MaxArchiveBytes) and checks it against the published SHA-256.
// It returns the staged file's path (the caller removes it).
func (in *Installer) download(ctx context.Context, version string) (string, *failure) {
	url := in.base + "/" + archiveName(version, in.arch)
	want, err := in.expectedSum(ctx, url)
	if err != nil {
		return "", fail(reasonDownload, "checksum file: "+safeErr(err))
	}
	body, err := in.get(ctx, url)
	if err != nil {
		return "", fail(reasonDownload, "archive: "+safeErr(err))
	}
	defer func() { _ = body.Close() }()
	_ = os.MkdirAll(in.path(stagingRel), 0o700) // CreateTemp fails if it could not
	f, err := os.CreateTemp(in.path(stagingRel), "teleport-*.tar.gz")
	if err != nil {
		return "", fail(reasonInstall, "staging file")
	}
	name := f.Name()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, in.maxArchive+1))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	switch {
	case err != nil:
		_ = os.Remove(name)
		return "", fail(reasonDownload, "archive: "+safeErr(err))
	case n > in.maxArchive:
		_ = os.Remove(name)
		return "", fail(reasonDownload, "archive over 512 MiB")
	case hex.EncodeToString(h.Sum(nil)) != want:
		_ = os.Remove(name)
		return "", fail(reasonChecksum, "archive SHA-256 does not match the published checksum")
	}
	return name, nil
}

// extractBinary copies the archive's teleport binary (a regular file named
// exactly teleport/teleport, at most limit bytes) to a new temporary
// file next to dst, mode 0755, and returns that file's path. Nothing else
// of the archive is written.
func extractBinary(archive, dstDir string, limit int64) (string, error) {
	f, err := os.Open(archive) // #nosec G304 -- the agent's own staging file
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(bufio.NewReader(f))
	if err != nil {
		return "", err
	}
	tr := tar.NewReader(zr)
	for {
		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				err = errors.New("archive has no teleport binary")
			}
			return "", err
		}
		if strings.TrimPrefix(hdr.Name, "./") != archiveBinary {
			continue
		}
		if hdr.Typeflag != tar.TypeReg || hdr.Size < 1 || hdr.Size > limit {
			return "", errors.New("teleport binary is not a regular file")
		}
		return writeTemp(dstDir, ".teleport-", 0o755, io.LimitReader(tr, hdr.Size))
	}
}

// writeTemp writes r to a new file in dir (O_EXCL, so never through an
// existing name or symlink), syncs it and returns its path.
func writeTemp(dir, prefix string, mode os.FileMode, r io.Reader) (string, error) {
	f, err := os.CreateTemp(dir, prefix+"*")
	if err != nil {
		return "", err
	}
	name := f.Name()
	_, err = io.Copy(f, r)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(name)
		return "", err
	}
	return name, nil
}

// replace writes data to dst atomically: a temporary file in dst's
// directory (mode), then rename.
func replace(dst string, mode os.FileMode, data []byte) error {
	dir := filepath.Dir(dst)
	_ = os.MkdirAll(dir, 0o755) // #nosec G301 -- /etc, /etc/systemd/system: standard system directories; writeTemp fails if it could not be made
	tmp, err := writeTemp(dir, "."+filepath.Base(dst)+"-", mode, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
