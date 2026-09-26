package grpcapi

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hostreport"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra/v4/identity"
)

// HostReportService bounds (contracts/inventory-grpc.md).
const (
	maxReportTenants   = 10000
	defaultReportLimit = 100
	maxReportLimit     = 200
	maxCursorLen       = 256
	cursorVersion      = "v1"
)

// HostReportServer implements inventory.v1.HostReportService: projections of
// each host's latest snapshot for IPAM. Every RPC requires a SPIFFE peer whose
// service name is listed in Consumers (host_reports.consumers), on top of the
// inbound mesh policy; ListReportTenants is cross-tenant (system scope), the
// others are scoped to the uuid tenant in the request.
type HostReportServer struct {
	invv1.UnimplementedHostReportServiceServer
	Store        repo.Store
	Consumers    []string
	MaxPageBytes int
}

// consumer admits only a SPIFFE peer of a configured consumer service.
func (s *HostReportServer) consumer(ctx context.Context) error {
	id, ok := callerFunc(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "service identity required")
	}
	name := serviceName(id)
	for _, c := range s.Consumers {
		if name != "" && c == name {
			return nil
		}
	}
	return status.Error(codes.PermissionDenied, "not a host report consumer")
}

// serviceName extracts <name> from a canonical spiffe://<td>/svc/<name>, or "".
func serviceName(id string) string {
	sid, err := identity.ParseSPIFFEID(id)
	if err != nil {
		return ""
	}
	return sid.ServiceName()
}

// ListReportTenants lists the tenants whose host reports changed after
// changed_since (0 = every tenant with hosts) and the next watermark.
func (s *HostReportServer) ListReportTenants(ctx context.Context, req *invv1.ListReportTenantsRequest) (*invv1.ListReportTenantsResponse, error) {
	if err := s.consumer(ctx); err != nil {
		return nil, err
	}
	if req.GetChangedSince() < 0 {
		return nil, status.Error(codes.InvalidArgument, "changed_since must be >= 0")
	}
	ids, maxAt, err := s.Store.ListReportTenants(ctx, msToTime(req.GetChangedSince()), maxReportTenants)
	if err != nil {
		return nil, grpcError(err)
	}
	return &invv1.ListReportTenantsResponse{TenantIds: ids, MaxChangedAt: timeToMs(maxAt)}, nil
}

// ListHostReports returns one page of a tenant's host reports ordered by
// (report_changed_at, host id), bounded by limit and MaxPageBytes.
func (s *HostReportServer) ListHostReports(ctx context.Context, req *invv1.ListHostReportsRequest) (*invv1.ListHostReportsResponse, error) {
	if err := s.consumer(ctx); err != nil {
		return nil, err
	}
	tenantID := req.GetTenantId()
	if !uuidRE.MatchString(tenantID) {
		return nil, status.Error(codes.InvalidArgument, "tenant_id must be a uuid")
	}
	limit := int(req.GetLimit())
	switch {
	case limit == 0:
		limit = defaultReportLimit
	case limit < 0 || limit > maxReportLimit:
		return nil, status.Error(codes.InvalidArgument, "limit must be within [1, 200]")
	}
	if req.GetChangedSince() < 0 {
		return nil, status.Error(codes.InvalidArgument, "changed_since must be >= 0")
	}
	digestOnly := false
	switch req.GetView() {
	case invv1.HostReportView_HOST_REPORT_VIEW_UNSPECIFIED, invv1.HostReportView_HOST_REPORT_VIEW_FULL:
	case invv1.HostReportView_HOST_REPORT_VIEW_DIGEST:
		digestOnly = true
	default:
		return nil, status.Error(codes.InvalidArgument, "unknown view")
	}
	var after *store.ReportCursor
	if c := req.GetCursor(); c != "" {
		cur, err := decodeCursor(c)
		if err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid cursor")
		}
		after = &cur
	}

	rows, err := s.Store.ListHostReportRows(ctx, tenantID, store.ReportRowFilter{
		ChangedSince: msToTime(req.GetChangedSince()), After: after, Limit: limit + 1,
	})
	if err != nil {
		return nil, grpcError(err)
	}
	out := &invv1.ListHostReportsResponse{}
	var last *store.ReportCursor // last row consumed (returned or skipped)
	budget, more := s.MaxPageBytes, len(rows) > limit
	for i, h := range rows {
		if i == limit {
			break
		}
		if h.TenantID != tenantID { // defence in depth: the store is tenant-scoped
			continue
		}
		r, err := s.report(ctx, h, digestOnly)
		if err != nil {
			return nil, err
		}
		if r != nil {
			size := proto.Size(r)
			if len(out.Reports) > 0 && size > budget {
				more = true
				break
			}
			budget -= size
			out.Reports = append(out.Reports, r)
		}
		last = &store.ReportCursor{ChangedAt: h.ReportChangedAt, ID: h.ID}
	}
	if more && last != nil {
		out.NextCursor = encodeCursor(*last)
	}
	return out, nil
}

