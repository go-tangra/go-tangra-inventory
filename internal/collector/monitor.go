package collector

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
)

type psMonitorResult struct {
	Manufacturer string `json:"Manufacturer"`
	Model        string `json:"Model"`
	Serial       string `json:"Serial"`
}

// collectMonitorInfo uses PowerShell to query WmiMonitorID from the root\wmi
// namespace. WmiMonitorID stores manufacturer, model, and serial as uint16
// arrays which PowerShell decodes natively into strings.
func collectMonitorInfo() ([]MonitorInfo, error) {
	script := `
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
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script)
	output, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("powershell WmiMonitorID query failed: %w", err)
	}

	output = bytes.TrimSpace(output)
	if len(output) == 0 || string(output) == "[]" {
		return nil, nil
	}

	var monitors []psMonitorResult
	if err := json.Unmarshal(output, &monitors); err != nil {
		return nil, fmt.Errorf("parsing monitor JSON: %w (raw: %s)", err, string(output))
	}

	result := make([]MonitorInfo, len(monitors))
	for i, m := range monitors {
		result[i] = MonitorInfo{
			Manufacturer: m.Manufacturer,
			Model:        m.Model,
			SerialNumber: m.Serial,
		}
	}
	return result, nil
}
