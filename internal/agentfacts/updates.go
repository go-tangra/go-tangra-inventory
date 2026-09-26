package agentfacts

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// maxPkgField bounds package names and versions taken from command output.
const maxPkgField = 256

// Managers is the detection order of the supported package managers.
var Managers = []string{"apt", "dnf", "yum", "apk", "pacman"}

// Pending is one package with a newer version available.
type Pending struct {
	Name      string
	Installed string // "" when the manager output does not say
	Available string
	Security  bool
}

func cleanField(s string) bool {
	if s == "" || len(s) > maxPkgField || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r <= 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func pending(name, installed, available string, security bool) (Pending, bool) {
	if !cleanField(name) || !cleanField(available) || (installed != "" && !cleanField(installed)) {
		return Pending{}, false
	}
	return Pending{Name: name, Installed: installed, Available: available, Security: security}, true
}

// ParseAptSimulate parses `apt-get -s dist-upgrade` (LANG=C) "Inst" lines:
// "Inst <name> [<installed>] (<available> <origins…> [<arch>])". Lines without
// an installed version (new dependencies) are not updates. A security update
// is one whose origins include a "-security" suite.
func ParseAptSimulate(out string) []Pending {
	var res []Pending
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, "Inst ")
		if !ok {
			continue
		}
		name, rest, ok := strings.Cut(rest, " ")
		if !ok || !strings.HasPrefix(rest, "[") {
			continue
		}
		installed, rest, ok := strings.Cut(rest[1:], "]")
		if !ok {
			continue
		}
		// "(<available> <origins> [<arch>])", possibly followed by " []".
		rest = strings.TrimSpace(rest)
		end := strings.LastIndexByte(rest, ')')
		if !strings.HasPrefix(rest, "(") || end < 1 {
			continue
		}
		inner := strings.Fields(rest[1:end])
		if len(inner) < 1 {
			continue
		}
		sec := strings.Contains(strings.ToLower(strings.Join(inner[1:], " ")), "-security")
		if p, ok := pending(name, installed, inner[0], sec); ok {
			res = append(res, p)
		}
	}
	return res
}

// ParseDnfCheckUpdate parses `dnf|yum -q -C check-update` output
// ("name.arch  [epoch:]version-release  repo", long names wrapped onto their
// own line), stopping at the "Obsoleting Packages" section.
func ParseDnfCheckUpdate(out string) []Pending {
	var res []Pending
	carry := ""
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "Obsoleting Packages") {
			break
		}
		f := strings.Fields(line)
		switch {
		case len(f) == 1 && strings.Contains(f[0], "."):
			carry = f[0]
			continue
		case len(f) == 2 && carry != "":
			f = []string{carry, f[0], f[1]}
		}
		carry = ""
		if len(f) != 3 || !strings.Contains(f[0], ".") {
			continue
		}
		name := f[0][:strings.LastIndexByte(f[0], '.')]
		if p, ok := pending(name, "", stripEpoch(f[1]), false); ok {
			res = append(res, p)
		}
	}
	return res
}

// ParseDnfSecurity parses `dnf -q -C updateinfo list --updates security`
// ("ADVISORY SEVERITY/Sec. name-[epoch:]version-release.arch") into the set
// of package names with a security update.
func ParseDnfSecurity(out string) map[string]bool {
	res := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 3 {
			continue
		}
		if n := nevraName(f[2]); cleanField(n) {
			res[n] = true
		}
	}
	return res
}

// nevraName extracts the name of name-[epoch:]version-release.arch.
func nevraName(nevra string) string {
	i := strings.LastIndexByte(nevra, '.')
	if i < 0 {
		return ""
	}
	s := nevra[:i]
	for k := 0; k < 2; k++ {
		j := strings.LastIndexByte(s, '-')
		if j <= 0 {
			return ""
		}
		s = s[:j]
	}
	return s
}

func stripEpoch(v string) string {
	if i := strings.IndexByte(v, ':'); i >= 0 {
		if _, err := strconv.Atoi(v[:i]); err == nil {
			return v[i+1:]
		}
	}
	return v
}

// ParseApkUpgradable parses `apk -u list` lines
// "name-ver-rel arch {origin} (license) [upgradable from: name-oldver-rel]".
func ParseApkUpgradable(out string) []Pending {
	var res []Pending
	for _, line := range strings.Split(out, "\n") {
		_, from, ok := strings.Cut(line, "[upgradable from: ")
		if !ok {
			continue
		}
		from = strings.TrimSuffix(strings.TrimSpace(from), "]")
		f := strings.Fields(line)
		name, avail := splitApk(f[0])
		if name == "" || !strings.HasPrefix(from, name+"-") {
			continue
		}
		if p, ok := pending(name, strings.TrimPrefix(from, name+"-"), avail, false); ok {
			res = append(res, p)
		}
	}
	return res
}

