package ingest

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

func TestValidateExtended_BoundsTruncateAndCount(t *testing.T) {
	inv := store.Inventory{Truncated: store.CollectionLimits{Interfaces: 1, Packages: 2}}
	for i := 0; i < 257; i++ {
		inv.Networks = append(inv.Networks, store.NetIface{Name: fmt.Sprintf("eth%d", i), Type: store.IfaceEthernet})
	}
	for i := 0; i < 65; i++ {
		inv.Networks[0].Addresses = append(inv.Networks[0].Addresses,
			store.IfAddress{Address: fmt.Sprintf("10.0.%d.%d", i/250, i%250+1), PrefixLength: 24, Family: "ipv4"})
		inv.Networks[0].IPAddresses = append(inv.Networks[0].IPAddresses, fmt.Sprintf("10.0.%d.%d/24", i/250, i%250+1))
	}
	for i := 0; i < 1001; i++ {
		inv.HypervisorGuests = append(inv.HypervisorGuests, store.HypervisorGuest{ID: fmt.Sprint(100 + i), Kind: "vm", Platform: "proxmox"})
	}
	for i := 0; i < 5001; i++ {
		inv.Programs = append(inv.Programs, store.Program{Name: fmt.Sprintf("pkg%d", i), Version: "1", AvailableVersion: "2", SecurityUpdate: true})
	}
	inv.Programs = append(inv.Programs, store.Program{Name: "no-update", Version: "1"})
	inv.Bmc = &store.Bmc{Address: "10.9.9.9", PrefixLength: 24}
	for i := 0; i < 9; i++ {
		inv.Bmc.Ports = append(inv.Bmc.Ports, store.BmcPort{Channel: uint32(i + 1), MAC: fmt.Sprintf("00:11:22:33:44:%02x", i+1)})
	}

	validateExtended(&inv)

	if len(inv.Networks) != store.MaxInterfaces {
		t.Fatalf("interfaces = %d", len(inv.Networks))
	}
	if len(inv.Networks[0].Addresses) != store.MaxIfaceAddresses || len(inv.Networks[0].IPAddresses) != store.MaxIfaceAddresses {
		t.Fatalf("addresses = %d / %d", len(inv.Networks[0].Addresses), len(inv.Networks[0].IPAddresses))
	}
	if len(inv.HypervisorGuests) != store.MaxGuests {
		t.Fatalf("guests = %d", len(inv.HypervisorGuests))
	}
	pending := 0
	for _, p := range inv.Programs {
		if p.AvailableVersion != "" {
			pending++
		}
	}
	if pending != store.MaxPendingUpdates || len(inv.Programs) != 5002 {
		t.Fatalf("pending = %d programs = %d (installed programs must be kept)", pending, len(inv.Programs))
	}
	if last := inv.Programs[5000]; last.AvailableVersion != "" || last.SecurityUpdate {
		t.Fatalf("5001st pending update not cleared: %+v", last)
	}
	if len(inv.Bmc.Ports) != store.MaxBmcPorts {
		t.Fatalf("bmc ports = %d", len(inv.Bmc.Ports))
	}
	want := store.CollectionLimits{Interfaces: 1 + 1, Addresses: 1, Guests: 1, Packages: 2 + 1, BmcPorts: 1}
	if inv.Truncated != want {
		t.Fatalf("truncated = %+v, want %+v", inv.Truncated, want)
	}
}

