// Package invpb maps the feature-020 host report parts of the inventory
// payload (interfaces with addresses, virtualization, BMC, hypervisor guests,
// update state, truncation counters) between the domain store types and the
// inventory.v1 wire messages. The agent sender, the ingest edge, the mesh API
// and the host report projection share these mappers so the three directions
// cannot drift. The mappers copy values only; validation and bounds live in
// internal/ingest (edge) and internal/agentfacts (agent).
package invpb

import (
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// NetIfaceToPB maps one interface to the wire message.
func NetIfaceToPB(n store.NetIface) *invv1.NetworkInterface {
	out := &invv1.NetworkInterface{
		Name: n.Name, Mac: n.MAC, IpAddresses: n.IPAddresses, Subnet: n.Subnet, Gateway: n.Gateway,
		Dns: n.DNS, Dhcp: n.DHCP, SpeedBps: n.SpeedBps, Type: n.Type, Up: n.Up,
		DefaultRoute: n.DefaultRoute, Master: n.Master, VlanId: n.VLANID,
	}
	for _, a := range n.Addresses {
		out.Addresses = append(out.Addresses, &invv1.InterfaceAddress{
			Address: a.Address, PrefixLength: a.PrefixLength, Family: a.Family, Dhcp: a.DHCP,
			Temporary: a.Temporary, Deprecated: a.Deprecated, Scope: a.Scope,
		})
	}
	return out
}

// NetIfacesToPB maps a list of interfaces.
func NetIfacesToPB(in []store.NetIface) []*invv1.NetworkInterface {
	if len(in) == 0 {
		return nil
	}
	out := make([]*invv1.NetworkInterface, 0, len(in))
	for _, n := range in {
		out = append(out, NetIfaceToPB(n))
	}
	return out
}

// NetIfacesFromPB maps wire interfaces to the domain type.
func NetIfacesFromPB(in []*invv1.NetworkInterface) []store.NetIface {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.NetIface, 0, len(in))
	for _, n := range in {
		ni := store.NetIface{
			Name: n.GetName(), MAC: n.GetMac(), IPAddresses: n.GetIpAddresses(), Subnet: n.GetSubnet(),
			Gateway: n.GetGateway(), DNS: n.GetDns(), DHCP: n.GetDhcp(), SpeedBps: n.GetSpeedBps(),
			Type: n.GetType(), Up: n.GetUp(), DefaultRoute: n.GetDefaultRoute(), Master: n.GetMaster(),
			VLANID: n.GetVlanId(),
		}
		for _, a := range n.GetAddresses() {
			ni.Addresses = append(ni.Addresses, store.IfAddress{
				Address: a.GetAddress(), PrefixLength: a.GetPrefixLength(), Family: a.GetFamily(), DHCP: a.GetDhcp(),
				Temporary: a.GetTemporary(), Deprecated: a.GetDeprecated(), Scope: a.GetScope(),
			})
		}
		out = append(out, ni)
	}
	return out
}

// VirtualizationToPB maps the virtualization role; the zero value maps to nil.
func VirtualizationToPB(v store.Virtualization) *invv1.Virtualization {
	if v == (store.Virtualization{}) {
		return nil
	}
	return &invv1.Virtualization{Role: v.Role, Kind: v.Kind, Source: v.Source}
}

// VirtualizationFromPB maps the wire virtualization role.
func VirtualizationFromPB(v *invv1.Virtualization) store.Virtualization {
	return store.Virtualization{Role: v.GetRole(), Kind: v.GetKind(), Source: v.GetSource()}
}

// BmcToPB maps the BMC; nil stays nil.
func BmcToPB(b *store.Bmc) *invv1.Bmc {
	if b == nil {
		return nil
	}
	out := &invv1.Bmc{Address: b.Address, PrefixLength: b.PrefixLength, Gateway: b.Gateway, IpSource: b.IPSource, VlanId: b.VLANID}
	for _, p := range b.Ports {
		out.Ports = append(out.Ports, &invv1.BmcPort{Channel: p.Channel, Mac: p.MAC, Address: p.Address})
	}
	return out
}

