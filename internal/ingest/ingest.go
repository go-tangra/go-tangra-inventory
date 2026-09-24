// Package ingest is the off-mesh INGEST EDGE of the inventory service: the
// gRPC surface (inventory.v1.IngestService) spoken by untrusted endpoint
// agents. It is served on a dedicated listener that is network-isolated from
// the mesh module-to-module API and does NOT use SPIFFE. Agents authenticate
// with a per-agent sealed credential presented in call metadata (see auth.go);
// Enroll is the single unauthenticated method, consuming an enrollment token.
//
// Trust boundary: every agent is scoped to the tenant recorded on its verified
// agent row. A payload NEVER carries a tenant; an agent can only ever write
// within its own tenant, so a compromised or malicious agent cannot reach
// another tenant's hosts. Enrollment tokens and per-agent credentials are
// treated as secrets and are never logged or echoed back beyond the single
// EnrollResponse that issues the credential.
package ingest

import (
	"context"
	"errors"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// defaultMaxBytes bounds an inbound SubmitInventory message when the caller
// does not configure a limit (8 MiB).
const defaultMaxBytes int64 = 8 << 20

// Server implements inventoryv1.IngestServiceServer for the agent plane.
type Server struct {
	inventoryv1.UnimplementedIngestServiceServer

	enroll     *enroll.Service
	snaps      *snapshots.Service
	reg        registry.Registry
	st         repo.Store
	maxBytes   int64
	instanceID string
}

// New builds an ingest Server. A non-positive maxBytes falls back to
// defaultMaxBytes.
func New(enrollSvc *enroll.Service, snaps *snapshots.Service, reg registry.Registry, st repo.Store, maxBytes int64, instanceID string) *Server {
	if maxBytes <= 0 {
		maxBytes = defaultMaxBytes
	}
	return &Server{
		enroll:     enrollSvc,
		snaps:      snaps,
		reg:        reg,
		st:         st,
		maxBytes:   maxBytes,
		instanceID: instanceID,
	}
}

// MaxBytes is the configured inbound message ceiling (bytes). The app uses it
// to set grpc.MaxRecvMsgSize on the ingest listener.
func (s *Server) MaxBytes() int64 { return s.maxBytes }

// InstanceID identifies the process serving this edge (audit/diagnostics).
func (s *Server) InstanceID() string { return s.instanceID }

// Enroll is UNAUTHENTICATED: the enrollment token in the request is itself the
// credential. It consumes the single-use token and issues the per-agent
// credential exactly once. The token and the issued credential are never
// logged; only coarse outcomes are surfaced.
func (s *Server) Enroll(ctx context.Context, req *inventoryv1.EnrollRequest) (*inventoryv1.EnrollResponse, error) {
	if req == nil || req.GetIdentity() == nil {
		return nil, status.Error(codes.InvalidArgument, "identity required")
	}
	id := req.GetIdentity()
	ident := store.Identity{
		HardwareUUID: id.GetHardwareUuid(),
		MachineID:    id.GetMachineId(),
		Hostname:     id.GetHostname(),
	}
	agentID, credential, err := s.enroll.Enroll(ctx, req.GetEnrollmentToken(), ident, req.GetAgentVersion())
	if err != nil {
		if errors.Is(err, enroll.ErrTokenInvalid) {
			return nil, status.Error(codes.PermissionDenied, "enrollment token invalid")
		}
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	return &inventoryv1.EnrollResponse{
		AgentId:         agentID,
		AgentCredential: credential,
	}, nil
}

// SubmitInventory is AUTHENTICATED. The tenant scope is taken from the verified
// agent in the context (never from the payload), the message size is enforced
// before any write, the payload is mapped to the domain inventory and ingested,
// and the agent's last-seen/version/host is touched. A snapshot summary is
// returned; no credential is ever included.
func (s *Server) SubmitInventory(ctx context.Context, req *inventoryv1.SubmitRequest) (*inventoryv1.SubmitResponse, error) {
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	if req == nil || req.GetInventory() == nil {
		return nil, status.Error(codes.InvalidArgument, "inventory required")
	}
	// Enforce the size ceiling BEFORE any write (no partial ingest).
	if int64(proto.Size(req)) > s.maxBytes {
		return nil, status.Error(codes.InvalidArgument, "inventory payload too large")
	}

	inv := inventoryFromProto(req.GetInventory())
	// The agent may not report a tenant; the write is bound to the verified
	// agent's tenant.
	snap, err := s.snaps.Ingest(ctx, agent.TenantID, inv, store.SourceAgent)
	if err != nil {
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}

	// Touch the agent's liveness/version and bind it to the resolved host.
	if err := s.st.TouchAgent(ctx, agent.ID, inv.AgentVersion, snap.HostID, snap.ReceivedAt); err != nil {
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}

	return &inventoryv1.SubmitResponse{
		SnapshotId: snap.ID,
		HostId:     snap.HostID,
		ReceivedAt: snap.ReceivedAt.Unix(),
	}, nil
}

// StreamCommands is AUTHENTICATED. It registers the verified agent as a live
// connection and forwards pushed commands to the client until the client
// disconnects or the context is cancelled. The connection is always
// unregistered on exit (which publishes the agent offline). The connection is
// bound to the verified agent (tenant/host/id), not to fields in the request.
func (s *Server) StreamCommands(req *inventoryv1.StreamRequest, stream inventoryv1.IngestService_StreamCommandsServer) error {
	ctx := stream.Context()
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "agent credential required")
	}

	version := agent.AgentVersion
	if req != nil && req.GetAgentVersion() != "" {
		version = req.GetAgentVersion()
	}

	commands, unregister, err := s.reg.Register(ctx, registry.ConnectedAgent{
		AgentID:     agent.ID,
		TenantID:    agent.TenantID,
		HostID:      agent.HostID,
		Version:     version,
		ConnectedAt: time.Now().UTC(),
	})
	if err != nil {
		return status.Error(codes.Unavailable, "cannot register connection")
	}
	defer unregister()

	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case cmd, open := <-commands:
			if !open {
				// Connection superseded (e.g. reconnect) or shut down.
				return nil
			}
			if err := stream.Send(&inventoryv1.Command{
				CommandId: cmd.ID,
				Type:      commandType(cmd.Type),
			}); err != nil {
				return err
			}
		}
	}
}