func TestValidateExtended_DropsMalformed(t *testing.T) {
	inv := store.Inventory{
		PrimaryIPv4: "2001:db8::1", // wrong family
		PrimaryIPv6: "not-an-ip",
		Networks: []store.NetIface{
			{
				Name: "eth0", MAC: "AA-BB-CC-DD-EE-FF", Type: "ethernet", Gateway: "999.1.1.1", Master: "br\x00x", VLANID: 5000,
				IPAddresses: []string{"10.0.0.1/24", "garbage", "10.0.0.2/99"},
				Addresses: []store.IfAddress{
					{Address: "10.0.0.1", PrefixLength: 24, Family: "ipv6", Scope: "global"}, // family fixed
					{Address: "10.0.0.2", PrefixLength: 33},                                  // bad prefix
					{Address: "zz", PrefixLength: 8},                                         // bad address
					{Address: "fe80::1", PrefixLength: 64, Scope: "weird"},                   // scope cleared
				},
			},
			{Name: "eth1", MAC: "not-a-mac", Type: "warp-drive"},
			{Name: "", MAC: "00:11:22:33:44:55"},                   // no name → dropped
			{Name: "evil\nname"},                                   // control char → dropped
			{Name: strings.Repeat("x", 300)},                       // over-long → dropped
			{Name: "bad\xffutf8"},                                  // invalid UTF-8 → dropped
			{Name: "wg0", Type: "", Gateway: "fe80::1", VLANID: 7}, // old agent type kept ""
		},
		Virtualization: store.Virtualization{Role: "mainframe", Kind: "Evil Kind!", Source: strings.Repeat("s", 40)},
		Bmc: &store.Bmc{Address: "10.1.1.1", PrefixLength: 40, Gateway: "x", IPSource: "magic", VLANID: 9999,
			Ports: []store.BmcPort{{Channel: 1, MAC: "zz"}, {Channel: 2, MAC: "00:AA:BB:CC:DD:EE", Address: "10.1.1.1"}}},
		HypervisorGuests: []store.HypervisorGuest{
			{ID: "100", Name: "ok", Kind: "vm", Platform: "proxmox", MACs: []string{"BC:24:11:00:00:01", "junk"}},
			{ID: "abc", Name: "bad id"},
			{ID: "1234567890", Name: "too long id"},
			{ID: "101", Name: "evil\x1bname", Kind: "vm"},
			{ID: "102", Name: strings.Repeat("n", 300), Kind: "hologram", Platform: "vmware"},
		},
		UpdateState: store.UpdateState{PackageManager: "brew", Status: "sort-of", RebootRequired: "maybe", AutomaticUpdates: "yes"},
		Programs: []store.Program{
			{Name: "a", AvailableVersion: "1\x002", SecurityUpdate: true},
			{Name: "b", AvailableVersion: strings.Repeat("9", 200)},
		},
	}
	validateExtended(&inv)

	if inv.PrimaryIPv4 != "" || inv.PrimaryIPv6 != "" {
		t.Fatalf("primary = %q %q", inv.PrimaryIPv4, inv.PrimaryIPv6)
	}
	if len(inv.Networks) != 3 {
		t.Fatalf("networks = %+v", inv.Networks)
	}
	e0 := inv.Networks[0]
	if e0.MAC != "aa:bb:cc:dd:ee:ff" || e0.Gateway != "" || e0.Master != "" || e0.VLANID != 0 {
		t.Fatalf("eth0 = %+v", e0)
	}
	if len(e0.IPAddresses) != 1 || e0.IPAddresses[0] != "10.0.0.1/24" {
		t.Fatalf("ip_addresses = %v", e0.IPAddresses)
	}
	if len(e0.Addresses) != 2 || e0.Addresses[0].Family != "ipv4" || e0.Addresses[1].Scope != "" || e0.Addresses[1].Family != "ipv6" {
		t.Fatalf("addresses = %+v", e0.Addresses)
	}
	if e1 := inv.Networks[1]; e1.MAC != "" || e1.Type != store.IfaceOther {
		t.Fatalf("eth1 = %+v", e1)
	}
	if wg := inv.Networks[2]; wg.Name != "wg0" || wg.Type != "" || wg.Gateway != "fe80::1" || wg.VLANID != 7 {
		t.Fatalf("wg0 = %+v", wg)
	}
	if inv.Virtualization != (store.Virtualization{Role: store.RoleUnknown}) {
		t.Fatalf("virt = %+v", inv.Virtualization)
	}
	b := inv.Bmc
	if b.PrefixLength != 0 || b.Gateway != "" || b.IPSource != "" || b.VLANID != 0 || len(b.Ports) != 1 || b.Ports[0].MAC != "00:aa:bb:cc:dd:ee" {
		t.Fatalf("bmc = %+v", b)
	}
	if len(inv.HypervisorGuests) != 2 {
		t.Fatalf("guests = %+v", inv.HypervisorGuests)
	}
	g0, g1 := inv.HypervisorGuests[0], inv.HypervisorGuests[1]
	if len(g0.MACs) != 1 || g0.MACs[0] != "bc:24:11:00:00:01" {
		t.Fatalf("guest macs = %v", g0.MACs)
	}
	if len(g1.Name) != 256 || g1.Kind != "" || g1.Platform != "" {
		t.Fatalf("guest 102 = %+v", g1)
	}
	u := inv.UpdateState
	if u.PackageManager != "" || u.Status != store.UpdateUnknown || u.RebootRequired != store.TriUnknown || u.AutomaticUpdates != store.TriUnknown {
		t.Fatalf("update = %+v", u)
	}
	for _, p := range inv.Programs {
		if p.AvailableVersion != "" || p.SecurityUpdate {
			t.Fatalf("program %q kept a malformed available version", p.Name)
		}
	}
}

