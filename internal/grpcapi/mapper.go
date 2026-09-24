package grpcapi

import (
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// unix returns t as unix seconds, or 0 for the zero time.
func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// ---- enums

func hostStatusToPB(s string) invv1.HostStatus {
	switch s {
	case store.HostActive:
		return invv1.HostStatus_HOST_STATUS_ACTIVE
	case store.HostStale:
		return invv1.HostStatus_HOST_STATUS_STALE
	case store.HostRetired:
		return invv1.HostStatus_HOST_STATUS_RETIRED
	}
	return invv1.HostStatus_HOST_STATUS_UNSPECIFIED
}

func hostStatusFromPB(s invv1.HostStatus) string {
	switch s {
	case invv1.HostStatus_HOST_STATUS_ACTIVE:
		return store.HostActive
	case invv1.HostStatus_HOST_STATUS_STALE:
		return store.HostStale
	case invv1.HostStatus_HOST_STATUS_RETIRED:
		return store.HostRetired
	}
	return ""
}

func snapshotSourceToPB(s string) invv1.SnapshotSource {
	switch s {
	case store.SourceAgent:
		return invv1.SnapshotSource_SNAPSHOT_SOURCE_AGENT
	case store.SourceManual:
		return invv1.SnapshotSource_SNAPSHOT_SOURCE_MANUAL
	case store.SourceImport:
		return invv1.SnapshotSource_SNAPSHOT_SOURCE_IMPORT
	}
	return invv1.SnapshotSource_SNAPSHOT_SOURCE_UNSPECIFIED
}

func changeTypeToPB(s string) invv1.ChangeType {
	switch s {
	case store.ChangeAdded:
		return invv1.ChangeType_CHANGE_TYPE_ADDED
	case store.ChangeRemoved:
		return invv1.ChangeType_CHANGE_TYPE_REMOVED
	case store.ChangeModified:
		return invv1.ChangeType_CHANGE_TYPE_MODIFIED
	}
	return invv1.ChangeType_CHANGE_TYPE_UNSPECIFIED
}

// ---- hosts

func hostToPB(v hosts.View) *invv1.Host {
	return &invv1.Host{
		Id: v.ID, TenantId: v.TenantID, Hostname: v.Hostname, MachineId: v.MachineID,
		HardwareUuid: v.HardwareUUID, SystemSerial: v.SystemSerial, IdentityKey: v.IdentityKey,
		Manufacturer: v.Manufacturer, Model: v.Model, OsName: v.OSName, OsVersion: v.OSVersion,
		OsArch: v.OSArch, AgentVersion: v.AgentVersion, AssignedUser: v.AssignedUser,
		Status: hostStatusToPB(v.Status), Tags: v.Tags, FirstSeen: unix(v.FirstSeen),
		LastSeen: unix(v.LastSeen), LastSnapshotId: v.LastSnapshotID,
		CreatedAt: unix(v.CreatedAt), UpdatedAt: unix(v.UpdatedAt),
	}
}

// ---- snapshots

func snapshotSummaryToPB(v snapshots.View) *invv1.SnapshotSummary {
	return &invv1.SnapshotSummary{
		Id: v.ID, TenantId: v.TenantID, HostId: v.HostID, CollectedAt: unix(v.CollectedAt),
		ReceivedAt: unix(v.ReceivedAt), AgentVersion: v.AgentVersion, Source: snapshotSourceToPB(v.Source),
		OsName: v.OSName, OsVersion: v.OSVersion, Manufacturer: v.Manufacturer, Model: v.Model,
	}
}

func snapshotToPB(v snapshots.View) *invv1.Snapshot {
	out := &invv1.Snapshot{Summary: snapshotSummaryToPB(v)}
	if v.Payload != nil {
		out.Payload = inventoryToPB(*v.Payload)
	}
	return out
}

// ---- changes

func changeToPB(c store.Change) *invv1.Change {
	return &invv1.Change{
		Id: c.ID, TenantId: c.TenantID, HostId: c.HostID, SnapshotId: c.SnapshotID,
		PrevSnapshotId: c.PrevSnapshotID, DetectedAt: unix(c.DetectedAt), Category: c.Category,
		ChangeType: changeTypeToPB(c.ChangeType), ComponentKey: c.ComponentKey,
		Before: c.Before, After: c.After,
	}
}

// ---- statistics

func statsToPB(s repo.Stats) *invv1.Stats {
	return &invv1.Stats{
		HostsTotal: s.HostsTotal, HostsByStatus: s.HostsByStatus, HostsByOs: s.HostsByOS,
		HostsByManufacturer: s.HostsByManufacturer, AgentsOnline: s.AgentsOnline,
		AgentsOffline: s.AgentsOffline, StaleHosts: s.StaleHosts, SnapshotsTotal: s.SnapshotsTotal,
		TotalMemoryBytes: s.TotalMemoryBytes, TotalCpuCores: s.TotalCPUCores,
		TotalDiskBytes: s.TotalDiskBytes, TopPrograms: s.TopPrograms, OsVersions: s.OSVersions,
	}
}

// ---- connected agents

func connectedAgentToPB(a registry.ConnectedAgent) *invv1.ConnectedAgent {
	return &invv1.ConnectedAgent{
		AgentId: a.AgentID, HostId: a.HostID, AgentVersion: a.Version,
		Online: true, LastSeen: unix(a.ConnectedAt),
	}
}

// ---- full inventory payload

func identityToPB(id store.Identity) *invv1.Identity {
	return &invv1.Identity{HardwareUuid: id.HardwareUUID, MachineId: id.MachineID, Hostname: id.Hostname}
}

// inventoryToPB maps a stored store.Inventory to the wire Inventory message.
func inventoryToPB(inv store.Inventory) *invv1.Inventory {
	pb := &invv1.Inventory{
		Identity:     identityToPB(inv.Identity),
		CollectedAt:  unix(inv.CollectedAt),
		AgentVersion: inv.AgentVersion,
		Os: &invv1.OSInfo{
			Name: inv.OS.Name, Version: inv.OS.Version, Build: inv.OS.Build, Arch: inv.OS.Arch,
			Kernel: inv.OS.Kernel, UptimeSec: inv.OS.UptimeSec,
		},
		Bios: &invv1.BIOSInfo{Vendor: inv.BIOS.Vendor, Version: inv.BIOS.Version, ReleaseDate: inv.BIOS.ReleaseDate},
		System: &invv1.SystemInfo{
			Manufacturer: inv.System.Manufacturer, ProductName: inv.System.ProductName, Version: inv.System.Version,
			SerialNumber: inv.System.SerialNumber, Uuid: inv.System.UUID, WakeUpType: inv.System.WakeUpType,
			SkuNumber: inv.System.SKUNumber, Family: inv.System.Family,
		},
		Baseboard: &invv1.BaseboardInfo{
			Manufacturer: inv.Baseboard.Manufacturer, Product: inv.Baseboard.Product, Version: inv.Baseboard.Version,
			SerialNumber: inv.Baseboard.SerialNumber, AssetTag: inv.Baseboard.AssetTag,
			LocationInChassis: inv.Baseboard.LocationInChassis, BoardType: inv.Baseboard.BoardType,
		},
		Chassis: &invv1.ChassisInfo{
			Manufacturer: inv.Chassis.Manufacturer, Version: inv.Chassis.Version, SerialNumber: inv.Chassis.SerialNumber,
			AssetTag: inv.Chassis.AssetTag, SkuNumber: inv.Chassis.SKUNumber, Type: inv.Chassis.Type,
		},
		Memory:       memoryToPB(inv.Memory),
		Ports:        inv.Ports,
		Slots:        inv.Slots,
		OemStrings:   inv.OEMStrings,
		BiosLanguage: inv.BIOSLanguage,
		Environment: &invv1.Environment{
			Domain: inv.Environment.Domain, Workgroup: inv.Environment.Workgroup,
			Timezone: inv.Environment.Timezone, Locale: inv.Environment.Locale,
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
			SocketDesignation: p.SocketDesignation, Manufacturer: p.Manufacturer, Version: p.Version,
			MaxSpeedMhz: p.MaxSpeedMHz, CurrentSpeedMhz: p.CurrentSpeedMHz, CoreCount: p.CoreCount,
			CoreEnabled: p.CoreEnabled, ThreadCount: p.ThreadCount, PartNumber: p.PartNumber,
			SerialNumber: p.SerialNumber, SocketPopulated: p.SocketPopulated,
		})
	}
	for _, c := range inv.Cache {
		pb.Cache = append(pb.Cache, &invv1.CacheInfo{SocketDesignation: c.SocketDesignation})
	}
	for _, m := range inv.Monitors {
		pb.Monitors = append(pb.Monitors, &invv1.Monitor{Manufacturer: m.Manufacturer, Model: m.Model, SerialNumber: m.SerialNumber})
	}
	for _, p := range inv.Programs {
		pb.InstalledPrograms = append(pb.InstalledPrograms, &invv1.Program{
			Name: p.Name, Version: p.Version, Publisher: p.Publisher, InstallDate: p.InstallDate,
			InstallLocation: p.InstallLocation, SizeBytes: p.SizeBytes,
		})
	}
	for _, sv := range inv.Services {
		pb.Services = append(pb.Services, &invv1.Service{
			Name: sv.Name, DisplayName: sv.DisplayName, State: sv.State, StartMode: sv.StartMode, Account: sv.Account,
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
			Name: n.Name, Mac: n.MAC, IpAddresses: n.IPAddresses, Subnet: n.Subnet, Gateway: n.Gateway,
			Dns: n.DNS, Dhcp: n.DHCP, SpeedBps: n.SpeedBps, Type: n.Type, Up: n.Up,
		})
	}
	for _, d := range inv.Disks {
		pd := &invv1.Disk{Model: d.Model, Serial: d.Serial, SizeBytes: d.SizeBytes, MediaType: d.MediaType, Interface: d.Interface}
		for _, part := range d.Partitions {
			pd.Partitions = append(pd.Partitions, &invv1.Partition{Mount: part.Mount, Fs: part.FS, SizeBytes: part.SizeBytes, FreeBytes: part.FreeBytes})
		}
		pb.Disks = append(pb.Disks, pd)
	}
	return pb
}

func memoryToPB(m store.MemoryInfo) *invv1.MemoryInfo {
	pb := &invv1.MemoryInfo{
		TotalPhysicalBytes: m.TotalPhysicalBytes,
		Array: &invv1.MemoryArray{
			Location: m.Array.Location, Use: m.Array.Use, ErrorCorrection: m.Array.ErrorCorrection,
			MaximumCapacity: m.Array.MaximumCapacity, NumberOfDevices: m.Array.NumberOfDevices,
		},
	}
	for _, mod := range m.Modules {
		pb.Modules = append(pb.Modules, &invv1.MemoryModule{
			DeviceLocator: mod.DeviceLocator, BankLocator: mod.BankLocator, CapacityBytes: mod.CapacityBytes,
			FormFactor: mod.FormFactor, MemoryType: mod.MemoryType, SpeedMtS: mod.SpeedMTs,
			ConfiguredSpeedMtS: mod.ConfiguredSpeedMTs, Manufacturer: mod.Manufacturer,
			SerialNumber: mod.SerialNumber, PartNumber: mod.PartNumber,
		})
	}
	return pb
}