// commandType maps a registry command type string to the proto enum. Unknown
// types map to UNSPECIFIED so a new server never crashes an old agent.
func commandType(t string) inventoryv1.CommandType {
	if v, ok := inventoryv1.CommandType_value[t]; ok {
		return inventoryv1.CommandType(v)
	}
	switch t {
	case "refresh":
		return inventoryv1.CommandType_COMMAND_TYPE_REFRESH
	}
	return inventoryv1.CommandType_COMMAND_TYPE_UNSPECIFIED
}

// inventoryFromProto maps the wire Inventory to the domain store.Inventory. It
// mirrors the agent-side sender mapping in reverse (server-side inbound) and is
// nil-safe via the generated getters. Unix-second timestamps of 0 map to the
// zero time so downstream defaulting (e.g. collected_at -> now) still applies.
func inventoryFromProto(pb *inventoryv1.Inventory) store.Inventory {
	inv := store.Inventory{
		Identity: store.Identity{
			HardwareUUID: pb.GetIdentity().GetHardwareUuid(),
			MachineID:    pb.GetIdentity().GetMachineId(),
			Hostname:     pb.GetIdentity().GetHostname(),
		},
		CollectedAt:  unixToTime(pb.GetCollectedAt()),
		AgentVersion: pb.GetAgentVersion(),
		OS: store.OSInfo{
			Name:        pb.GetOs().GetName(),
			Version:     pb.GetOs().GetVersion(),
			Build:       pb.GetOs().GetBuild(),
			Arch:        pb.GetOs().GetArch(),
			Kernel:      pb.GetOs().GetKernel(),
			InstallDate: unixToTime(pb.GetOs().GetInstallDate()),
			LastBoot:    unixToTime(pb.GetOs().GetLastBoot()),
			UptimeSec:   pb.GetOs().GetUptimeSec(),
		},
		BIOS: store.BIOSInfo{
			Vendor:      pb.GetBios().GetVendor(),
			Version:     pb.GetBios().GetVersion(),
			ReleaseDate: pb.GetBios().GetReleaseDate(),
		},
		System: store.SystemInfo{
			Manufacturer: pb.GetSystem().GetManufacturer(),
			ProductName:  pb.GetSystem().GetProductName(),
			Version:      pb.GetSystem().GetVersion(),
			SerialNumber: pb.GetSystem().GetSerialNumber(),
			UUID:         pb.GetSystem().GetUuid(),
			WakeUpType:   pb.GetSystem().GetWakeUpType(),
			SKUNumber:    pb.GetSystem().GetSkuNumber(),
			Family:       pb.GetSystem().GetFamily(),
		},
		Baseboard: store.BaseboardInfo{
			Manufacturer:      pb.GetBaseboard().GetManufacturer(),
			Product:           pb.GetBaseboard().GetProduct(),
			Version:           pb.GetBaseboard().GetVersion(),
			SerialNumber:      pb.GetBaseboard().GetSerialNumber(),
			AssetTag:          pb.GetBaseboard().GetAssetTag(),
			LocationInChassis: pb.GetBaseboard().GetLocationInChassis(),
			BoardType:         pb.GetBaseboard().GetBoardType(),
		},
		Chassis: store.ChassisInfo{
			Manufacturer: pb.GetChassis().GetManufacturer(),
			Version:      pb.GetChassis().GetVersion(),
			SerialNumber: pb.GetChassis().GetSerialNumber(),
			AssetTag:     pb.GetChassis().GetAssetTag(),
			SKUNumber:    pb.GetChassis().GetSkuNumber(),
			Type:         pb.GetChassis().GetType(),
		},
		Memory:       memoryFromProto(pb.GetMemory()),
		Ports:        pb.GetPorts(),
		Slots:        pb.GetSlots(),
		OEMStrings:   pb.GetOemStrings(),
		BIOSLanguage: pb.GetBiosLanguage(),
		Environment: store.Environment{
			Domain:    pb.GetEnvironment().GetDomain(),
			Workgroup: pb.GetEnvironment().GetWorkgroup(),
			Timezone:  pb.GetEnvironment().GetTimezone(),
			Locale:    pb.GetEnvironment().GetLocale(),
		},
	}

	for _, p := range pb.GetProcessors() {
		inv.Processors = append(inv.Processors, store.Processor{
			SocketDesignation: p.GetSocketDesignation(),
			Manufacturer:      p.GetManufacturer(),
			Version:           p.GetVersion(),
			MaxSpeedMHz:       p.GetMaxSpeedMhz(),
			CurrentSpeedMHz:   p.GetCurrentSpeedMhz(),
			CoreCount:         p.GetCoreCount(),
			CoreEnabled:       p.GetCoreEnabled(),
			ThreadCount:       p.GetThreadCount(),
			PartNumber:        p.GetPartNumber(),
			SerialNumber:      p.GetSerialNumber(),
			SocketPopulated:   p.GetSocketPopulated(),
		})
	}
	for _, c := range pb.GetCache() {
		inv.Cache = append(inv.Cache, store.CacheInfo{SocketDesignation: c.GetSocketDesignation()})
	}
	for _, m := range pb.GetMonitors() {
		inv.Monitors = append(inv.Monitors, store.Monitor{
			Manufacturer: m.GetManufacturer(),
			Model:        m.GetModel(),
			SerialNumber: m.GetSerialNumber(),
		})
	}
	for _, p := range pb.GetInstalledPrograms() {
		inv.Programs = append(inv.Programs, store.Program{
			Name:            p.GetName(),
			Version:         p.GetVersion(),
			Publisher:       p.GetPublisher(),
			InstallDate:     p.GetInstallDate(),
			InstallLocation: p.GetInstallLocation(),
			SizeBytes:       p.GetSizeBytes(),
		})
	}
	for _, sv := range pb.GetServices() {
		inv.Services = append(inv.Services, store.Service{
			Name:        sv.GetName(),
			DisplayName: sv.GetDisplayName(),
			State:       sv.GetState(),
			StartMode:   sv.GetStartMode(),
			Account:     sv.GetAccount(),
		})
	}
	for _, u := range pb.GetUsers() {
		inv.Users = append(inv.Users, store.UserAccount{
			Name:      u.GetName(),
			IsAdmin:   u.GetIsAdmin(),
			LastLogon: unixToTime(u.GetLastLogon()),
		})
	}
	for _, p := range pb.GetPatches() {
		inv.Patches = append(inv.Patches, store.Patch{
			ID:          p.GetId(),
			InstalledOn: p.GetInstalledOn(),
		})
	}
	for _, n := range pb.GetNetworkInterfaces() {
		inv.Networks = append(inv.Networks, store.NetIface{
			Name:        n.GetName(),
			MAC:         n.GetMac(),
			IPAddresses: n.GetIpAddresses(),
			Subnet:      n.GetSubnet(),
			Gateway:     n.GetGateway(),
			DNS:         n.GetDns(),
			DHCP:        n.GetDhcp(),
			SpeedBps:    n.GetSpeedBps(),
			Type:        n.GetType(),
			Up:          n.GetUp(),
		})
	}
	for _, d := range pb.GetDisks() {
		disk := store.Disk{
			Model:     d.GetModel(),
			Serial:    d.GetSerial(),
			SizeBytes: d.GetSizeBytes(),
			MediaType: d.GetMediaType(),
			Interface: d.GetInterface(),
		}
		for _, part := range d.GetPartitions() {
			disk.Partitions = append(disk.Partitions, store.Partition{
				Mount:     part.GetMount(),
				FS:        part.GetFs(),
				SizeBytes: part.GetSizeBytes(),
				FreeBytes: part.GetFreeBytes(),
			})
		}
		inv.Disks = append(inv.Disks, disk)
	}
	return inv
}

