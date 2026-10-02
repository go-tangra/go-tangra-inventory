package agentcerts

import (
	"bytes"
	"errors"
	"io/fs"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
)

// unchanged reports whether the current generation already holds exactly
// this bundle (research D11): the metadata fingerprint matches, every file
// has the expected content and the key is the delivered one (or, for a
// certificate-only bundle, a key matching the leaf). Content mismatches or
// missing files mean "reinstall"; owner or mode drift alone is fixed in
// place. Nothing is written unless everything else matches.
func (s *Store) unchanged(name string, cur current, b certmaterial.Bundle, uid, gid int) (bool, error) {
	if cur.gen == "" || cur.meta == nil || cur.meta.Fingerprint != b.Fingerprint {
		return false, nil
	}
	gen := genPath(name, cur.gen)
	type want struct {
		rel  string
		data []byte
		mode fs.FileMode
	}
	files := []want{{gen + "/" + fileCert, b.CertPEM, s.cfg.CertMode}, {gen + "/" + fileChain, b.ChainPEM, s.cfg.CertMode},
		{gen + "/" + fileFullChain, b.FullChainPEM, s.cfg.CertMode}}
	keyRel := gen + "/" + fileKey
	switch {
	case b.HasKey:
		files = append(files, want{keyRel, b.KeyPEM, s.cfg.KeyMode})
	case cur.meta.HasKey:
		k, err := s.fs.ReadFile(keyRel, certmaterial.MaxKeyBytes)
		if err != nil || !keyMatches(b, k) {
			return false, nil
		}
		files = append(files, want{keyRel, k, s.cfg.KeyMode})
	default:
		if _, err := s.fs.Lstat(keyRel); !errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
	}
	infos := make([]FileInfo, len(files))
	for i, f := range files {
		fi, err := s.fs.Lstat(f.rel)
		if err != nil || !fi.IsRegular() {
			return false, nil
		}
		data, err := s.fs.ReadFile(f.rel, maxReadBytes)
		if err != nil || !bytes.Equal(data, f.data) {
			return false, nil
		}
		infos[i] = fi
	}
	// Content matches: correct owner and mode drift in place.
	if err := s.fixDrift(gen, cur.genInfo, s.cfg.DirMode, s.root, gid); err != nil {
		return false, err
	}
	for i, f := range files {
		if err := s.fixDrift(f.rel, infos[i], f.mode, uid, gid); err != nil {
			return false, err
		}
	}
	if mi, err := s.fs.Lstat(metaPath(name)); err == nil && mi.IsRegular() {
		if err := s.fixDrift(metaPath(name), mi, s.cfg.CertMode, uid, gid); err != nil {
			return false, err
		}
	}
	return true, nil
}
