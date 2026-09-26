package agentfacts

import (
	"fmt"
	"strings"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

const qemuConf = `boot: order=scsi0;net0
cores: 2
name: web-01
net0: virtio=BC:24:11:AA:BB:CC,bridge=vmbr0,firewall=1
net1: e1000=bc:24:11:aa:bb:cd,bridge=vmbr1
net2: vmxnet3=BC:24:11:AA:BB:CE,bridge=vmbr1,tag=20
net3: rtl8139=BC:24:11:AA:BB:CF
net4: e1000e=BC:24:11:AA:BB:D0
net5: virtio=zz:zz,bridge=vmbr0
description: net9: virtio=BC:24:11:00:00:99
scsi0: local-lvm:vm-100-disk-0,size=32G

[snap1]
name: old-name
net6: virtio=BC:24:11:FF:FF:FF
`

const lxcConf = `arch: amd64
hostname: ct-db
net0: name=eth0,bridge=vmbr0,hwaddr=BC:24:11:11:22:33,ip=dhcp,type=veth
net1: name=eth1,bridge=vmbr1,hwaddr=BC:24:11:11:22:34,type=veth
`

func TestParseProxmoxConf(t *testing.T) {
	g, ok := ParseProxmoxConf("100.conf", "qemu", []byte(qemuConf))
	if !ok || g.ID != "100" || g.Name != "web-01" || g.Kind != "vm" || g.Platform != "proxmox" {
		t.Fatalf("qemu = %+v %v", g, ok)
	}
	want := []string{"bc:24:11:aa:bb:cc", "bc:24:11:aa:bb:cd", "bc:24:11:aa:bb:ce", "bc:24:11:aa:bb:cf", "bc:24:11:aa:bb:d0"}
	if strings.Join(g.MACs, ",") != strings.Join(want, ",") {
		t.Fatalf("macs = %v", g.MACs)
	}
	c, ok := ParseProxmoxConf("101.conf", "lxc", []byte(lxcConf))
	if !ok || c.Name != "ct-db" || c.Kind != "container" || len(c.MACs) != 2 || c.MACs[0] != "bc:24:11:11:22:33" {
		t.Fatalf("lxc = %+v", c)
	}
	// Guests without MACs are kept.
	n, ok := ParseProxmoxConf("102.conf", "qemu", []byte("name: nomac\n"))
	if !ok || n.Name != "nomac" || len(n.MACs) != 0 {
		t.Fatalf("no mac = %+v", n)
	}
}

func TestParseProxmoxConfRejects(t *testing.T) {
	for _, name := range []string{"abc.conf", "1234567890.conf", "100.conf.bak", "../100.conf", "100", ".conf", "-1.conf"} {
		if _, ok := ParseProxmoxConf(name, "qemu", []byte("name: x\n")); ok {
			t.Errorf("file name %q accepted", name)
		}
	}
	if _, ok := ParseProxmoxConf("100.conf", "openvz", []byte("name: x\n")); ok {
		t.Error("unknown guest type accepted")
	}
	// Name with control characters or over 256 bytes is dropped/clipped.
	g, _ := ParseProxmoxConf("100.conf", "qemu", []byte("name: bad\x1bname\n"))
	if g.Name != "" {
		t.Fatalf("control char name = %q", g.Name)
	}
	g, _ = ParseProxmoxConf("100.conf", "qemu", []byte("name: "+strings.Repeat("x", 300)+"\n"))
	if len(g.Name) != 256 {
		t.Fatalf("long name = %d", len(g.Name))
	}
}

func TestParseProxmoxConfBounds(t *testing.T) {
	var b strings.Builder
	b.WriteString("name: many\n")
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&b, "net%d: virtio=BC:24:11:00:00:%02X,bridge=vmbr0\n", i, i)
	}
	g, _ := ParseProxmoxConf("200.conf", "qemu", []byte(b.String()))
	if len(g.MACs) != store.MaxGuestMACs {
		t.Fatalf("macs = %d", len(g.MACs))
	}
	// Only the first 64 KiB are parsed.
	big := "name: big\n" + strings.Repeat("#"+strings.Repeat("x", 1000)+"\n", 70) + "net0: virtio=BC:24:11:00:00:01\n"
	g, _ = ParseProxmoxConf("201.conf", "qemu", []byte(big))
	if len(g.MACs) != 0 || g.Name != "big" {
		t.Fatalf("64 KiB bound: %+v", g)
	}
}

func TestProxmoxGuestsBound(t *testing.T) {
	var files []ProxmoxFile
	for i := 0; i < 1005; i++ {
		files = append(files, ProxmoxFile{Name: fmt.Sprint(100+i, ".conf"), Type: "qemu", Data: []byte("name: g\n")})
	}
	files = append(files, ProxmoxFile{Name: "bad.conf", Type: "qemu"})
	guests, dropped := ProxmoxGuests(files)
	if len(guests) != store.MaxGuests || dropped != 5 {
		t.Fatalf("guests = %d dropped = %d", len(guests), dropped)
	}
}
