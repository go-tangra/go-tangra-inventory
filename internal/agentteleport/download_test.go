package agentteleport

import (
	"archive/tar"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExtractBinary(t *testing.T) {
	dir := t.TempDir()
	put := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if _, err := extractBinary(filepath.Join(dir, "missing"), dir, 100); err == nil {
		t.Fatal("missing archive")
	}
	if _, err := extractBinary(put("plain", []byte("not gzip")), dir, 100); err == nil {
		t.Fatal("not gzip")
	}
	good := goodArchive(t)
	if _, err := extractBinary(put("trunc", good[:len(good)-30]), dir, 100); err == nil {
		t.Fatal("truncated")
	}
	link := tarGz(t, entry{"teleport/teleport", tar.TypeSymlink, ""})
	if _, err := extractBinary(put("link", link), dir, 100); err == nil || !strings.Contains(err.Error(), "regular") {
		t.Fatalf("symlink: %v", err)
	}
	empty := tarGz(t, entry{"teleport/teleport", tar.TypeReg, ""})
	if _, err := extractBinary(put("empty", empty), dir, 100); err == nil {
		t.Fatal("empty binary")
	}
	if _, err := extractBinary(put("big", good), dir, 3); err == nil {
		t.Fatal("over limit")
	}
	dot := tarGz(t, entry{"../teleport/teleport", tar.TypeReg, "evil"}, entry{"./teleport/teleport", tar.TypeReg, "ok"})
	p, err := extractBinary(put("dot", dot), dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "ok" || filepath.Dir(p) != dir {
		t.Fatalf("extracted %s = %q", p, b)
	}
	if _, err := extractBinary(put("good", good), filepath.Join(dir, "nodir"), 100); err == nil {
		t.Fatal("no destination")
	}
}

func TestWriteTempCopyError(t *testing.T) {
	dir := t.TempDir()
	if _, err := writeTemp(dir, "x-", 0o600, &errReader{data: []byte("ab")}); err == nil {
		t.Fatal("copy error")
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 0 {
		t.Fatalf("left %v", ents)
	}
}
