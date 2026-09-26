package agentfacts

import (
	"testing"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// FuzzProxmoxConf: arbitrary configuration text never panics, and the guest
// stays within its bounds with clean strings.
func FuzzProxmoxConf(f *testing.F) {
	f.Add([]byte(qemuConf), true)
	f.Add([]byte(lxcConf), false)
	f.Add([]byte("net0: hwaddr=\nname:\n["), true)
	f.Fuzz(func(t *testing.T, data []byte, qemu bool) {
		typ := "lxc"
		if qemu {
			typ = "qemu"
		}
		g, ok := ParseProxmoxConf("100.conf", typ, data)
		if !ok {
			t.Fatal("valid file name rejected")
		}
		if len(g.MACs) > store.MaxGuestMACs || len(g.Name) > 256 || !utf8.ValidString(g.Name) {
			t.Fatalf("out of bounds: %+v", g)
		}
		for _, r := range g.Name {
			if r < 0x20 || r == 0x7f {
				t.Fatalf("control character in name %q", g.Name)
			}
		}
	})
}
