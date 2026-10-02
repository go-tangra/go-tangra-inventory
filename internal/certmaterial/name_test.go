package certmaterial

import (
	"strings"
	"testing"
)

func TestValidName(t *testing.T) {
	ok := []string{"www", "a", "A", "0", "web-1.example_com", "wildcard.example.com", "x.", strings.Repeat("a", 64)}
	for _, n := range ok {
		if !ValidName(n) {
			t.Errorf("ValidName(%q) = false, want true", n)
		}
	}
	bad := []string{"", ".", "..", ".hidden", "-x", "_x", "a/b", `a\b`, "../x", "a..b", "x..",
		strings.Repeat("a", 65), "a\x00b", "a\nb", "a b", "a\tb", "ü", "wwwé", "a:b", "*.example.com", "a/"}
	for _, n := range bad {
		if ValidName(n) {
			t.Errorf("ValidName(%q) = true, want false", n)
		}
	}
}

func TestDefaultName(t *testing.T) {
	cases := []struct {
		cn, want string
		ok       bool
	}{
		{"www.example.com", "www.example.com", true},
		{"*.example.com", "wildcard.example.com", true},
		{"Www.Example.COM", "www.example.com", true},
		{"my host/with:bad chars", "my_host_with_bad_chars", true},
		{"..evil..name..", "evil.name.", true},
		{"__x", "x", true},
		{"a..b", "a.b", true},
		{strings.Repeat("b", 80), strings.Repeat("b", 64), true},
		{"", "", false},
		{"   ", "", false},
		{"*", "", false},
		{"...", "", false},
		{"üüü", "", false},
	}
	for _, c := range cases {
		got, ok := DefaultName(c.cn)
		if ok != c.ok || got != c.want {
			t.Errorf("DefaultName(%q) = %q,%v want %q,%v", c.cn, got, ok, c.want, c.ok)
		}
		if ok && !ValidName(got) {
			t.Errorf("DefaultName(%q) = %q is not a valid name", c.cn, got)
		}
	}
}

func TestValidTag(t *testing.T) {
	ok := []string{"role", "role=web", "env=prod eu", "k=", strings.Repeat("k", 63), "k=" + strings.Repeat("v", 255), "app.kubernetes.io/name=x"}
	for _, s := range ok {
		if !ValidTag(s) {
			t.Errorf("ValidTag(%q) = false, want true", s)
		}
	}
	bad := []string{"", "=x", "=", strings.Repeat("k", 64), "k=" + strings.Repeat("v", 256), "ro le", "role\x00", "role=a\nb",
		"role=\x7f", "k\tx=y", "k=\xff", "\xff"}
	for _, s := range bad {
		if ValidTag(s) {
			t.Errorf("ValidTag(%q) = true, want false", s)
		}
	}
}

// TestValidCertificateID (T110): the id reaches the hook environment.
func TestValidCertificateID(t *testing.T) {
	for _, id := range []string{"cert-1", "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66", "a:b.c_d", strings.Repeat("a", 128)} {
		if !ValidCertificateID(id) {
			t.Errorf("%q refused", id)
		}
	}
	for _, id := range []string{"", "$(reboot)", "a b", "a;b", "a`b", "a\nb", "é", strings.Repeat("a", 129)} {
		if ValidCertificateID(id) {
			t.Errorf("%q accepted", id)
		}
	}
}