// GetHostReport returns the latest report of one host.
func (s *HostReportServer) GetHostReport(ctx context.Context, req *invv1.GetHostReportRequest) (*invv1.HostReport, error) {
	if err := s.consumer(ctx); err != nil {
		return nil, err
	}
	if !uuidRE.MatchString(req.GetTenantId()) || !uuidRE.MatchString(req.GetHostId()) {
		return nil, status.Error(codes.InvalidArgument, "tenant_id and host_id must be uuids")
	}
	h, err := s.Store.GetHost(ctx, req.GetTenantId(), req.GetHostId())
	if err != nil {
		return nil, grpcError(err)
	}
	r, err := s.report(ctx, h, false)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, status.Error(codes.NotFound, "not_found")
	}
	return r, nil
}

// report builds the full or digest report of h; nil when the host has no
// snapshot (nothing to report). The stored digest is authoritative so the
// full and digest views agree; a host without one (never reported since
// migration 0005, or retired) gets the digest of its current projection.
func (s *HostReportServer) report(ctx context.Context, h store.Host, digestOnly bool) (*invv1.HostReport, error) {
	if digestOnly && h.ReportDigest != "" {
		return hostreport.DigestView(h), nil
	}
	snap, err := s.Store.GetLatestForHost(ctx, h.TenantID, h.ID)
	if errors.Is(err, repo.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, grpcError(err)
	}
	r := hostreport.Project(h, snap)
	if h.ReportDigest != "" {
		r.ReportDigest = h.ReportDigest
	}
	if digestOnly {
		d := hostreport.DigestView(h)
		d.ReportDigest = r.GetReportDigest()
		return d, nil
	}
	return r, nil
}

// encodeCursor makes the opaque keyset cursor "v1|<unix µs>|<host id>".
func encodeCursor(c store.ReportCursor) string {
	var us int64
	if !c.ChangedAt.IsZero() {
		us = c.ChangedAt.UnixMicro()
	}
	return base64.RawURLEncoding.EncodeToString([]byte(cursorVersion + "|" + strconv.FormatInt(us, 10) + "|" + c.ID))
}

func decodeCursor(s string) (store.ReportCursor, error) {
	bad := errors.New("bad cursor")
	if len(s) > maxCursorLen {
		return store.ReportCursor{}, bad
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return store.ReportCursor{}, bad
	}
	parts := strings.Split(string(raw), "|")
	if len(parts) != 3 || parts[0] != cursorVersion || !uuidRE.MatchString(parts[2]) {
		return store.ReportCursor{}, bad
	}
	us, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || us < 0 {
		return store.ReportCursor{}, bad
	}
	c := store.ReportCursor{ID: parts[2]}
	if us > 0 {
		c.ChangedAt = time.UnixMicro(us).UTC()
	}
	return c, nil
}

func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}

func timeToMs(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixMilli()
}
