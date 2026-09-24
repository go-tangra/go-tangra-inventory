package sender

import (
	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
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
		},
		Bios: &invv1.BIOSInfo{
			Vendor:      inv.BIOS.Vendor,
			Version:     inv.BIOS.Version,
			ReleaseDate: inv.BIOS.ReleaseDate,
		},
		System: &invv1.SystemInfo{
			Manufacturer: inv.System.Manufacturer,
			ProductName:  inv.System.ProductName,
			Version:      inv.System.Version,
			SerialNumber: inv.System.SerialNumber,
			Uuid:         inv.System.UUID,
			WakeUpType:   inv.System.WakeUpType,
			SkuNumber:    inv.System.SKUNumber,
			Family:       inv.System.Family,
		},
		Baseboard: &invv1.BaseboardInfo{
			Manufacturer:      inv.Baseboard.Manufacturer,
			Product:           inv.Baseboard.Product,
			Version:           inv.Baseboard.Version,
			SerialNumber:      inv.Baseboard.SerialNumber,
			AssetTag:          inv.Baseboard.AssetTag,
			LocationInChassis: inv.Baseboard.LocationInChassis,
			BoardType:         inv.Baseboard.BoardType,
		},
		Chassis: &invv1.ChassisInfo{
			Manufacturer: inv.Chassis.Manufacturer,
			Version:      inv.Chassis.Version,
			SerialNumber: inv.Chassis.SerialNumber,
			AssetTag:     inv.Chassis.AssetTag,
			SkuNumber:    inv.Chassis.SKUNumber,
			Type:         inv.Chassis.Type,
		},
		Memory:       memoryToProto(inv.Memory),
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

	for _, p := range inv.Processors {
		pb.Processors = append(pb.Processors, &invv1.Processor{
			SocketDesignation: p.SocketDesignation,
			Manufacturer:      p.Manufacturer,
			Version:           p.Version,
			MaxSpeedMhz:       p.MaxSpeedMHz,
			CurrentSpeedMhz:   p.CurrentSpeedMHz,
			CoreCount:         p.CoreCount,
			CoreEnabled:       p.CoreEnabled,
			ThreadCount:       p.ThreadCount,
			PartNumber:        p.PartNumber,
			SerialNumber:      p.SerialNumber,
			SocketPopulated:   p.SocketPopulated,
		})
	}
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
			Name:            p.Name,
			Version:         p.Version,
			Publisher:       p.Publisher,
			InstallDate:     p.InstallDate,
			InstallLocation: p.InstallLocation,
			SizeBytes:       p.SizeBytes,
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
	for _, n := range inv.Networks {
		pb.NetworkInterfaces = append(pb.NetworkInterfaces, &invv1.NetworkInterface{
			Name:        n.Name,
			Mac:         n.MAC,
			IpAddresses: n.IPAddresses,
			Subnet:      n.Subnet,
			Gateway:     n.Gateway,
			Dns:         n.DNS,
			Dhcp:        n.DHCP,
			SpeedBps:    n.SpeedBps,
			Type:        n.Type,
			Up:          n.Up,
		})
	}
	for _, d := range inv.Disks {
		pd := &invv1.Disk{
			Model:     d.Model,
			Serial:    d.Serial,
			SizeBytes: d.SizeBytes,
			MediaType: d.MediaType,
			Interface: d.Interface,
		}
		for _, part := range d.Partitions {
			pd.Partitions = append(pd.Partitions, &invv1.Partition{
				Mount:     part.Mount,
				Fs:        part.FS,
				SizeBytes: part.SizeBytes,
				FreeBytes: part.FreeBytes,
			})
		}
		pb.Disks = append(pb.Disks, pd)
	}
	return pb
}

func memoryToProto(m store.MemoryInfo) *invv1.MemoryInfo {
	pb := &invv1.MemoryInfo{
		TotalPhysicalBytes: m.TotalPhysicalBytes,
		Array: &invv1.MemoryArray{
			Location:        m.Array.Location,
			Use:             m.Array.Use,
			ErrorCorrection: m.Array.ErrorCorrection,
			MaximumCapacity: m.Array.MaximumCapacity,
			NumberOfDevices: m.Array.NumberOfDevices,
		},
	}
	for _, mod := range m.Modules {
		pb.Modules = append(pb.Modules, &invv1.MemoryModule{
			DeviceLocator:      mod.DeviceLocator,
			BankLocator:        mod.BankLocator,
			CapacityBytes:      mod.CapacityBytes,
			FormFactor:         mod.FormFactor,
			MemoryType:         mod.MemoryType,
			SpeedMtS:           mod.SpeedMTs,
			ConfiguredSpeedMtS: mod.ConfiguredSpeedMTs,
			Manufacturer:       mod.Manufacturer,
			SerialNumber:       mod.SerialNumber,
			PartNumber:         mod.PartNumber,
		})
	}
	return pb
}
