package inventoryclient

import (
	"context"
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// HostReport is the IPAM-oriented projection of a host's latest snapshot, as
// served by inventory.v1.HostReportService. A digest-view report carries only
// TenantID, Host, Digest and ChangedAt.
type HostReport struct {
	TenantID       string
	Host           Host
	SnapshotID     string
	CollectedAt    time.Time
	ChangedAt      time.Time // report_changed_at (inventory clock, ms precision)
	Digest         string    // hex sha256 of the projection
	AgentVersion   string
	OSFamily       string
	Interfaces     []NetworkInterface
	PrimaryIPv4    string
	PrimaryIPv6    string
	Virtualization Virtualization
	BMC            *BMC // nil = no BMC or not readable
	Guests         []HypervisorGuest
	Updates        UpdateState
	PendingUpdates []PendingUpdate
	Truncated      CollectionLimits
}

// PendingUpdate is an installed package with a newer version available.
type PendingUpdate struct {
	Name             string
	InstalledVersion string
	AvailableVersion string
	Security         bool
}

// ReportFilter constrains ListHostReports. A zero ChangedSince lists every
// host (including retired ones, flagged by Host.Status). Digest selects the
// digest view. Limit 0 uses the server default (100, max 200).
type ReportFilter struct {
	ChangedSince time.Time
	Digest       bool
	Limit        int
	Cursor       string
}

// ListReportTenants returns the tenants having at least one host whose report
// changed after changedSince (zero = every tenant with hosts) and the highest
// change time seen, to be used as the next watermark. Only services listed in
// inventory's host_reports.consumers may call it.
func (c *Client) ListReportTenants(ctx context.Context, changedSince time.Time) (ids []string, maxChanged time.Time, err error) {
	resp, err := c.reports.ListReportTenants(ctx, &invv1.ListReportTenantsRequest{ChangedSince: unixMilli(changedSince)})
	if err != nil {
		return nil, time.Time{}, err
	}
	return resp.GetTenantIds(), msTime(resp.GetMaxChangedAt()), nil
}

// ListHostReports returns one page of a tenant's host reports ordered by
// (change time, host id) and the cursor of the next page ("" = last page).
func (c *Client) ListHostReports(ctx context.Context, tenantID string, f ReportFilter) (reports []HostReport, next string, err error) {
	view := invv1.HostReportView_HOST_REPORT_VIEW_FULL
	if f.Digest {
		view = invv1.HostReportView_HOST_REPORT_VIEW_DIGEST
	}
	resp, err := c.reports.ListHostReports(ctx, &invv1.ListHostReportsRequest{
		TenantId: tenantID, ChangedSince: unixMilli(f.ChangedSince), View: view,
		Limit: int32(min(max(f.Limit, 0), 1<<20)), Cursor: f.Cursor, // #nosec G115 -- clamped to [0, 2^20]
	})
	if err != nil {
		return nil, "", err
	}
	out := make([]HostReport, 0, len(resp.GetReports()))
	for _, r := range resp.GetReports() {
		out = append(out, toHostReport(r))
	}
	return out, resp.GetNextCursor(), nil
}

// GetHostReport returns the latest report of one host.
func (c *Client) GetHostReport(ctx context.Context, tenantID, hostID string) (HostReport, error) {
	r, err := c.reports.GetHostReport(ctx, &invv1.GetHostReportRequest{TenantId: tenantID, HostId: hostID})
	if err != nil {
		return HostReport{}, err
	}
	return toHostReport(r), nil
}

func toHostReport(r *invv1.HostReport) HostReport {
	out := HostReport{
		TenantID: r.GetTenantId(), SnapshotID: r.GetSnapshotId(), CollectedAt: unixTime(r.GetCollectedAt()),
		ChangedAt: msTime(r.GetReportChangedAt()), Digest: r.GetReportDigest(), AgentVersion: r.GetAgentVersion(),
		OSFamily: r.GetOsFamily(), Interfaces: toInterfaces(r.GetNetworkInterfaces()),
		PrimaryIPv4: r.GetPrimaryIpv4(), PrimaryIPv6: r.GetPrimaryIpv6(),
		Virtualization: toVirtualization(r.GetVirtualization()), BMC: toBMC(r.GetBmc()),
		Guests: toGuests(r.GetHypervisorGuests()), Updates: toUpdateState(r.GetUpdateState()),
		Truncated: toLimits(r.GetTruncated()),
	}
	if h := r.GetHost(); h != nil {
		out.Host = toHost(h)
	}
	for _, p := range r.GetPendingUpdates() {
		out.PendingUpdates = append(out.PendingUpdates, PendingUpdate{
			Name: p.GetName(), InstalledVersion: p.GetInstalledVersion(),
			AvailableVersion: p.GetAvailableVersion(), Security: p.GetSecurity(),
		})
	}
	return out
}

func unixMilli(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}

func msTime(ms int64) time.Time {
	if ms == 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