// splitApk splits "name-ver-rel" into name and "ver-rel".
func splitApk(s string) (string, string) {
	j := strings.LastIndexByte(s, '-')
	if j <= 0 {
		return "", ""
	}
	i := strings.LastIndexByte(s[:j], '-')
	if i <= 0 {
		return "", ""
	}
	return s[:i], s[i+1:]
}

// ParseCheckupdates parses pacman-contrib `checkupdates` lines
// "name installed -> available".
func ParseCheckupdates(out string) []Pending {
	var res []Pending
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) != 4 || f[2] != "->" {
			continue
		}
		if p, ok := pending(f[0], f[1], f[3], false); ok {
			res = append(res, p)
		}
	}
	return res
}

// RebootRequired decides the reboot-required tristate: the reboot-required
// flag file (Debian/Ubuntu) → true; RHEL `needs-restarting -r` exit 1 → true,
// 0 → false (-1 = not run); needrestart's NEEDRESTART-KSTA (≥ 2 pending
// kernel → true, 1 → false); otherwise unknown.
func RebootRequired(flagFile bool, needsRestartingExit int, needrestart string) string {
	switch {
	case flagFile:
		return store.TriTrue
	case needsRestartingExit == 1:
		return store.TriTrue
	case needsRestartingExit == 0:
		return store.TriFalse
	}
	for _, line := range strings.Split(needrestart, "\n") {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "NEEDRESTART-KSTA:")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(v))
		switch {
		case err != nil:
		case n >= 2:
			return store.TriTrue
		case n == 1:
			return store.TriFalse
		}
	}
	return store.TriUnknown
}

// AutomaticUpdates decides whether the OS installs updates by itself: apt
// needs APT::Periodic::Unattended-Upgrade "1" and apt-daily-upgrade.timer
// enabled; dnf the dnf-automatic(-install) timer; yum the yum-cron service.
// A known manager without any of these is false; no manager is unknown.
func AutomaticUpdates(manager, aptConfig string, enabled map[string]bool) string {
	on := false
	switch manager {
	case "apt":
		on = strings.Contains(aptConfig, `APT::Periodic::Unattended-Upgrade "1"`) && enabled["apt-daily-upgrade.timer"]
	case "dnf":
		on = enabled["dnf-automatic.timer"] || enabled["dnf-automatic-install.timer"]
	case "yum":
		on = enabled["yum-cron.service"]
	case "apk", "pacman":
	default:
		return store.TriUnknown
	}
	if on {
		return store.TriTrue
	}
	return store.TriFalse
}

// MergePending sets the available version and security flag on the matching
// installed programs (by name) and appends programs the installed list lacks.
// At most store.MaxPendingUpdates pending entries are applied (dropped counts
// the rest); the returned state carries the full pending/security counts.
func MergePending(programs []store.Program, pend []Pending, security map[string]bool) ([]store.Program, store.UpdateState, uint32) {
	out := append([]store.Program(nil), programs...)
	byName := map[string][]int{}
	for i, p := range out {
		byName[p.Name] = append(byName[p.Name], i)
	}
	var st store.UpdateState
	var dropped uint32
	applied := 0
	for _, p := range pend {
		sec := p.Security || security[p.Name]
		st.PendingCount++
		if sec {
			st.SecurityCount++
		}
		if applied == store.MaxPendingUpdates {
			dropped++
			continue
		}
		applied++
		idx, ok := byName[p.Name]
		if !ok {
			out = append(out, store.Program{Name: p.Name, Version: p.Installed, AvailableVersion: p.Available, SecurityUpdate: sec})
			continue
		}
		for _, i := range idx {
			out[i].AvailableVersion, out[i].SecurityUpdate = p.Available, sec
		}
	}
	return out, st, dropped
}

// UpdateStatus derives the update status: no known manager → unsupported,
// failed/timed-out command → error, otherwise by the pending count.
func UpdateStatus(manager string, err error, pendingCount int) string {
	switch {
	case manager == "":
		return store.UpdateUnsupported
	case err != nil:
		return store.UpdateError
	case pendingCount == 0:
		return store.UpdateUpToDate
	}
	return store.UpdateAvailable
}
