package certdelivery

import (
	"context"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Read views of the inventory UI (US4, contracts/inventory-http.md). They
// work while the relay is disabled and never carry material.

// HistoryItem is a delivery item with its hostname and its delivery
// (configuration, trigger, requested by).
type HistoryItem struct {
	store.CertDeliveryItem
	Hostname string
	Delivery store.CertDelivery
}

// History pages the tenant's delivery items (filters and order of
// store.CertItemList) with their hostnames and deliveries.
func (s *Service) History(ctx context.Context, tenantID string, f repo.CertItemFilter, req listquery.Request) (listquery.Page[HistoryItem], error) {
	items, total, applied, err := s.repo.ListCertItemsPage(ctx, tenantID, f, req)
	if err != nil {
		return listquery.Page[HistoryItem]{}, err
	}
	out, err := s.history(ctx, tenantID, items)
	if err != nil {
		return listquery.Page[HistoryItem]{}, err
	}
	return listquery.NewPage(out, total, applied), nil
}

// Item returns one of the tenant's items with its delivery; repo.ErrNotFound
// for another tenant's, a missing or a malformed id.
func (s *Service) Item(ctx context.Context, tenantID, id string) (HistoryItem, error) {
	if !uuidRE.MatchString(id) {
		return HistoryItem{}, repo.ErrNotFound
	}
	i, err := s.repo.GetCertItem(ctx, tenantID, id)
	if err != nil {
		return HistoryItem{}, err
	}
	out, err := s.history(ctx, tenantID, []store.CertDeliveryItem{i})
	if err != nil {
		return HistoryItem{}, err
	}
	return out[0], nil
}

// history joins items with their hostnames and deliveries (two batch reads).
func (s *Service) history(ctx context.Context, tenantID string, items []store.CertDeliveryItem) ([]HistoryItem, error) {
	out := make([]HistoryItem, 0, len(items))
	if len(items) == 0 {
		return out, nil
	}
	dIDs, hIDs := make([]string, 0, len(items)), make([]string, 0, len(items))
	for _, i := range items {
		dIDs, hIDs = append(dIDs, i.DeliveryID), append(hIDs, i.HostID)
	}
	ds, err := s.repo.ListCertDeliveriesByID(ctx, tenantID, dIDs)
	if err != nil {
		return nil, err
	}
	names, err := s.repo.HostnamesByID(ctx, tenantID, hIDs)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]store.CertDelivery, len(ds))
	for _, d := range ds {
		byID[d.ID] = d
	}
	for _, i := range items {
		out = append(out, HistoryItem{CertDeliveryItem: i, Hostname: names[i.HostID], Delivery: byID[i.DeliveryID]})
	}
	return out, nil
}

// HostCertRow is a host's certificate under one name with the item still
// waiting for the agent (nil: none). A name with only a queued item has the
// item's state, certificate and configuration and no installed identity.
type HostCertRow struct {
	store.HostCertificate
	Active *HistoryItem
}

// HostCertFilter constrains HostCertificatePage; zero values match all.
type HostCertFilter struct {
	State   string
	Revoked *bool // true: only revoked certificates, false: only the others
}

// activePageSize is the page size HostCertificatePage reads a host's active
// items with (a variable for tests).
var activePageSize = listquery.MaxPageSize

// HostCertificatePage pages a host's certificates (one row per name,
// store.HostCertList order). The caller checks that the host belongs to
// the tenant; another tenant's host has no rows.
func (s *Service) HostCertificatePage(ctx context.Context, tenantID, hostID string, f HostCertFilter, req listquery.Request) (listquery.Page[HostCertRow], error) {
	certs, err := s.repo.ListHostCertificates(ctx, tenantID, hostID)
	if err != nil {
		return listquery.Page[HostCertRow]{}, err
	}
	var active []store.CertDeliveryItem
	for page := 1; ; page++ {
		items, total, _, err := s.repo.ListCertItemsPage(ctx, tenantID, repo.CertItemFilter{HostID: hostID, Active: true},
			listquery.Request{Page: page, PageSize: activePageSize, Sort: "created_at", Order: listquery.Asc})
		if err != nil {
			return listquery.Page[HostCertRow]{}, err
		}
		active = append(active, items...)
		if len(items) == 0 || len(active) >= total {
			break
		}
	}
	queued, err := s.history(ctx, tenantID, active)
	if err != nil {
		return listquery.Page[HostCertRow]{}, err
	}
	byName := make(map[string]*HostCertRow, len(certs)+len(queued))
	rows := make([]*HostCertRow, 0, len(certs)+len(queued))
	for _, c := range certs {
		r := &HostCertRow{HostCertificate: c}
		byName[c.Name] = r
		rows = append(rows, r)
	}
	for _, q := range queued {
		r, ok := byName[q.Name]
		if !ok {
			r = &HostCertRow{HostCertificate: store.HostCertificate{TenantID: tenantID, HostID: hostID, Name: q.Name,
				CertificateID: q.CertificateID, ConfigurationID: q.Delivery.ConfigurationID, State: q.State,
				LastItemID: q.ID, UpdatedAt: q.UpdatedAt}}
			byName[q.Name] = r
			rows = append(rows, r)
		}
		r.Active = &q
	}
	out := make([]HostCertRow, 0, len(rows))
	for _, r := range rows {
		if (f.State == "" || r.State == f.State) && (f.Revoked == nil || (r.RevokedAt != nil) == *f.Revoked) {
			out = append(out, *r)
		}
	}
	req = store.ListRequest(req, store.HostCertList)
	listquery.SortSlice(out, req, hostCertKey, func(r HostCertRow) string { return r.Name })
	page, total, applied := listquery.Window(out, req)
	return listquery.NewPage(page, total, applied), nil
}

// hostCertKey is the value of a store.HostCertList sort field (a missing
// time sorts last).
func hostCertKey(r HostCertRow, field string) any {
	switch field {
	case "state":
		return r.State
	case "not_after":
		return timeKey(r.NotAfter)
	case "last_delivered_at":
		return timeKey(r.LastDeliveredAt)
	default:
		return r.Name
	}
}

func timeKey(t *time.Time) any {
	if t == nil {
		return nil
	}
	return *t
}
