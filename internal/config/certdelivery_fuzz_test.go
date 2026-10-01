package config

import (
	"path"
	"strings"
	"testing"
)

// FuzzAgentCertsConfig: decoding never panics, and an accepted certificates
// section always confines files to a safe absolute directory with
// restrictive modes and an absolute hook path (contracts/agent-config.md §1).
func FuzzAgentCertsConfig(f *testing.F) {
	for _, s := range []string{
		"certificates:\n  directory: /etc/inventory-agent/certs\n",
		"certificates:\n  directory: /tmp/x\n  key_mode: \"0644\"\n",
		"certificates:\n  deploy_hook: ../x\n  dir_mode: \"0777\"\n",
		"certificates:\n  owner: \"0\"\n  group: nginx\n  keep_previous: 9\n",
		"certificates: [1, 2]\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		a, err := decodeAgent(raw)
		if err != nil {
			return
		}
		c := a.Certificates
		if c.Validate() != nil {
			return
		}
		d := c.Directory
		if !path.IsAbs(d) || path.Clean(d) != d || d == "/" {
			t.Fatalf("unsafe directory accepted: %q", d)
		}
		for _, p := range forbiddenCertDirs {
			if d == p || strings.HasPrefix(d, p+"/") {
				t.Fatalf("directory under %s accepted: %q", p, d)
			}
		}
		dm, cm, km := c.Modes()
		if dm&0o022 != 0 || dm&0o700 != 0o700 || cm&0o022 != 0 || cm&0o400 == 0 || (km != 0o600 && km != 0o640) {
			t.Fatalf("unsafe modes accepted: %o %o %o", dm, cm, km)
		}
		if c.DeployHook != "" && (!path.IsAbs(c.DeployHook) || path.Clean(c.DeployHook) != c.DeployHook) {
			t.Fatalf("unsafe hook accepted: %q", c.DeployHook)
		}
		if c.KeepPrevious < 0 || c.KeepPrevious > 5 || c.HookTimeoutSeconds < 30 || c.HookTimeoutSeconds > 1800 {
			t.Fatalf("bounds not enforced: %+v", c)
		}
	})
}
