package agentcerts

import (
	"errors"
	"io/fs"
	"sort"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
)

// generations lists the generation directories of a name, oldest first.
func (s *Store) generations(name string) ([]string, error) {
	entries, err := s.fs.ReadDir(archivePath(name))
	if err != nil {
		return nil, err
	}
	var gens []string
	for _, e := range entries {
		if genRE.MatchString(e) {
			gens = append(gens, e)
		}
	}
	sort.Strings(gens)
	return gens, nil
}

// prune removes generations older than the live one beyond KeepPrevious.
// The live target is never removed. Failures are logged only.
func (s *Store) prune(name, live string) {
	gens, err := s.generations(name)
	if err != nil {
		s.logf("certs: name %s: prune: %v", name, err)
		return
	}
	idx := sort.SearchStrings(gens, live)
	older := gens[:idx]
	if len(older) <= s.cfg.KeepPrevious {
		return
	}
	for _, g := range older[:len(older)-s.cfg.KeepPrevious] {
		if err := s.fs.RemoveAll(genPath(name, g)); err != nil {
			s.logf("certs: name %s: prune %s: %v", name, g, err)
		}
	}
}

// Recover cleans up after a crash (run once at agent start, before any
// install): leftover live/.<name>.tmp links and renewal/.<name>.json.tmp
// files are removed, and generations newer than the one live/<name> points
// to (staged but never switched to) are discarded. A missing store is
// nothing to recover; the store directory and its subdirectories must be
// real directories owned by root.
func (s *Store) Recover() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.listDir("."); err != nil {
		return err
	}
	for _, dir := range []string{dirLive, dirRenewal} {
		if err := s.removeTemps(dir); err != nil {
			return err
		}
	}
	names, err := s.listDir(dirArchive)
	if err != nil {
		return err
	}
	for _, name := range names {
		if !certmaterial.ValidName(name) {
			continue
		}
		if fi, err := s.fs.Lstat(archivePath(name)); err != nil || !fi.IsDir() || fi.UID != s.root {
			continue
		}
		s.discardUnswitched(name)
	}
	return nil
}

// listDir lists a store directory after checking it is safe; a missing
// directory is empty.
func (s *Store) listDir(dir string) ([]string, error) {
	fi, err := s.fs.Lstat(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || fi.UID != s.root {
		return nil, errUnsafe
	}
	return s.fs.ReadDir(dir)
}

func (s *Store) removeTemps(dir string) error {
	names, err := s.listDir(dir)
	if err != nil {
		return err
	}
	for _, n := range names {
		if strings.HasPrefix(n, ".") && strings.HasSuffix(n, ".tmp") {
			if err := s.fs.RemoveAll(dir + "/" + n); err != nil {
				return err
			}
			s.logf("certs: recovery: removed leftover %s/%s", dir, n)
		}
	}
	return nil
}

// discardUnswitched removes generations of name newer than the live one
// (all of them when there is no usable live link).
func (s *Store) discardUnswitched(name string) {
	cur, err := s.current(name)
	if err != nil {
		s.logf("certs: recovery: name %s: %v", name, err)
		return
	}
	gens, err := s.generations(name)
	if err != nil {
		s.logf("certs: recovery: name %s: %v", name, err)
		return
	}
	for _, g := range gens {
		if g > cur.gen {
			if err := s.fs.RemoveAll(genPath(name, g)); err != nil {
				s.logf("certs: recovery: name %s: remove %s: %v", name, g, err)
				continue
			}
			s.logf("certs: recovery: name %s: discarded unswitched generation %s", name, g)
		}
	}
}
