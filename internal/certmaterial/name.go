package certmaterial

import (
	"strings"
	"unicode/utf8"
)

// Name and tag bounds (research D17, contracts/inventory-grpc.md).
const (
	MaxNameLen     = 64
	MaxTagKeyLen   = 63
	MaxTagValueLen = 255
)

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

func isNameByte(c byte) bool {
	return isAlnum(c) || c == '.' || c == '_' || c == '-'
}

// ValidName reports whether s is a certificate name: ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$
// without "..". Such a name is a single path component that never starts
// with a dot, so it cannot escape or hide in the agent's directory.
func ValidName(s string) bool {
	if s == "" || len(s) > MaxNameLen || !isAlnum(s[0]) || strings.Contains(s, "..") {
		return false
	}
	for i := 1; i < len(s); i++ {
		if !isNameByte(s[i]) {
			return false
		}
	}
	return true
}

// DefaultName derives a certificate name from a certificate common name:
// lowercased, a leading "*." becomes "wildcard.", every other character
// outside [a-z0-9._-] becomes "_", runs of dots collapse, leading
// characters other than a letter or digit are dropped and the result is cut
// to 64 characters. ok is false (and the name empty) when nothing valid
// remains.
func DefaultName(cn string) (string, bool) {
	s := strings.ToLower(strings.TrimSpace(cn))
	if strings.HasPrefix(s, "*.") {
		s = "wildcard." + s[2:]
	}
	s = strings.Map(func(r rune) rune {
		if isNameRune(r) {
			return r
		}
		return '_'
	}, s)
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '.' && i > 0 && s[i-1] == '.' {
			continue
		}
		b.WriteByte(s[i])
	}
	out := strings.TrimLeftFunc(b.String(), func(r rune) bool { return !isAlnumRune(r) })
	if len(out) > MaxNameLen {
		out = out[:MaxNameLen]
	}
	if !ValidName(out) {
		return "", false
	}
	return out, true
}

func isAlnumRune(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

func isNameRune(r rune) bool {
	return isAlnumRune(r) || r == '.' || r == '_' || r == '-'
}

// ValidTag reports whether s is a host tag selector: "key" or "key=value",
// the key 1-63 printable characters without whitespace or "=", the value at
// most 255 bytes, valid UTF-8 and free of control characters.
func ValidTag(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	k, v, _ := strings.Cut(s, "=")
	if k == "" || len(k) > MaxTagKeyLen || len(v) > MaxTagValueLen {
		return false
	}
	for _, r := range k {
		if r <= 0x20 || isControl(r) {
			return false
		}
	}
	for _, r := range v {
		if r < 0x20 || isControl(r) {
			return false
		}
	}
	return true
}

func isControl(r rune) bool { return r == 0x7f || (r >= 0x80 && r < 0xa0) }
