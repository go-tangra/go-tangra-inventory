package hostreport

import (
	"testing"

	"google.golang.org/protobuf/proto"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/invpb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// FuzzHostReport decodes arbitrary Inventory bytes into a snapshot payload and
// projects it: the projection must never panic, stay within the pending
// update bound, and yield the same digest for the same input.
func FuzzHostReport(f *testing.F) {
	seed, _ := proto.Marshal(&invv1.Inventory{
		Os:                &invv1.OSInfo{Name: "Ubuntu", Family: "linux"},
		InstalledPrograms: []*invv1.Program{{Name: "a", Version: "1", AvailableVersion: "2", SecurityUpdate: true}},
		NetworkInterfaces: []*invv1.NetworkInterface{{Name: "eth0", Addresses: []*invv1.InterfaceAddress{{Address: "10.0.0.1", PrefixLength: 8}}}},
		Bmc:               &invv1.Bmc{Address: "10.0.0.2"},
	})
	f.Add(seed, "active")
	f.Add([]byte{}, "retired")
	f.Fuzz(func(t *testing.T, data []byte, status string) {
		pb := &invv1.Inventory{}
		if proto.Unmarshal(data, pb) != nil {
			return
		}
		inv := store.Inventory{
			OS:               store.OSInfo{Name: pb.GetOs().GetName(), Family: pb.GetOs().GetFamily()},
			Networks:         invpb.NetIfacesFromPB(pb.GetNetworkInterfaces()),
			PrimaryIPv4:      pb.GetPrimaryIpv4(),
			PrimaryIPv6:      pb.GetPrimaryIpv6(),
			Virtualization:   invpb.VirtualizationFromPB(pb.GetVirtualization()),
			Bmc:              invpb.BmcFromPB(pb.GetBmc()),
			HypervisorGuests: invpb.GuestsFromPB(pb.GetHypervisorGuests()),
			UpdateState:      invpb.UpdateStateFromPB(pb.GetUpdateState()),
			Truncated:        invpb.LimitsFromPB(pb.GetTruncated()),
		}
		for _, p := range pb.GetInstalledPrograms() {
			inv.Programs = append(inv.Programs, store.Program{Name: p.GetName(), Version: p.GetVersion(),
				AvailableVersion: p.GetAvailableVersion(), SecurityUpdate: p.GetSecurityUpdate()})
		}
		h := store.Host{ID: "h", TenantID: "t", Status: status}
		s := store.Snapshot{ID: "s", Payload: inv}
		r1, r2 := Project(h, s), Project(h, s)
		if len(r1.GetPendingUpdates()) > store.MaxPendingUpdates {
			t.Fatalf("pending = %d", len(r1.GetPendingUpdates()))
		}
		if r1.GetReportDigest() != r2.GetReportDigest() || len(r1.GetReportDigest()) != 64 {
			t.Fatalf("digest not deterministic: %q vs %q", r1.GetReportDigest(), r2.GetReportDigest())
		}
	})
}
