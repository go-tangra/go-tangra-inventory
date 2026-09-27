package agentfacts

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/siderolabs/go-smbios/smbios"
)

// TestSplitMatchesGoSMBIOS: for well-formed tables the agent's splitter and
// go-smbios yield the same structures.
func TestSplitMatchesGoSMBIOS(t *testing.T) {
	for _, f := range fixtures {
		table := encode(f.build())
		s, err := smbiosDecode(table, f.version)
		if err != nil {
			t.Fatal(err)
		}
		v, want := FromGoSMBIOS(s)
		if v != f.version {
			t.Fatalf("%s: version %+v", f.name, v)
		}
		got := SplitSMBIOS(table)
		for i := range want { // go-smbios keeps nil for empty sets
			if len(want[i].Formatted) == 0 {
				want[i].Formatted = nil
			}
			if len(want[i].Strings) == 0 {
				want[i].Strings = nil
			}
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: split differs from go-smbios\n got %+v\nwant %+v", f.name, got, want)
		}
	}
	if v, s := FromGoSMBIOS(nil); v != (SMBIOSVersion{}) || s != nil {
		t.Fatal("nil table")
	}
	withNil := &smbios.SMBIOS{Structures: nil}
	if _, s := FromGoSMBIOS(withNil); len(s) != 0 {
		t.Fatal("empty table")
	}
}

func TestSplitMalformed(t *testing.T) {
	cases := map[string][]byte{
		"empty":            nil,
		"short header":     {1, 4},
		"length below 4":   {1, 2, 0, 0, 0, 0},
		"length past end":  {1, 0x40, 0, 0, 1, 2},
		"unterminated str": {1, 5, 0, 0, 1, 'a', 'b'},
	}
	for name, b := range cases {
		got := SplitSMBIOS(b)
		for _, s := range got {
			if len(s.Formatted) != int(s.Length)-4 {
				t.Errorf("%s: %+v", name, s)
			}
		}
		if name != "unterminated str" && len(got) != 0 {
			t.Errorf("%s: %+v", name, got)
		}
	}
	if s := SplitSMBIOS([]byte{1, 5, 0, 0, 1, 'a', 'b'}); len(s) != 1 || !reflect.DeepEqual(s[0].Strings, []string(nil)) {
		t.Errorf("unterminated string must not be kept: %+v", s)
	}
	// A header followed by a single trailing byte ends the table cleanly.
	if s := SplitSMBIOS([]byte{2, 4, 1, 0, 0}); len(s) != 1 || s[0].Type != 2 || s[0].Strings != nil {
		t.Errorf("trailing byte: %+v", s)
	}
	// Strings are bounded per structure.
	b := []byte{11, 5, 0, 0, 255}
	for i := 0; i < 300; i++ {
		b = append(b, 'x', 0)
	}
	b = append(b, 0)
	if s := SplitSMBIOS(b); len(s) != 1 || len(s[0].Strings) != maxStructureStrings {
		t.Errorf("strings = %d", len(s[0].Strings))
	}
	// Structure count and table size are bounded.
	many := make([]byte, 0, (maxSMBIOSStructures+10)*6)
	for i := 0; i < maxSMBIOSStructures+10; i++ {
		many = append(many, 200, 4, 0, 0, 0, 0)
	}
	if n := len(SplitSMBIOS(many)); n != maxSMBIOSStructures {
		t.Errorf("structures = %d", n)
	}
	huge := make([]byte, maxTableBytes+100)
	for i := 0; i+6 <= len(huge); i += 6 {
		copy(huge[i:], []byte{200, 4, 0, 0, 0, 0})
	}
	if n := len(SplitSMBIOS(huge)); n > maxSMBIOSStructures {
		t.Errorf("huge table = %d", n)
	}
	if _, err := DecodeSMBIOSTable(nil, v33); err == nil {
		t.Error("empty table must be an error")
	}
}

func TestOpenGoSMBIOS(t *testing.T) {
	boom := errors.New("boom")
	if _, err := OpenGoSMBIOS(func() (*smbios.SMBIOS, error) { return nil, boom }); !errors.Is(err, boom) {
		t.Fatalf("error passthrough = %v", err)
	}
	if _, err := OpenGoSMBIOS(func() (*smbios.SMBIOS, error) { panic("short structure") }); err == nil {
		t.Fatal("panic must become an error")
	}
	// The real go-smbios decoder panics on a type 1 structure shorter than
	// the UUID; the wrapper turns it into an error.
	table := []byte{1, 5, 0, 0, 0, 0, 0, 127, 4, 0, 0, 0, 0}
	if _, err := OpenGoSMBIOS(func() (*smbios.SMBIOS, error) { return smbiosDecode(table, v33) }); err == nil {
		t.Fatal("go-smbios panic not recovered")
	}
	if hw, err := DecodeSMBIOSTable(table, v33); err != nil || hw.System.UUID != "" {
		t.Fatalf("agent decoder on the same table = %+v %v", hw.System, err)
	}
}

func TestParseEntryPoint(t *testing.T) {
	for _, v := range []SMBIOSVersion{{3, 3}, {2, 8}} {
		if got, ok := ParseEntryPoint(entryPoint(v)); !ok || got != v {
			t.Errorf("%+v -> %+v %v", v, got, ok)
		}
	}
	for _, bad := range [][]byte{nil, []byte("_SM3_"), []byte("_SM_"), []byte("garbage-entry-point")} {
		if _, ok := ParseEntryPoint(bad); ok {
			t.Errorf("accepted %q", bad)
		}
	}
	ep, err := os.ReadFile(filepath.Join(smbiosDir, "node-1.ep"))
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := ParseEntryPoint(ep); !ok || v != (SMBIOSVersion{3, 3}) {
		t.Fatalf("node-1 entry point = %+v", v)
	}
}
