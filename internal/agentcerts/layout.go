package agentcerts

import (
	"errors"
	"io/fs"
	"path"
	"regexp"
	"strconv"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
)

// Store layout (contracts/agent-config.md §2).
const (
	dirLive    = "live"
	dirArchive = "archive"
	dirRenewal = "renewal"

	fileCert      = "cert.pem"
	fileChain     = "chain.pem"
	fileFullChain = "fullchain.pem"
	fileKey       = "privkey.pem"

	// maxReadBytes bounds every file the store reads back.
	maxReadBytes = 1 << 20
	// maxGenSuffix bounds same-second generation name collisions.
	maxGenSuffix = 9
)

// errUnsafe marks a store directory or link that is not what the store
// created: a symlink where a directory belongs, a directory not owned by
// root, or a live link that is a real directory.
var errUnsafe = errors.New("agentcerts: unsafe directory")

// genRE matches a generation name: UTC timestamp, the first 16 hex digits of
// the serial and an optional same-second suffix.
var genRE = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z-[0-9a-f]{1,16}(-[1-9])?$`)

func livePath(name string) string    { return dirLive + "/" + name }
func archivePath(name string) string { return dirArchive + "/" + name }
func metaPath(name string) string    { return dirRenewal + "/" + name + ".json" }
func genPath(name, gen string) string {
	return dirArchive + "/" + name + "/" + gen
}

// linkTarget is the relative target of live/<name> for a generation.
func linkTarget(name, gen string) string { return "../" + genPath(name, gen) }

// parseTarget returns the generation a live link target names, if the
// target is exactly ../archive/<name>/<generation>.
func parseTarget(name, target string) (string, bool) {
	prefix := "../" + archivePath(name) + "/"
	if len(target) <= len(prefix) || target[:len(prefix)] != prefix {
		return "", false
	}
	gen := target[len(prefix):]
	return gen, genRE.MatchString(gen)
}

// ensureLayout creates or checks the store directories for name: the root,
// live, archive, archive/<name> and renewal. Existing directories must be
// real directories owned by root; their group and mode are corrected.
func (s *Store) ensureLayout(name string, gid int) error {
	for _, rel := range []string{".", dirLive, dirArchive, archivePath(name), dirRenewal} {
		if err := s.ensureDir(rel, gid); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureDir(rel string, gid int) error {
	fi, err := s.fs.Lstat(rel)
	if errors.Is(err, fs.ErrNotExist) {
		if err := s.fs.Mkdir(rel, 0o700); err != nil {
			return err
		}
		if err := s.fs.Lchown(rel, s.root, gid); err != nil {
			return err
		}
		return s.fs.Chmod(rel, s.cfg.DirMode)
	}
	if err != nil {
		return err
	}
	if !fi.IsDir() || fi.UID != s.root {
		return errUnsafe
	}
	return s.fixDrift(rel, fi, s.cfg.DirMode, s.root, gid)
}

// fixDrift corrects the owner and mode of an entry in place.
func (s *Store) fixDrift(rel string, fi FileInfo, mode fs.FileMode, uid, gid int) error {
	if fi.UID != uid || fi.GID != gid {
		if err := s.fs.Lchown(rel, uid, gid); err != nil {
			return err
		}
	}
	if fi.Mode.Perm() != mode {
		return s.fs.Chmod(rel, mode)
	}
	return nil
}

// current is the installed state of a name: the generation live/<name>
// points to ("" when none) and its metadata (nil when absent or unreadable).
type current struct {
	gen     string
	genInfo FileInfo
	meta    *Metadata
}

// current reads live/<name> without following it. A live entry that is not
// a symlink is unsafe; a link to anything but an existing generation of the
// name is ignored (it is replaced by the next swap, never followed).
func (s *Store) current(name string) (current, error) {
	var cur current
	fi, err := s.fs.Lstat(livePath(name))
	if errors.Is(err, fs.ErrNotExist) {
		return cur, nil
	}
	if err != nil {
		return cur, err
	}
	if !fi.IsSymlink() {
		return cur, errUnsafe
	}
	target, err := s.fs.Readlink(livePath(name))
	if err != nil {
		return cur, err
	}
	gen, ok := parseTarget(name, target)
	if !ok {
		s.logf("certs: name %s: live link does not point into the archive; replacing it", name)
		return cur, nil
	}
	gi, err := s.fs.Lstat(genPath(name, gen))
	if err != nil || !gi.IsDir() || gi.UID != s.root {
		return cur, nil
	}
	cur.gen, cur.genInfo = gen, gi
	if m := s.readMeta(name); m != nil && m.Generation == gen {
		cur.meta = m
	}
	return cur, nil
}

// currentKey reads the private key of the current generation (ok=false
// when the generation has none).
func (s *Store) currentKey(name, gen string) ([]byte, bool, error) {
	k, err := s.fs.ReadFile(genPath(name, gen)+"/"+fileKey, certmaterial.MaxKeyBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return k, true, nil
}

// generation is the base generation name of a bundle installed now.
func (s *Store) generation(serial string) string {
	if len(serial) > 16 {
		serial = serial[:16]
	}
	return s.now().UTC().Format("20060102T150405Z") + "-" + serial
}

// stage writes a new generation directory (0700 while staging) with the
// four files, fsyncs it and gives it the configured mode. On any error the
// staged directory is removed.
func (s *Store) stage(name string, b certmaterial.Bundle, key []byte, uid, gid int) (string, error) {
	gen, err := s.freeGeneration(name, s.generation(b.Serial))
	if err != nil {
		return "", err
	}
	rel := genPath(name, gen)
	if err := s.fs.Mkdir(rel, 0o700); err != nil {
		return "", err
	}
	if err := s.writeGeneration(rel, b, key, uid, gid); err != nil {
		_ = s.fs.RemoveAll(rel)
		return "", err
	}
	return gen, nil
}

func (s *Store) writeGeneration(rel string, b certmaterial.Bundle, key []byte, uid, gid int) error {
	type file struct {
		name string
		data []byte
		mode fs.FileMode
	}
	files := []file{{fileCert, b.CertPEM, s.cfg.CertMode}, {fileChain, b.ChainPEM, s.cfg.CertMode},
		{fileFullChain, b.FullChainPEM, s.cfg.CertMode}}
	if len(key) > 0 {
		files = append(files, file{fileKey, key, s.cfg.KeyMode})
	}
	if err := s.fs.Lchown(rel, s.root, gid); err != nil {
		return err
	}
	for _, f := range files {
		if err := s.fs.WriteFile(rel+"/"+f.name, f.data, f.mode, uid, gid); err != nil {
			return err
		}
	}
	if err := s.fs.SyncDir(rel); err != nil {
		return err
	}
	if err := s.fs.Chmod(rel, s.cfg.DirMode); err != nil {
		return err
	}
	return s.fs.SyncDir(path.Dir(rel))
}

// freeGeneration returns base, or base-N when a generation of that name
// already exists (two installs within one second).
func (s *Store) freeGeneration(name, base string) (string, error) {
	gen := base
	for i := 1; ; i++ {
		_, err := s.fs.Lstat(genPath(name, gen))
		if errors.Is(err, fs.ErrNotExist) {
			return gen, nil
		}
		if err != nil {
			return "", err
		}
		if i > maxGenSuffix {
			return "", errors.New("agentcerts: too many generations within one second")
		}
		gen = base + "-" + strconv.Itoa(i)
	}
}

// swap points live/<name> at the generation with one rename(2) of a fresh
// symlink over the old one: readers see the old set or the new set, never a
// mix. Once the rename succeeded the swap is done; a failing directory fsync
// is only logged.
func (s *Store) swap(name, gen string) error {
	tmp := dirLive + "/." + name + ".tmp"
	if err := s.fs.RemoveAll(tmp); err != nil {
		return err
	}
	if err := s.fs.Symlink(linkTarget(name, gen), tmp); err != nil {
		return err
	}
	if err := s.fs.Rename(tmp, livePath(name)); err != nil {
		_ = s.fs.RemoveAll(tmp)
		return err
	}
	if err := s.fs.SyncDir(dirLive); err != nil {
		s.logf("certs: name %s: fsync of live/ failed: %v", name, err)
	}
	return nil
}
