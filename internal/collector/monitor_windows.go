//go:build windows

package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

type psMonitor struct {
	Manufacturer string `json:"Manufacturer"`
	Model        string `json:"Model"`
	Serial       string `json:"Serial"`
}

// collectMonitors queries WmiMonitorID from root\wmi via PowerShell. The EDID
// strings are stored as uint16 arrays which PowerShell decodes into ASCII.
func collectMonitors() []store.Monitor {
	const script = `
$monitors = @(Get-CimInstance -Namespace root\wmi -ClassName WmiMonitorID -ErrorAction SilentlyContinue | ForEach-Object {
    [PSCustomObject]@{
        Manufacturer = [System.Text.Encoding]::ASCII.GetString($_.ManufacturerName -ne 0)
        Model = [System.Text.Encoding]::ASCII.GetString($_.UserFriendlyName -ne 0)
        Serial = [System.Text.Encoding]::ASCII.GetString($_.SerialNumberID -ne 0)
    }
})
if ($monitors.Count -eq 0) {
    Write-Output '[]'
} elseif ($monitors.Count -eq 1) {
    Write-Output ('[' + ($monitors[0] | ConvertTo-Json -Compress) + ']')
} else {
    $monitors | ConvertTo-Json -Compress
}
`
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", script).Output() // #nosec G204 -- fixed script, no user input
	if err != nil {
		return nil
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 || string(out) == "[]" {
		return nil
	}

	var raw []psMonitor
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil
	}
	monitors := make([]store.Monitor, 0, len(raw))
	for _, m := range raw {
		monitors = append(monitors, store.Monitor{
			Manufacturer: m.Manufacturer,
			Model:        m.Model,
			SerialNumber: m.Serial,
		})
	}
	return monitors
}