func TestValidateExtended_EmptyBmcDropped(t *testing.T) {
	inv := store.Inventory{Bmc: &store.Bmc{Address: "nope", Ports: []store.BmcPort{{Channel: 1, MAC: "bad"}}}}
	validateExtended(&inv)
	if inv.Bmc != nil {
		t.Fatalf("bmc without address and ports must be dropped: %+v", inv.Bmc)
	}
	var nilInv store.Inventory
	validateExtended(&nilInv) // zero inventory (old agent) is untouched
	if nilInv.Virtualization != (store.Virtualization{}) || nilInv.UpdateState != (store.UpdateState{}) {
		t.Fatalf("old agent inventory changed: %+v", nilInv)
	}
}

func TestValidateExtended_SaturatingCounters(t *testing.T) {
	inv := store.Inventory{Truncated: store.CollectionLimits{Interfaces: ^uint32(0)}}
	for i := 0; i < 300; i++ {
		inv.Networks = append(inv.Networks, store.NetIface{Name: fmt.Sprint("e", i)})
	}
	validateExtended(&inv)
	if inv.Truncated.Interfaces != ^uint32(0) {
		t.Fatalf("counter overflowed: %d", inv.Truncated.Interfaces)
	}
}

// SubmitInventory applies validateExtended before storing: an oversized list is
// bounded and counted, and the snapshot is still stored.
func TestSubmit_ValidatesExtendedAndStores(t *testing.T) {
	h := newHarness(t, 0)
	agentID, cred := h.mintAndEnroll(t, "tenant-v", "host-v")
	ctx := h.authedCtx(t, agentID, cred)
	pb := sampleInventory("host-v")
	for i := 0; i < 300; i++ {
		pb.NetworkInterfaces = append(pb.NetworkInterfaces, &inventoryv1.NetworkInterface{Name: fmt.Sprint("veth", i)})
	}
	resp, err := h.srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{Inventory: pb})
	if err != nil {
		t.Fatalf("SubmitInventory: %v", err)
	}
	snap, err := h.mem.GetSnapshot(context.Background(), "tenant-v", resp.GetSnapshotId())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Payload.Networks) != store.MaxInterfaces || snap.Payload.Truncated.Interfaces != 301-store.MaxInterfaces {
		t.Fatalf("stored %d interfaces, truncated %d", len(snap.Payload.Networks), snap.Payload.Truncated.Interfaces)
	}
}

// Negative: the size ceiling still rejects before any validation or write.
func TestSubmit_OversizedStillInvalidArgument(t *testing.T) {
	h := newHarness(t, 512)
	agentID, cred := h.mintAndEnroll(t, "tenant-o", "host-o")
	ctx := h.authedCtx(t, agentID, cred)
	pb := sampleInventory("host-o")
	for i := 0; i < 50; i++ {
		pb.HypervisorGuests = append(pb.HypervisorGuests, &inventoryv1.HypervisorGuest{Id: fmt.Sprint(i), Name: strings.Repeat("g", 50)})
	}
	_, err := h.srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{Inventory: pb})
	requireCode(t, err, codes.InvalidArgument)
	if hosts, _ := h.mem.ListHosts(context.Background(), "tenant-o", store.HostFilter{}); len(hosts) != 0 {
		t.Fatalf("oversized payload wrote %d hosts", len(hosts))
	}
}

// Negative: a payload carrying another tenant-looking value never moves the
// write out of the verified agent's tenant.
func TestSubmit_TenantAlwaysFromCredential(t *testing.T) {
	h := newHarness(t, 0)
	const other = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c66"
	agentID, cred := h.mintAndEnroll(t, "tenant-own", "host-t")
	ctx := h.authedCtx(t, agentID, cred)
	pb := sampleInventory("host-t")
	pb.OemStrings = []string{"tenant_id=" + other}
	pb.Environment = &inventoryv1.Environment{Domain: other}
	pb.HypervisorGuests = []*inventoryv1.HypervisorGuest{{Id: "100", Name: other}}
	resp, err := h.srv.SubmitInventory(ctx, &inventoryv1.SubmitRequest{Inventory: pb})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.mem.GetSnapshot(context.Background(), other, resp.GetSnapshotId()); err == nil {
		t.Fatal("snapshot visible in the tenant named by the payload")
	}
	if _, err := h.mem.GetSnapshot(context.Background(), "tenant-own", resp.GetSnapshotId()); err != nil {
		t.Fatalf("snapshot not in the agent's tenant: %v", err)
	}
}
