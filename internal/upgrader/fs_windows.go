//go:build windows

package upgrader

import (
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// adminOnlySDDL is the protected DACL of the staging directory, handed down
// to everything created in it: SYSTEM and Administrators only, nothing
// inherited from ProgramData (which lets Users create files).
const adminOnlySDDL = "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"

// restrictDir replaces the directory's DACL with adminOnlySDDL.
func restrictDir(path string, _ fs.FileMode) error {
	sd, err := windows.SecurityDescriptorFromString(adminOnlySDDL)
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// private: every allow entry of the DACL is for SYSTEM or Administrators (a
// NULL DACL grants everyone; unknown entry types count as not private).
func private(path string, _ os.FileInfo) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return false
	}
	for i := uint16(0); i < dacl.AceCount; i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, uint32(i), &ace); err != nil {
			return false
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE:
			continue // deny entries only restrict
		case windows.ACCESS_ALLOWED_ACE_TYPE:
			sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)) // #nosec G103 -- SID follows the ACE header in the same buffer
			if !sid.IsWellKnown(windows.WinLocalSystemSid) && !sid.IsWellKnown(windows.WinBuiltinAdministratorsSid) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// ownedByAdmin: owned by SYSTEM or the Administrators group.
func ownedByAdmin(path string, _ os.FileInfo) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return false
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return false
	}
	return owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid)
}

func freeBytes(path string) (uint64, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return 0, err
	}
	var free, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(p, &free, &total, &totalFree); err != nil {
		return 0, err
	}
	return free, nil
}
