package certmaterial

import (
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

// FuzzValidName: an accepted name is always one safe path component.
func FuzzValidName(f *testing.F) {
	for _, s := range []string{"www", "../x", "a..b", ".hidden", "a/b", `a\b`, "", ".", "x\x00"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ValidName(s) {
			assertSafeName(t, s)
		}
		if n, ok := DefaultName(s); ok {
			assertSafeName(t, n)
		} else if n != "" {
			t.Fatalf("DefaultName(%q) returned %q with ok=false", s, n)
		}
	})
}

func assertSafeName(t *testing.T, s string) {
	t.Helper()
	if !ValidName(s) || s == "" || len(s) > MaxNameLen || strings.ContainsAny(s, `/\`) || strings.Contains(s, "..") ||
		strings.HasPrefix(s, ".") || filepath.Base(s) != s || filepath.Clean(s) != strings.TrimSuffix(s, "/") {
		t.Fatalf("unsafe name accepted: %q", s)
	}
	for _, r := range s {
		if r < 0x21 || r > 0x7e {
			t.Fatalf("non-printable or non-ASCII rune in accepted name %q", s)
		}
	}
}

// FuzzHostTag: an accepted tag has a non-empty bounded key, a bounded value
// and no control characters.
func FuzzHostTag(f *testing.F) {
	for _, s := range []string{"role", "role=web", "=x", "env=prod eu", "a\x00=b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if !ValidTag(s) {
			return
		}
		k, v, _ := strings.Cut(s, "=")
		if k == "" || len(k) > MaxTagKeyLen || len(v) > MaxTagValueLen || !utf8.ValidString(s) {
			t.Fatalf("bad tag accepted: %q", s)
		}
		for _, r := range s {
			if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
				t.Fatalf("control character in accepted tag %q", s)
			}
		}
	})
}
