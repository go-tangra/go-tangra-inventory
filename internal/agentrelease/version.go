package agentrelease

import (
	"strconv"
	"strings"
)

// Floor is the first agent version that upgrades itself: no upgrade (or
// administrator-pinned downgrade) may target an older version, which would
// strand the host on a manual-upgrade agent.
const Floor = "4.4.0"

type version struct {
	major, minor, patch int
	pre                 []string
}

// parseVersion parses MAJOR.MINOR.PATCH[-pre][+build], an optional leading
// "v", and git describe versions (4.3.1~11-gabc1234), which sort as
// pre-releases of their base version.
func parseVersion(s string) (version, bool) {
	s = strings.TrimPrefix(s, "v")
	if i := strings.IndexByte(s, '+'); i >= 0 {
		s = s[:i]
	}
	core, pre, hasPre := strings.Cut(s, "-")
	if i := strings.IndexByte(core, '~'); i >= 0 {
		core, pre, hasPre = s[:i], s[i:], true
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 || (hasPre && pre == "") {
		return version{}, false
	}
	var nums [3]int
	for i, p := range parts {
		n, ok := number(p)
		if !ok {
			return version{}, false
		}
		nums[i] = n
	}
	v := version{major: nums[0], minor: nums[1], patch: nums[2]}
	if hasPre {
		v.pre = strings.Split(pre, ".")
		for _, id := range v.pre {
			if id == "" {
				return version{}, false
			}
		}
	}
	return v, true
}

// number parses a semver numeric identifier (no leading zeros, bounded).
func number(s string) (int, bool) {
	if s == "" || len(s) > 9 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil && n >= 0
}

// Compare orders two versions by semantic version precedence; ok is false
// when either is not a version (e.g. "dev"): such agents are never
// outdated automatically.
func Compare(a, b string) (int, bool) {
	va, okA := parseVersion(a)
	vb, okB := parseVersion(b)
	if !okA || !okB {
		return 0, false
	}
	for _, d := range []int{va.major - vb.major, va.minor - vb.minor, va.patch - vb.patch} {
		if d != 0 {
			return sign(d), true
		}
	}
	return comparePre(va.pre, vb.pre), true
}

func comparePre(a, b []string) int {
	switch {
	case len(a) == 0 && len(b) == 0:
		return 0
	case len(a) == 0:
		return 1 // a release is newer than its pre-releases
	case len(b) == 0:
		return -1
	}
	for i := 0; i < len(a) && i < len(b); i++ {
		na, numA := number(a[i])
		nb, numB := number(b[i])
		switch {
		case numA && numB:
			if na != nb {
				return sign(na - nb)
			}
		case numA:
			return -1 // numeric identifiers sort before alphanumeric ones
		case numB:
			return 1
		case a[i] != b[i]:
			return sign(strings.Compare(a[i], b[i]))
		}
	}
	return sign(len(a) - len(b))
}

func sign(d int) int {
	switch {
	case d < 0:
		return -1
	case d > 0:
		return 1
	}
	return 0
}

// IsRelease reports whether s is a release version (MAJOR.MINOR.PATCH with
// an optional pre-release, no "v", no build or describe suffix).
func IsRelease(s string) bool { return len(s) <= 64 && releaseRE.MatchString(s) }

// BelowFloor reports whether v is a version older than Floor.
func BelowFloor(v string) bool {
	c, ok := Compare(v, Floor)
	return ok && c < 0
}