func memoryFromProto(pb *inventoryv1.MemoryInfo) store.MemoryInfo {
	m := store.MemoryInfo{
		TotalPhysicalBytes: pb.GetTotalPhysicalBytes(),
		Array: store.MemoryArray{
			Location:        pb.GetArray().GetLocation(),
			Use:             pb.GetArray().GetUse(),
			ErrorCorrection: pb.GetArray().GetErrorCorrection(),
			MaximumCapacity: pb.GetArray().GetMaximumCapacity(),
			NumberOfDevices: pb.GetArray().GetNumberOfDevices(),
		},
	}
	for _, mod := range pb.GetModules() {
		m.Modules = append(m.Modules, store.MemoryModule{
			DeviceLocator:      mod.GetDeviceLocator(),
			BankLocator:        mod.GetBankLocator(),
			CapacityBytes:      mod.GetCapacityBytes(),
			FormFactor:         mod.GetFormFactor(),
			MemoryType:         mod.GetMemoryType(),
			SpeedMTs:           mod.GetSpeedMtS(),
			ConfiguredSpeedMTs: mod.GetConfiguredSpeedMtS(),
			Manufacturer:       mod.GetManufacturer(),
			SerialNumber:       mod.GetSerialNumber(),
			PartNumber:         mod.GetPartNumber(),
		})
	}
	return m
}

// unixToTime converts unix seconds to a UTC time, mapping 0 to the zero time.
func unixToTime(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0).UTC()
}

var _ inventoryv1.IngestServiceServer = (*Server)(nil)
