//go:build windows

package collector

import (
	"strings"

	"golang.org/x/sys/windows/registry"
)

// machineID reads MachineGuid from the Windows registry (Cryptography key).
// Empty when the key cannot be opened.
func machineID() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, `SOFTWARE\Microsoft\Cryptography`, registry.QUERY_VALUE|registry.WOW64_64KEY)
	if err != nil {
		return ""
	}
	defer k.Close()

	guid, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(guid)
}
