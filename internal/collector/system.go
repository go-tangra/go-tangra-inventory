package collector

import "github.com/yusufpapurcu/wmi"

type win32ComputerSystem struct {
	Manufacturer string
	Model        string
}

type win32BIOS struct {
	SerialNumber string
}

// collectSystemInfo queries Win32_ComputerSystem and Win32_BIOS for
// manufacturer, model, and chassis serial number.
func collectSystemInfo() (SystemInfo, error) {
	var cs []win32ComputerSystem
	if err := wmi.Query("SELECT Manufacturer, Model FROM Win32_ComputerSystem", &cs); err != nil {
		return SystemInfo{}, err
	}

	var bios []win32BIOS
	if err := wmi.Query("SELECT SerialNumber FROM Win32_BIOS", &bios); err != nil {
		return SystemInfo{}, err
	}

	info := SystemInfo{}
	if len(cs) > 0 {
		info.Manufacturer = cs[0].Manufacturer
		info.Model = cs[0].Model
	}
	if len(bios) > 0 {
		info.SerialNumber = bios[0].SerialNumber
	}
	return info, nil
}
