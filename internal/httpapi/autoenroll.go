package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/autoenroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Automatic enrollment administration (feature 029). Reads need
// agents:read, changes agents:manage (OpenAPI x-freya-permission). A key's
// secret is returned only by create and rotate.

type autoKeyView struct {
	ID             string     `json:"id"`
	KeyID          string     `json:"key_id"`
	Name           string     `json:"name"`
	AllowedCIDRs   []string   `json:"allowed_cidrs"`
	Enabled        bool       `json:"enabled"`
	State          string     `json:"state"` // active | disabled | expired | exhausted
	ExpiresAt      *time.Time `json:"expires_at"`
	MaxEnrollments int        `json:"max_enrollments"`
	Enrollments    int        `json:"enrollments"`
	LastUsedAt     *time.Time `json:"last_used_at"`
	LastUsedIP     string     `json:"last_used_ip"`
	CreatedBy      string     `json:"created_by"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

func viewAutoKey(k store.AutoEnrollKey, now time.Time) autoKeyView {
	state := "active"
	switch {
	case !k.Enabled:
		state = "disabled"
	case k.Expired(now):
		state = "expired"
	case k.Exhausted():
		state = "exhausted"
	}
	cidrs := k.AllowedCIDRs
	if cidrs == nil {
		cidrs = []string{}
	}
	return autoKeyView{ID: k.ID, KeyID: k.KeyID, Name: k.Name, AllowedCIDRs: cidrs, Enabled: k.Enabled, State: state, ExpiresAt: k.ExpiresAt,
		MaxEnrollments: k.MaxEnrollments, Enrollments: k.Enrollments, LastUsedAt: k.LastUsedAt, LastUsedIP: k.LastUsedIP,
		CreatedBy: k.CreatedBy, CreatedAt: k.CreatedAt, UpdatedAt: k.UpdatedAt}
}

// autoKeyKey is the value of a store.AutoEnrollKeyList sort field.
func autoKeyKey(k autoKeyView, field string) any {
	if field == "created_at" {
		return k.CreatedAt
	}
	return k.Name
}

// failAuto maps autoenroll errors; anything else goes to failSvc.
func failAuto(w http.ResponseWriter, err error) {
	var ie *autoenroll.InvalidError
	switch {
	case errors.As(err, &ie):
		WriteDetail(w, ErrValidation, map[string]any{"field": ie.Field, "message": ie.Msg})
	case errors.Is(err, autoenroll.ErrNotFound):
		WriteError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, autoenroll.ErrConflict):
		WriteDetail(w, ErrConflict, map[string]any{"field": "name", "message": "a key with this name exists"})
	default:
		failSvc(w, err)
	}
}

func (s *Server) registerAutoEnroll(svc *autoenroll.Service, p string) {
	actor := func(r *http.Request) (autoenroll.Actor, string, error) {
		subj, err := subjects(r)
		if err != nil {
			return autoenroll.Actor{}, "", err
		}
		return autoenroll.Actor{Kind: "user", ID: subj.UserID}, subj.TenantID, nil
	}
	s.MustHandle("GET", p+"/agents/auto-enroll", func(w http.ResponseWriter, r *http.Request) {
		_, tenant, err := actor(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		req, ok := parseList(w, r, store.AutoEnrollKeyList)
		if !ok {
			return
		}
		st, err := svc.Settings(r.Context(), tenant)
		if err != nil {
			failSvc(w, err)
			return
		}
		keys, err := svc.Keys(r.Context(), tenant)
		if err != nil {
			failSvc(w, err)
			return
		}
		now := time.Now()
		views := make([]autoKeyView, 0, len(keys))
		for _, k := range keys {
			views = append(views, viewAutoKey(k, now))
		}
		// The keys are a list contract page (store.AutoEnrollKeyList).
		listquery.SortSlice(views, req, autoKeyKey, func(k autoKeyView) string { return k.ID })
		page, total, applied := listquery.Window(views, req)
		var updated *time.Time
		if !st.UpdatedAt.IsZero() {
			updated = &st.UpdatedAt
		}
		WriteJSON(w, http.StatusOK, map[string]any{"enabled": st.Enabled, "updated_by": st.UpdatedBy, "updated_at": updated,
			"window_seconds": int(svc.Window().Seconds()), "keys": page,
			"total": total, "page": applied.Page, "page_size": applied.PageSize, "sort": applied.Sort, "order": applied.Order})
	})
	s.MustHandle("PUT", p+"/agents/auto-enroll", func(w http.ResponseWriter, r *http.Request) {
		a, tenant, err := actor(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		var in struct {
			Enabled *bool `json:"enabled"`
		}
		if err := DecodeJSON(r, &in, 0); err != nil {
			Fail(w, r, nil, err)
			return
		}
		if in.Enabled == nil {
			WriteDetail(w, ErrValidation, map[string]any{"field": "enabled", "message": "required"})
			return
		}
		st, err := svc.SetEnabled(r.Context(), tenant, a, *in.Enabled)
		if err != nil {
			failAuto(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"enabled": st.Enabled, "updated_by": st.UpdatedBy, "updated_at": st.UpdatedAt})
	})
	s.MustHandle("POST", p+"/agents/auto-enroll/keys", func(w http.ResponseWriter, r *http.Request) {
		a, tenant, err := actor(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		var in struct {
			Name           string     `json:"name"`
			AllowedCIDRs   []string   `json:"allowed_cidrs"`
			Enabled        *bool      `json:"enabled"`
			ExpiresAt      *time.Time `json:"expires_at"`
			MaxEnrollments int        `json:"max_enrollments"`
		}
		if err := DecodeJSON(r, &in, 0); err != nil {
			Fail(w, r, nil, err)
			return
		}
		k, secret, err := svc.CreateKey(r.Context(), tenant, a, autoenroll.KeyInput{Name: in.Name, AllowedCIDRs: in.AllowedCIDRs, Enabled: in.Enabled,
			ExpiresAt: in.ExpiresAt, MaxEnrollments: in.MaxEnrollments})
		if err != nil {
			failAuto(w, err)
			return
		}
		WriteJSON(w, http.StatusCreated, map[string]any{"key": viewAutoKey(k, time.Now()), "secret": secret})
	})
	s.MustHandle("PATCH", p+"/agents/auto-enroll/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		a, tenant, err := actor(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		var in struct {
			Name           *string         `json:"name"`
			AllowedCIDRs   *[]string       `json:"allowed_cidrs"`
			Enabled        *bool           `json:"enabled"`
			ExpiresAt      json.RawMessage `json:"expires_at"` // null clears
			MaxEnrollments *int            `json:"max_enrollments"`
		}
		if err := DecodeJSON(r, &in, 0); err != nil {
			Fail(w, r, nil, err)
			return
		}
		patch := autoenroll.KeyPatch{Name: in.Name, AllowedCIDRs: in.AllowedCIDRs, Enabled: in.Enabled, MaxEnrollments: in.MaxEnrollments}
		switch {
		case len(in.ExpiresAt) == 0:
		case bytes.Equal(bytes.TrimSpace(in.ExpiresAt), []byte("null")):
			patch.ClearExpiry = true
		default:
			var t time.Time
			if err := json.Unmarshal(in.ExpiresAt, &t); err != nil {
				WriteDetail(w, ErrValidation, map[string]any{"field": "expires_at", "message": "RFC 3339 time or null"})
				return
			}
			patch.ExpiresAt = &t
		}
		k, err := svc.UpdateKey(r.Context(), tenant, a, r.PathValue("id"), patch)
		if err != nil {
			failAuto(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, viewAutoKey(k, time.Now()))
	})
	s.MustHandle("POST", p+"/agents/auto-enroll/keys/{id}/rotate", func(w http.ResponseWriter, r *http.Request) {
		a, tenant, err := actor(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		k, secret, err := svc.RotateKey(r.Context(), tenant, a, r.PathValue("id"))
		if err != nil {
			failAuto(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"key": viewAutoKey(k, time.Now()), "secret": secret})
	})
	s.MustHandle("DELETE", p+"/agents/auto-enroll/keys/{id}", func(w http.ResponseWriter, r *http.Request) {
		a, tenant, err := actor(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		if err := svc.DeleteKey(r.Context(), tenant, a, r.PathValue("id")); err != nil {
			failAuto(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
