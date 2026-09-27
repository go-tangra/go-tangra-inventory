//go:build windows

package collector

import (
	"context"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentfacts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Fixed PowerShell queries (no interpolation of any input).
const (
	psPhysicalDisks = "Get-PhysicalDisk | Select-Object DeviceId, FriendlyName, SerialNumber, Size, MediaType, BusType | ConvertTo-Json -Compress"
	psDiskDrives    = "Get-CimInstance Win32_DiskDrive | Select-Object Index, Model, SerialNumber, Size, InterfaceType, MediaType | ConvertTo-Json -Compress"
	psPartitions    = "Get-Partition | Select-Object DiskNumber, @{n='DriveLetter';e={[string]$_.DriveLetter}} | ConvertTo-Json -Compress"
)

func powershell(script string) ([]byte, error) {
	out, err := runCmd("powershell.exe", "-NoProfile", "-NonInteractive", "-Command", script)
	return []byte(out), err
}

// physicalDisks queries the Storage module (Win32_DiskDrive where it is
// missing) and maps drive letters to disks for the filesystems.
func physicalDisks(_ context.Context) (agentfacts.BlockDevices, agentfacts.DiskResolver) {
	res := agentfacts.BlockDevices{Availability: store.AvailOK}
	var disks []store.Disk
	out, err := powershell(psPhysicalDisks)
	if err == nil {
		disks, err = agentfacts.ParseWindowsPhysicalDisks(out)
	}
	if err != nil {
		out, err = powershell(psDiskDrives)
		if err == nil {
			disks, err = agentfacts.ParseWin32DiskDrives(out)
		}
		res.Availability = store.AvailPartial
	}
	if err != nil {
		return agentfacts.BlockDevices{Availability: store.AvailUnavailable}, nil
	}
	for _, d := range disks {
		if len(res.Disks) == store.MaxDisks {
			res.Truncated++
			continue
		}
		res.Disks = append(res.Disks, d)
	}
	letters := map[string]string{}
	if out, perr := powershell(psPartitions); perr == nil {
		if m, merr := agentfacts.ParseWindowsDriveLetters(out); merr == nil {
			letters = m
		}
	}
	return res, agentfacts.WindowsDriveResolver(letters)
}
