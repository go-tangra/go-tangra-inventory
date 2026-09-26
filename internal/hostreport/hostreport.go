package hostreport

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/invpb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Project builds the host report of host from its latest snapshot, with the
// digest filled in. It is pure and deterministic.
func Project(host store.Host, snap store.Snapshot) *invv1.HostReport {
	inv := snap.Payload
	r := &invv1.HostReport{
		TenantId:          host.TenantID,
		Host:              projectHost(host),
		SnapshotId:        snap.ID,
		CollectedAt:       unix(snap.CollectedAt),
		ReportChangedAt:   unixMilli(host.ReportChangedAt),
		AgentVersion:      snap.AgentVersion,
		OsFamily:          OSFamily(inv.OS),
		NetworkInterfaces: invpb.NetIfacesToPB(inv.Networks),
		PrimaryIpv4:       inv.PrimaryIPv4,
		PrimaryIpv6:       inv.PrimaryIPv6,
		Virtualization:    invpb.VirtualizationToPB(inv.Virtualization),
		Bmc:               invpb.BmcToPB(inv.Bmc),
		HypervisorGuests:  invpb.GuestsToPB(inv.HypervisorGuests),
		UpdateState:       invpb.UpdateStateToPB(inv.UpdateState),
		Truncated:         invpb.LimitsToPB(inv.Truncated),
	}
	for _, p := range inv.Programs {
		if p.AvailableVersion == "" {
			continue
		}
		if len(r.PendingUpdates) == store.MaxPendingUpdates {
			break
		}
		r.PendingUpdates = append(r.PendingUpdates, &invv1.PendingUpdate{
			Name: p.Name, InstalledVersion: p.Version, AvailableVersion: p.AvailableVersion, Security: p.SecurityUpdate,
		})
	}
	r.ReportDigest = Digest(r)
	return r
}

// DigestView is the digest-only row of a host: host identity and status, the
// stored digest and its change time. It never carries report data.
func DigestView(host store.Host) *invv1.HostReport {
	return &invv1.HostReport{
		TenantId:        host.TenantID,
		Host:            projectHost(host),
		ReportDigest:    host.ReportDigest,
		ReportChangedAt: unixMilli(host.ReportChangedAt),
	}
}

// Digest is the hex sha256 of the deterministic encoding of r without the
// fields that change on every report although nothing IPAM uses changed:
// host.last_seen, snapshot_id, collected_at, update_state.checked_at (and the
// digest/changed-at fields themselves).
func Digest(r *invv1.HostReport) string {
	c := proto.CloneOf(r)
	c.SnapshotId, c.CollectedAt, c.ReportChangedAt, c.ReportDigest = "", 0, 0, ""
	if c.Host != nil {
		c.Host.LastSeen = 0
	}
	if c.UpdateState != nil {
		c.UpdateState.CheckedAt = 0
	}
	b, _ := proto.MarshalOptions{Deterministic: true}.Marshal(c) // a well-formed message always marshals
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// OSFamily is the reported OS family, falling back (older agents) to
// "windows" when the OS name says so and "linux" otherwise.
func OSFamily(os store.OSInfo) string {
	if os.Family != "" {
		return os.Family
	}
	if strings.Contains(strings.ToLower(os.Name), "windows") {
		return "windows"
	}
	return "linux"
}

// projectHost keeps only the host fields IPAM matches and displays on.
func projectHost(h store.Host) *invv1.Host {
	return &invv1.Host{
		Id: h.ID, TenantId: h.TenantID, Hostname: h.Hostname, SystemSerial: h.SystemSerial,
		Manufacturer: h.Manufacturer, Model: h.Model, OsName: h.OSName, OsVersion: h.OSVersion,
		Status: statusToPB(h.Status), LastSeen: unix(h.LastSeen),
	}
}

func statusToPB(s string) invv1.HostStatus {
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

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
