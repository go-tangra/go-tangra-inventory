package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/certdelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Host certificates and certificate delivery history (feature 033 US4,
// contracts/inventory-http.md). The gateway enforces inventory:read on the
// reads and agents:manage on cancel; the tenant always comes from the
// verified token. Responses carry identity only (ids, serial, fingerprint,
// common name, expiry), never certificate or key material. Reads work while
// cert_delivery.enabled is false; cancel then answers 503.

// maxCertCancelBytes bounds the (empty) cancel body.
const maxCertCancelBytes = 1 << 10

// failCert maps certificate delivery errors to the closed vocabulary.
func failCert(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, certdelivery.ErrDisabled):
		WriteError(w, http.StatusServiceUnavailable, "certificate_delivery_disabled")
	case errors.Is(err, certdelivery.ErrNotCancellable):
		WriteError(w, http.StatusConflict, "not_cancellable")
	case errors.Is(err, repo.ErrNotFound), errors.Is(err, hosts.ErrNotFound):
		WriteError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "unauthenticated")
	default:
		WriteError(w, ErrUnavailable.Status, ErrUnavailable.Reason)
	}
}

// certItemJSON is a CertificateDeliveryItem (OpenAPI, closed schema).
type certItemJSON struct {
	ID                string     `json:"id"`
	DeliveryID        string     `json:"delivery_id"`
	HostID            string     `json:"host_id"`
	Hostname          string     `json:"hostname"`
	Name              string     `json:"name"`
	CertificateID     string     `json:"certificate_id"`
	ConfigurationID   string     `json:"configuration_id"`
	Trigger           string     `json:"trigger"`
	State             string     `json:"state"`
	Reason            string     `json:"reason"`
	Attempts          int        `json:"attempts"`
	Serial            string     `json:"serial"`
	FingerprintSHA256 string     `json:"fingerprint_sha256"`
	CommonName        string     `json:"common_name"`
	NotAfter          *time.Time `json:"not_after"`
	HookExitCode      *int       `json:"hook_exit_code"`
	Detail            string     `json:"detail"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	FinishedAt        *time.Time `json:"finished_at"`
}

func certItem(h certdelivery.HistoryItem) certItemJSON {
	i := h.CertDeliveryItem
	return certItemJSON{ID: i.ID, DeliveryID: i.DeliveryID, HostID: i.HostID, Hostname: h.Hostname, Name: i.Name,
		CertificateID: i.CertificateID, ConfigurationID: h.Delivery.ConfigurationID, Trigger: h.Delivery.Trigger, State: i.State,
		Reason: i.Reason, Attempts: i.Attempts, Serial: i.Serial, FingerprintSHA256: i.FingerprintSHA256, CommonName: i.CommonName,
		NotAfter: i.NotAfter, HookExitCode: i.HookExitCode, Detail: i.Detail, CreatedAt: i.CreatedAt, UpdatedAt: i.UpdatedAt,
		FinishedAt: i.FinishedAt}
}

// certDeliveryJSON is a CertificateDelivery (without the host selection).
type certDeliveryJSON struct {
	ID              string    `json:"id"`
	Source          string    `json:"source"`
	ConfigurationID string    `json:"configuration_id"`
	TargetID        string    `json:"target_id"`
	Trigger         string    `json:"trigger"`
	CertificateID   string    `json:"certificate_id"`
	Name            string    `json:"name"`
	KeyPolicy       string    `json:"key_policy"`
	RequestedBy     string    `json:"requested_by"`
	CreatedAt       time.Time `json:"created_at"`
	ExpiresAt       time.Time `json:"expires_at"`
}

// hostCertJSON is a HostCertificate (OpenAPI, closed schema).
type hostCertJSON struct {
	Name              string        `json:"name"`
	CertificateID     string        `json:"certificate_id"`
	ConfigurationID   string        `json:"configuration_id"`
	CommonName        string        `json:"common_name"`
	Serial            string        `json:"serial"`
	FingerprintSHA256 string        `json:"fingerprint_sha256"`
	NotAfter          *time.Time    `json:"not_after"`
	State             string        `json:"state"`
	Reason            string        `json:"reason"`
	HookExitCode      *int          `json:"hook_exit_code"`
	LastDeliveredAt   *time.Time    `json:"last_delivered_at"`
	Revoked           bool          `json:"revoked"`
	RevokedAt         *time.Time    `json:"revoked_at"`
	ActiveItem        *certItemJSON `json:"active_item"`
}

func hostCert(r certdelivery.HostCertRow) hostCertJSON {
	out := hostCertJSON{Name: r.Name, CertificateID: r.CertificateID, ConfigurationID: r.ConfigurationID, CommonName: r.CommonName,
		Serial: r.Serial, FingerprintSHA256: r.FingerprintSHA256, NotAfter: r.NotAfter, State: r.State, Reason: r.Reason,
		HookExitCode: r.HookExitCode, LastDeliveredAt: r.LastDeliveredAt, Revoked: r.RevokedAt != nil, RevokedAt: r.RevokedAt}
	if r.Active != nil {
		a := certItem(*r.Active)
		out.ActiveItem = &a
	}
	return out
}

// mapPage converts a page's items keeping its list contract fields.
func mapPage[T, U any](p listquery.Page[T], f func(T) U) listquery.Page[U] {
	items := make([]U, 0, len(p.Items))
	for _, it := range p.Items {
		items = append(items, f(it))
	}
	return listquery.Page[U]{Items: items, Total: p.Total, Page: p.Page, PageSize: p.PageSize, Sort: p.Sort, Order: p.Order}
}

func (s *Server) registerCertificates(d Deps, p string) {
	c := d.CertDelivery
	s.MustHandle("GET", p+"/hosts/{id}/certificates", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failCert(w, err)
			return
		}
		req, ok := parseList(w, r, store.HostCertList)
		if !ok {
			return
		}
		hostID := r.PathValue("id")
		if _, err := d.Hosts.Get(r.Context(), subj, hostID); err != nil {
			failCert(w, err)
			return
		}
		q := r.URL.Query()
		f := certdelivery.HostCertFilter{State: q.Get("state")}
		if v := q.Get("revoked"); v != "" {
			b := v == "true"
			f.Revoked = &b
		}
		page, err := c.HostCertificatePage(r.Context(), subj.TenantID, hostID, f, req)
		if err != nil {
			failCert(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, mapPage(page, hostCert))
	})
	s.MustHandle("GET", p+"/certificate-deliveries", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failCert(w, err)
			return
		}
		req, ok := parseList(w, r, store.CertItemList)
		if !ok {
			return
		}
		q := r.URL.Query()
		f := repo.CertItemFilter{HostID: q.Get("host_id"), State: q.Get("state"), Name: q.Get("name"),
			CertificateID: q.Get("certificate_id"), DeliveryID: q.Get("delivery_id")}
		page, err := c.History(r.Context(), subj.TenantID, f, req)
		if err != nil {
			failCert(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, mapPage(page, certItem))
	})
	s.MustHandle("GET", p+"/certificate-deliveries/{item_id}", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failCert(w, err)
			return
		}
		h, err := c.Item(r.Context(), subj.TenantID, r.PathValue("item_id"))
		if err != nil {
			failCert(w, err)
			return
		}
		dl := h.Delivery
		WriteJSON(w, http.StatusOK, struct {
			Item     certItemJSON     `json:"item"`
			Delivery certDeliveryJSON `json:"delivery"`
		}{certItem(h), certDeliveryJSON{ID: dl.ID, Source: dl.Source, ConfigurationID: dl.ConfigurationID, TargetID: dl.TargetID,
			Trigger: dl.Trigger, CertificateID: dl.CertificateID, Name: dl.Name, KeyPolicy: dl.KeyPolicy, RequestedBy: dl.RequestedBy,
			CreatedAt: dl.CreatedAt, ExpiresAt: dl.ExpiresAt}})
	})
	s.MustHandle("POST", p+"/certificate-deliveries/{item_id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failCert(w, err)
			return
		}
		if r.ContentLength > maxCertCancelBytes {
			WriteError(w, ErrBodyTooLarge.Status, ErrBodyTooLarge.Reason)
			return
		}
		id := r.PathValue("item_id")
		actor := certdelivery.Actor{Kind: audit.ActorUser, ID: subj.UserID}
		if _, err := c.Cancel(r.Context(), subj.TenantID, actor, id); err != nil {
			failCert(w, err)
			return
		}
		h, err := c.Item(r.Context(), subj.TenantID, id)
		if err != nil {
			failCert(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, certItem(h))
	})
}
