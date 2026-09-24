//go:build linux

package collector

import (
	"os"
	"strings"
)

// machineID reads the systemd/D-Bus machine id. Empty when neither file exists.
func machineID() string {
	for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
		b, err := os.ReadFile(p) // #nosec G304 -- fixed, well-known system paths
		if err == nil {
			if id := strings.TrimSpace(string(b)); id != "" {
				return id
			}
		}
	}
	return ""
}
