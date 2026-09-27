package sender

import (
	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/invpb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// identityToProto maps a store.Identity to its proto message.
func identityToProto(id store.Identity) *invv1.Identity {
	return &invv1.Identity{
		HardwareUuid: id.HardwareUUID,
		MachineId:    id.MachineID,
		Hostname:     id.Hostname,
	}
}

// toProto maps a collected store.Inventory to the wire Inventory message.
func toProto(inv store.Inventory) *invv1.Inventory {
	pb := &invv1.Inventory{
		Identity:     identityToProto(inv.Identity),
		CollectedAt:  inv.CollectedAt.Unix(),
		AgentVersion: inv.AgentVersion,
		Os: &invv1.OSInfo{
			Name:      inv.OS.Name,
			Version:   inv.OS.Version,
			Build:     inv.OS.Build,
			Arch:      inv.OS.Arch,
			Kernel:    inv.OS.Kernel,
			UptimeSec: inv.OS.UptimeSec,
			Family:    inv.OS.Family,
		},
		Bios:         invpb.BIOSToPB(inv.BIOS),
		System:       invpb.SystemToPB(inv.System),
		Baseboard:    invpb.BaseboardToPB(inv.Baseboard),
		Chassis:      invpb.ChassisToPB(inv.Chassis),
		Memory:       invpb.MemoryToPB(inv.Memory),
		Ports:        inv.Ports,
		Slots:        inv.Slots,
		OemStrings:   inv.OEMStrings,
		BiosLanguage: inv.BIOSLanguage,
		Environment: &invv1.Environment{
			Domain:    inv.Environment.Domain,
			Workgroup: inv.Environment.Workgroup,
			Timezone:  inv.Environment.Timezone,
			Locale:    inv.Environment.Locale,
		},
	}
	if !inv.OS.InstallDate.IsZero() {
		pb.Os.InstallDate = inv.OS.InstallDate.Unix()
	}
	if !inv.OS.LastBoot.IsZero() {
		pb.Os.LastBoot = inv.OS.LastBoot.Unix()
	}

	pb.Processors = invpb.ProcessorsToPB(inv.Processors)
	for _, c := range inv.Cache {
		pb.Cache = append(pb.Cache, &invv1.CacheInfo{SocketDesignation: c.SocketDesignation})
	}
	for _, m := range inv.Monitors {
		pb.Monitors = append(pb.Monitors, &invv1.Monitor{
			Manufacturer: m.Manufacturer,
			Model:        m.Model,
			SerialNumber: m.SerialNumber,
		})
	}
	for _, p := range inv.Programs {
		pb.InstalledPrograms = append(pb.InstalledPrograms, &invv1.Program{
			Name:             p.Name,
			Version:          p.Version,
			Publisher:        p.Publisher,
			InstallDate:      p.InstallDate,
			InstallLocation:  p.InstallLocation,
			SizeBytes:        p.SizeBytes,
			AvailableVersion: p.AvailableVersion,
			SecurityUpdate:   p.SecurityUpdate,
		})
	}
	for _, s := range inv.Services {
		pb.Services = append(pb.Services, &invv1.Service{
			Name:        s.Name,
			DisplayName: s.DisplayName,
			State:       s.State,
			StartMode:   s.StartMode,
			Account:     s.Account,
		})
	}
	for _, u := range inv.Users {
		pu := &invv1.UserAccount{Name: u.Name, IsAdmin: u.IsAdmin}
		if !u.LastLogon.IsZero() {
			pu.LastLogon = u.LastLogon.Unix()
		}
		pb.Users = append(pb.Users, pu)
	}
	for _, p := range inv.Patches {
		pb.Patches = append(pb.Patches, &invv1.Patch{Id: p.ID, InstalledOn: p.InstalledOn})
	}
	pb.NetworkInterfaces = invpb.NetIfacesToPB(inv.Networks)
	pb.PrimaryIpv4 = inv.PrimaryIPv4
	pb.PrimaryIpv6 = inv.PrimaryIPv6
	pb.Virtualization = invpb.VirtualizationToPB(inv.Virtualization)
	pb.Bmc = invpb.BmcToPB(inv.Bmc)
	pb.HypervisorGuests = invpb.GuestsToPB(inv.HypervisorGuests)
	pb.UpdateState = invpb.UpdateStateToPB(inv.UpdateState)
	pb.Truncated = invpb.LimitsToPB(inv.Truncated)
	pb.Disks = invpb.DisksToPB(inv.Disks)
	pb.Filesystems = invpb.FilesystemsToPB(inv.Filesystems)
	pb.HardwareAvailability = invpb.AvailabilityToPB(inv.Availability)
	pb.HardwareSchema = inv.HardwareSchema
	return pb
}
