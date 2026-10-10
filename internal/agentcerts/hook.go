package agentcerts

import (
	"context"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certmaterial"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Hook exit codes reported besides 0..255.
const (
	HookNotRun   = -1  // no hook configured, or refused
	HookTimedOut = 256 // killed at the timeout
	hookNoStart  = 127 // the file could not be executed
)

// hookPath is the fixed PATH of the hook environment.
const hookPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// hookResult maps a hook run to the report: reason is "" on success.
type hookResult struct {
	code   int
	reason string
	detail string
}

// runHook checks and runs the locally configured hook for an installed name
// (contracts/agent-config.md §4). Its output goes to the local log only.
func (s *Store) runHook(ctx context.Context, req Request, b certmaterial.Bundle, hasKey, renewal bool) hookResult {
	if why := CheckHook(s.hooks, s.cfg.Hook, s.root); why != "" {
		s.logf("certs: name %s: deploy hook %s refused: %s", req.Name, s.cfg.Hook, why)
		return hookResult{code: HookNotRun, reason: store.ReasonHookRefused, detail: why}
	}
	out := s.hooks.Run(ctx, HookSpec{Path: s.cfg.Hook, Dir: path.Join(s.cfg.Dir, livePath(req.Name)),
		Env: hookEnv(s.cfg.Dir, req.Name, req.CertificateID, b, hasKey, renewal), Timeout: s.cfg.HookTimeout})
	code, reason, detail := HookVerdict(out, s.cfg.HookTimeout, func(format string, args ...any) {
		s.logf("certs: name %s: "+format, append([]any{req.Name}, args...)...)
	})
	return hookResult{code: code, reason: reason, detail: detail}
}

// HookVerdict maps a hook run to the report (shared with the module writer,
// feature 037): exit code, reason ("" on success) and a short detail that
// never holds hook output. The output excerpt (at most MaxHookOutput bytes)
// goes to logf only.
func HookVerdict(out HookOutcome, timeout time.Duration, logf func(format string, args ...any)) (code int, reason, detail string) {
	if len(out.Output) > 0 {
		excerpt := out.Output
		if len(excerpt) > MaxHookOutput {
			excerpt = excerpt[:MaxHookOutput]
		}
		logf("deploy hook output: %q", excerpt)
	}
	switch {
	case out.Err != nil:
		logf("deploy hook could not be started: %v", out.Err)
		return hookNoStart, store.ReasonHookFailed, "hook could not be started"
	case out.TimedOut:
		return HookTimedOut, store.ReasonHookTimeout, "hook killed after " + timeout.Round(time.Second).String()
	case out.ExitCode != 0:
		return out.ExitCode, store.ReasonHookFailed, "hook exited with code " + strconv.Itoa(out.ExitCode)
	}
	return 0, "", ""
}

// CheckHook returns why hook may not run ("" when it may), shared with the
// module writer (feature 037): it must be an absolute path to a regular
// file (not a symlink) owned by rootUID, executable by its owner, not
// writable by group or others, and its directory and every ancestor up to
// "/" must be rootUID-owned directories not writable by group or others (a
// writable ancestor would let a local user rename the path and substitute
// the hook that runs as root).
func CheckHook(r HookRunner, hook string, rootUID int) string {
	if !path.IsAbs(hook) { // the configuration refuses it too
		return "hook path is not absolute"
	}
	fi, err := r.Lstat(hook)
	switch {
	case err != nil:
		return "hook not found"
	case !fi.IsRegular():
		return "hook is not a regular file"
	case fi.UID != rootUID:
		return "hook is not owned by root"
	case fi.Mode.Perm()&0o022 != 0:
		return "hook is writable by group or others"
	case fi.Mode.Perm()&0o100 == 0:
		return "hook is not executable"
	}
	for dir := path.Dir(hook); ; dir = path.Dir(dir) {
		di, err := r.Stat(dir)
		if err != nil || !di.IsDir() || di.UID != rootUID || di.Mode.Perm()&0o022 != 0 {
			return "hook directory " + dir + " is not root-owned or is writable by group or others"
		}
		if dir == "/" {
			return ""
		}
	}
}

// hookEnv is the complete hook environment: PATH, LANG and the v3 LCM_*
// variables. Certificate values come from the agent's own parse of the PEM;
// nothing is inherited from the agent's environment.
func hookEnv(dir, name, certificateID string, b certmaterial.Bundle, hasKey, renewal bool) []string {
	live := path.Join(dir, livePath(name))
	keyPath := ""
	if hasKey {
		keyPath = live + "/" + fileKey
	}
	return []string{
		"PATH=" + hookPath,
		"LANG=C.UTF-8",
		"LCM_CERT_NAME=" + name,
		"LCM_CERT_DIR=" + live,
		"LCM_CERT_PATH=" + live + "/" + fileCert,
		"LCM_KEY_PATH=" + keyPath,
		"LCM_CHAIN_PATH=" + live + "/" + fileChain,
		"LCM_FULLCHAIN_PATH=" + live + "/" + fileFullChain,
		"LCM_COMMON_NAME=" + clean(b.CommonName),
		"LCM_DNS_NAMES=" + clean(strings.Join(b.DNSNames, ",")),
		"LCM_IP_ADDRESSES=" + strings.Join(b.IPAddresses, ","),
		"LCM_SERIAL_NUMBER=" + b.Serial,
		"LCM_EXPIRES_AT=" + b.NotAfter.UTC().Format(time.RFC3339),
		"LCM_IS_RENEWAL=" + strconv.FormatBool(renewal),
		"LCM_CERTIFICATE_ID=" + clean(certificateID),
	}
}

// clean drops control characters (an environment value never carries a
// newline or NUL from a certificate field).
func clean(v string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, v)
}
