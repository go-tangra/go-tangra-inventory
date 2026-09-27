package agentfacts

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/siderolabs/go-smbios/smbios"
)

// Bounds of one SMBIOS table (a real table has a few hundred structures of
// a few strings each; the bounds keep a hostile table cheap).
const (
	maxSMBIOSStructures = 8192
	maxStructureStrings = 255 // string indexes are one byte
	maxTableBytes       = 1 << 20
)

// SplitSMBIOS splits a raw DMI structure table (as in
// /sys/firmware/dmi/tables/DMI) into structures. It stops at the
// end-of-table structure (type 127), at a malformed header or at the end of
// the data and never panics — unlike go-smbios' Decode, whose typed decoding
// panics on short structures (a malformed firmware table must not crash the
// agent).
func SplitSMBIOS(table []byte) []SMBIOSStructure {
	if len(table) > maxTableBytes {
		table = table[:maxTableBytes]
	}
	var out []SMBIOSStructure
	for i := 0; i+4 <= len(table) && len(out) < maxSMBIOSStructures; {
		typ, length := table[i], int(table[i+1])
		handle := binary.LittleEndian.Uint16(table[i+2 : i+4])
		if length < 4 || i+length > len(table) {
			break
		}
		s := SMBIOSStructure{Type: typ, Length: uint8(length), Handle: handle} // #nosec G115 -- a byte read above
		if length > 4 {
			s.Formatted = append([]byte(nil), table[i+4:i+length]...)
		}
		j := i + length
		// String set: NUL-terminated strings ended by an empty string; a
		// structure without strings ends with two NULs.
		if j+1 < len(table) && table[j] == 0 && table[j+1] == 0 {
			j += 2
		} else {
			for j < len(table) {
				end := bytes.IndexByte(table[j:], 0)
				if end < 0 {
					j = len(table)
					break
				}
				if end == 0 {
					j++
					break
				}
				if len(s.Strings) < maxStructureStrings {
					s.Strings = append(s.Strings, string(table[j:j+end]))
				}
				j += end + 1
			}
		}
		out = append(out, s)
		if typ == 127 {
			break
		}
		i = j
	}
	return out
}

// FromGoSMBIOS converts a table found and split by go-smbios (the Windows
// firmware table path) into raw structures; its typed, wrongly numbered
// enumerations are not used.
func FromGoSMBIOS(s *smbios.SMBIOS) (SMBIOSVersion, []SMBIOSStructure) {
	if s == nil {
		return SMBIOSVersion{}, nil
	}
	v := SMBIOSVersion{Major: s.Version.Major, Minor: s.Version.Minor}
	out := make([]SMBIOSStructure, 0, min(len(s.Structures), maxSMBIOSStructures))
	for _, st := range s.Structures {
		if st == nil || len(out) == maxSMBIOSStructures {
			continue
		}
		out = append(out, SMBIOSStructure{
			Type: st.Header.Type, Length: st.Header.Length, Handle: st.Header.Handle,
			Formatted: st.Formatted, Strings: st.Strings,
		})
	}
	return v, out
}

// OpenGoSMBIOS runs go-smbios' table discovery and decoding, converting a
// panic of its typed decoding on a malformed table into an error.
func OpenGoSMBIOS(open func() (*smbios.SMBIOS, error)) (s *smbios.SMBIOS, err error) {
	defer func() {
		if r := recover(); r != nil {
			s, err = nil, fmt.Errorf("agentfacts: malformed smbios table: %v", r)
		}
	}()
	return open()
}

// DecodeSMBIOSTable decodes a raw DMI structure table of the given version.
func DecodeSMBIOSTable(table []byte, v SMBIOSVersion) (SMBIOSHardware, error) {
	structs := SplitSMBIOS(table)
	if len(structs) == 0 {
		return SMBIOSHardware{}, fmt.Errorf("agentfacts: empty smbios table")
	}
	return DecodeSMBIOS(v, structs), nil
}

// ParseEntryPoint reads the SMBIOS version from a 32-bit ("_SM_") or 64-bit
// ("_SM3_") entry point structure.
func ParseEntryPoint(ep []byte) (SMBIOSVersion, bool) {
	switch {
	case len(ep) >= 9 && bytes.HasPrefix(ep, []byte("_SM3_")):
		return SMBIOSVersion{Major: int(ep[7]), Minor: int(ep[8])}, true
	case len(ep) >= 8 && bytes.HasPrefix(ep, []byte("_SM_")):
		return SMBIOSVersion{Major: int(ep[6]), Minor: int(ep[7])}, true
	}
	return SMBIOSVersion{}, false
}
