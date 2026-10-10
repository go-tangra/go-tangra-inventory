package agentmodules

import (
	"archive/zip"
	"bytes"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Archive bounds (spec 037 edge cases).
const (
	MaxEntries     = 300
	MaxUnpacked    = 16 << 20
	zipEncrypted   = 0x1 // general purpose flag bit 0
	privateDir     = "private"
	envFile        = ".env"
	modeDir        = fs.FileMode(0o750)
	modePrivateDir = fs.FileMode(0o700)
	modeSecret     = fs.FileMode(0o600)
	modeExec       = fs.FileMode(0o755)
	modeFile       = fs.FileMode(0o644)
)

// entry is one checked archive member below <module>/: rel is its path
// relative to the module directory ("" is the module directory itself).
type entry struct {
	rel  string
	dir  bool
	perm fs.FileMode
	file *zip.File
}

// secret reports whether rel is .env or lies below private/.
func secret(rel string) bool {
	return path.Base(rel) == envFile || rel == privateDir || strings.HasPrefix(rel, privateDir+"/")
}

// permFor is the mode written for rel: directories 0750 (private/ and below
// 0700); .env and files below private/ 0600; files the archive marks
// executable 0755; others 0644.
func permFor(rel string, dir bool, archived fs.FileMode) fs.FileMode {
	switch {
	case dir && secret(rel):
		return modePrivateDir
	case dir:
		return modeDir
	case secret(rel):
		return modeSecret
	case archived&0o111 != 0:
		return modeExec
	}
	return modeFile
}

// cleanName validates one member name and returns it without a trailing
// slash ("" when unusable).
func cleanName(name string) string {
	n := strings.TrimSuffix(name, "/")
	if n == "" || strings.ContainsAny(n, "\\\x00") || path.IsAbs(n) || path.Clean(n) != n {
		return ""
	}
	for _, c := range strings.Split(n, "/") {
		if c == "." || c == ".." {
			return ""
		}
	}
	return n
}

// parseArchive checks the archive (contracts/agent-config.md) and returns
// its entries, directories first (parents before children), then files.
// Directories implied by a file's path are added. Nothing is extracted.
func parseArchive(data []byte, module string) ([]entry, *failure) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fail(store.ReasonInvalidBundle, "not a zip archive")
	}
	if len(zr.File) > MaxEntries {
		return nil, fail(store.ReasonTooLarge, "too many entries")
	}
	var total uint64
	dirs := map[string]bool{"": true}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		n := cleanName(f.Name)
		if n == "" || f.Flags&zipEncrypted != 0 {
			return nil, fail(store.ReasonInvalidBundle, "unsafe entry name")
		}
		top, rel, _ := strings.Cut(n, "/")
		isDir := strings.HasSuffix(f.Name, "/")
		typ := f.Mode().Type()
		switch {
		case top != module:
			return nil, fail(store.ReasonInvalidBundle, "entry outside the module directory")
		case isDir && typ != fs.ModeDir, !isDir && typ != 0:
			return nil, fail(store.ReasonInvalidBundle, "entry is not a regular file or directory")
		case !isDir && rel == "":
			return nil, fail(store.ReasonInvalidBundle, "module directory is a file")
		case files[rel] != nil || (isDir && rel != "" && dirs[rel]):
			return nil, fail(store.ReasonInvalidBundle, "duplicate entry")
		}
		if isDir {
			dirs[rel] = true
			continue
		}
		total += f.UncompressedSize64
		if total > MaxUnpacked {
			return nil, fail(store.ReasonTooLarge, "unpacked size over the limit")
		}
		files[rel] = f
	}
	if len(files) == 0 {
		return nil, fail(store.ReasonInvalidBundle, "no files")
	}
	for rel := range files {
		for p := path.Dir(rel); p != "."; p = path.Dir(p) {
			if files[p] != nil {
				return nil, fail(store.ReasonInvalidBundle, "file used as a directory")
			}
			dirs[p] = true
		}
	}
	for rel := range dirs {
		if files[rel] != nil {
			return nil, fail(store.ReasonInvalidBundle, "file used as a directory")
		}
	}
	out := make([]entry, 0, len(dirs)+len(files))
	for rel := range dirs {
		out = append(out, entry{rel: rel, dir: true, perm: permFor(rel, true, 0)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].rel < out[j].rel })
	start := len(out)
	for rel, f := range files {
		out = append(out, entry{rel: rel, perm: permFor(rel, false, f.Mode().Perm()), file: f})
	}
	sort.Slice(out[start:], func(i, j int) bool { return out[start+i].rel < out[start+j].rel })
	return out, nil
}
