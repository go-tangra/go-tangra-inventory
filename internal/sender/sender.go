package sender

import (
	"context"
	"fmt"
	"time"

	"github.com/go-tangra/go-tangra-inventory/internal/collector"

	collectorv1 "github.com/go-tangra/go-tangra-inventory/gen/go/inventory/collector/v1"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Send connects to the collector at addr and submits the inventory.
// When secret is non-empty, it is sent as the x-client-secret gRPC metadata header.
// Returns the assigned record ID.
func Send(ctx context.Context, addr string, secret string, inv *collector.Inventory) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if secret != "" {
		ctx = metadata.AppendToOutgoingContext(ctx, "x-client-secret", secret)
	}

	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return 0, fmt.Errorf("connect to collector: %w", err)
	}
	defer conn.Close()

	client := collectorv1.NewInventoryCollectorServiceClient(conn)

	pbInv := toProto(inv)

	resp, err := client.SubmitInventory(ctx, &collectorv1.SubmitInventoryRequest{
		Inventory: pbInv,
	})
	if err != nil {
		return 0, fmt.Errorf("submit inventory: %w", err)
	}

	return resp.Id, nil
}

func toProto(inv *collector.Inventory) *collectorv1.Inventory {
	pb := &collectorv1.Inventory{
		CollectedAt: timestamppb.New(inv.CollectedAt),
		Hostname:    inv.Hostname,
		Username:    inv.Username,
		SmbiosVersion: &collectorv1.VersionInfo{
			Major:    int32(inv.SMBIOSVersion.Major),
			Minor:    int32(inv.SMBIOSVersion.Minor),
			Revision: int32(inv.SMBIOSVersion.Revision),
		},
		Bios: &collectorv1.BIOSInfo{
			Vendor:      inv.BIOS.Vendor,
			Version:     inv.BIOS.Version,
			ReleaseDate: inv.BIOS.ReleaseDate,
		},
		System: &collectorv1.SystemInfo{
			Manufacturer: inv.System.Manufacturer,
			ProductName:  inv.System.ProductName,
			Version:      inv.System.Version,
			SerialNumber: inv.System.SerialNumber,
			Uuid:         inv.System.UUID,
			WakeUpType:   inv.System.WakeUpType,
			SkuNumber:    inv.System.SKUNumber,
			Family:       inv.System.Family,
		},
		Baseboard: &collectorv1.BaseboardInfo{
			Manufacturer:    inv.Baseboard.Manufacturer,
			Product:         inv.Baseboard.Product,
			Version:         inv.Baseboard.Version,
			SerialNumber:    inv.Baseboard.SerialNumber,
			AssetTag:        inv.Baseboard.AssetTag,
			LocationInChassis: inv.Baseboard.LocationInChassis,
			BoardType:       inv.Baseboard.BoardType,
		},
		Chassis: &collectorv1.ChassisInfo{
			Manufacturer:   inv.Chassis.Manufacturer,
			Version:        inv.Chassis.Version,
			SerialNumber:   inv.Chassis.SerialNumber,
			AssetTagNumber: inv.Chassis.AssetTagNumber,
			SkuNumber:      inv.Chassis.SKUNumber,
		},
		OemStrings: inv.OEMStrings,
	}

	// Processors
	for _, p := range inv.Processors {
		pb.Processors = append(pb.Processors, &collectorv1.ProcessorInfo{
			SocketDesignation: p.SocketDesignation,
			Manufacturer:      p.Manufacturer,
			Version:           p.Version,
			MaxSpeedMhz:       uint32(p.MaxSpeedMHz),
			CurrentSpeedMhz:   uint32(p.CurrentSpeedMHz),
			SocketPopulated:   p.SocketPopulated,
			SerialNumber:      p.SerialNumber,
			AssetTag:          p.AssetTag,
			PartNumber:        p.PartNumber,
			CoreCount:         uint32(p.CoreCount),
			CoreEnabled:       uint32(p.CoreEnabled),
			ThreadCount:       uint32(p.ThreadCount),
		})
	}

	// Cache
	for _, c := range inv.Cache {
		pb.Cache = append(pb.Cache, &collectorv1.CacheInfo{
			SocketDesignation: c.SocketDesignation,
		})
	}

	// Memory
	pb.Memory = &collectorv1.MemoryInfo{
		TotalPhysicalBytes: inv.Memory.TotalPhysicalBytes,
		TotalPhysicalGb:    inv.Memory.TotalPhysicalGB,
		Array: &collectorv1.PhysicalMemoryArray{
			Location:              inv.Memory.Array.Location,
			Use:                   inv.Memory.Array.Use,
			ErrorCorrection:       inv.Memory.Array.ErrorCorrection,
			MaximumCapacity:       inv.Memory.Array.MaximumCapacity,
			NumberOfMemoryDevices: uint32(inv.Memory.Array.NumberOfMemoryDevices),
		},
	}
	for _, m := range inv.Memory.Modules {
		pb.Memory.Modules = append(pb.Memory.Modules, &collectorv1.MemoryModule{
			DeviceLocator:      m.DeviceLocator,
			BankLocator:        m.BankLocator,
			CapacityBytes:      m.CapacityBytes,
			FormFactor:         m.FormFactor,
			MemoryType:         m.MemoryType,
			TypeDetail:         m.TypeDetail,
			SpeedMtS:           uint32(m.SpeedMTs),
			ConfiguredSpeedMtS: uint32(m.ConfiguredSpeedMTs),
			Manufacturer:       m.Manufacturer,
			SerialNumber:       m.SerialNumber,
			AssetTag:           m.AssetTag,
			PartNumber:         m.PartNumber,
			MinimumVoltage:     m.MinimumVoltage,
			MaximumVoltage:     m.MaximumVoltage,
			ConfiguredVoltage:  m.ConfiguredVoltage,
			TotalWidth:         m.TotalWidthBits,
			DataWidth:          m.DataWidthBits,
		})
	}

	// Ports
	for _, p := range inv.Ports {
		pb.Ports = append(pb.Ports, &collectorv1.PortInfo{
			InternalDesignator: p.InternalDesignator,
			ExternalDesignator: p.ExternalDesignator,
		})
	}

	// Slots
	for _, s := range inv.Slots {
		pb.Slots = append(pb.Slots, &collectorv1.SlotInfo{
			Designation: s.Designation,
		})
	}

	// BIOS Language
	pb.BiosLanguage = &collectorv1.BIOSLanguageInfo{
		CurrentLanguage:      inv.BIOSLanguage.CurrentLanguage,
		InstallableLanguages: inv.BIOSLanguage.InstallableLanguages,
	}

	// Monitors
	for _, m := range inv.Monitor {
		pb.Monitor = append(pb.Monitor, &collectorv1.MonitorInfo{
			Manufacturer: m.Manufacturer,
			Model:        m.Model,
			SerialNumber: m.SerialNumber,
		})
	}

	return pb
}