// BmcFromPB maps the wire BMC; nil stays nil.
func BmcFromPB(b *invv1.Bmc) *store.Bmc {
	if b == nil {
		return nil
	}
	out := &store.Bmc{Address: b.GetAddress(), PrefixLength: b.GetPrefixLength(), Gateway: b.GetGateway(),
		IPSource: b.GetIpSource(), VLANID: b.GetVlanId()}
	for _, p := range b.GetPorts() {
		out.Ports = append(out.Ports, store.BmcPort{Channel: p.GetChannel(), MAC: p.GetMac(), Address: p.GetAddress()})
	}
	return out
}

// GuestsToPB maps hypervisor guests.
func GuestsToPB(in []store.HypervisorGuest) []*invv1.HypervisorGuest {
	if len(in) == 0 {
		return nil
	}
	out := make([]*invv1.HypervisorGuest, 0, len(in))
	for _, g := range in {
		out = append(out, &invv1.HypervisorGuest{Id: g.ID, Name: g.Name, Kind: g.Kind, Platform: g.Platform, Macs: g.MACs})
	}
	return out
}

// GuestsFromPB maps wire hypervisor guests.
func GuestsFromPB(in []*invv1.HypervisorGuest) []store.HypervisorGuest {
	if len(in) == 0 {
		return nil
	}
	out := make([]store.HypervisorGuest, 0, len(in))
	for _, g := range in {
		out = append(out, store.HypervisorGuest{ID: g.GetId(), Name: g.GetName(), Kind: g.GetKind(),
			Platform: g.GetPlatform(), MACs: g.GetMacs()})
	}
	return out
}

// UpdateStateToPB maps the update state; the zero value maps to nil.
func UpdateStateToPB(u store.UpdateState) *invv1.UpdateState {
	if u == (store.UpdateState{}) {
		return nil
	}
	return &invv1.UpdateState{
		PackageManager: u.PackageManager, Status: u.Status, RebootRequired: u.RebootRequired,
		AutomaticUpdates: u.AutomaticUpdates, SecurityClassified: u.SecurityClassified,
		CheckedAt: unix(u.CheckedAt), PendingCount: u.PendingCount, SecurityCount: u.SecurityCount,
	}
}

// UpdateStateFromPB maps the wire update state.
func UpdateStateFromPB(u *invv1.UpdateState) store.UpdateState {
	out := store.UpdateState{
		PackageManager: u.GetPackageManager(), Status: u.GetStatus(), RebootRequired: u.GetRebootRequired(),
		AutomaticUpdates: u.GetAutomaticUpdates(), SecurityClassified: u.GetSecurityClassified(),
		PendingCount: u.GetPendingCount(), SecurityCount: u.GetSecurityCount(),
	}
	if ts := u.GetCheckedAt(); ts != 0 {
		out.CheckedAt = time.Unix(ts, 0).UTC()
	}
	return out
}

// LimitsToPB maps truncation counters; all-zero maps to nil.
func LimitsToPB(l store.CollectionLimits) *invv1.CollectionLimits {
	if l == (store.CollectionLimits{}) {
		return nil
	}
	return &invv1.CollectionLimits{Interfaces: l.Interfaces, Addresses: l.Addresses, Guests: l.Guests,
		Packages: l.Packages, BmcPorts: l.BmcPorts}
}

// LimitsFromPB maps wire truncation counters.
func LimitsFromPB(l *invv1.CollectionLimits) store.CollectionLimits {
	return store.CollectionLimits{Interfaces: l.GetInterfaces(), Addresses: l.GetAddresses(), Guests: l.GetGuests(),
		Packages: l.GetPackages(), BmcPorts: l.GetBmcPorts()}
}

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
