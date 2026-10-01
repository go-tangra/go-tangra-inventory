package httpapi

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/backup"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

// Register mounts the inventory HTTP routes declared in the OpenAPI document.
// Every route resolves the caller from the gateway-forwarded platform token,
// derives a tenant-scoped subject, and delegates to a domain service. Agent
// credentials and enrollment secrets are never returned except the one-time
// secret from POST /agents/enroll-token.
func (s *Server) Register(d Deps) {
	p := "/api/inventory/v1"

	// ---- Hosts
	s.MustHandle("GET", p+"/hosts", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		q := r.URL.Query()
		f, bad := hostFilter(q)
		if bad != "" {
			writeParamError(w, bad)
			return
		}
		if listquery.Legacy(q) {
			// Legacy cursor/limit (kept one release): the id-DESC keyset page
			// in its previous shape, plus the total.
			f.Limit, f.CursorID = atoiDefault(q.Get("limit"), 0), q.Get("cursor")
			items, err := d.Hosts.List(r.Context(), subj, f)
			if err != nil {
				failSvc(w, err)
				return
			}
			count, err := d.Hosts.ListPage(r.Context(), subj, f, listquery.Request{PageSize: 1})
			if err != nil {
				failSvc(w, err)
				return
			}
			WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": count.Total})
			return
		}
		req, ok := parseList(w, r, store.HostList)
		if !ok {
			return
		}
		page, err := d.Hosts.ListPage(r.Context(), subj, f, req)
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, page)
	})
	s.MustHandle("GET", p+"/hosts/{id}", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		v, err := d.Hosts.Get(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	s.MustHandle("DELETE", p+"/hosts/{id}", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		if err := d.Hosts.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			failSvc(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.MustHandle("GET", p+"/hosts/{id}/latest", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		v, err := d.Snapshots.GetLatestForHost(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	s.MustHandle("POST", p+"/hosts/{id}/tags", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		var in struct {
			Tags map[string]string `json:"tags"`
		}
		if err := DecodeJSON(r, &in, 0); err != nil {
			Fail(w, r, nil, err)
			return
		}
		v, err := d.Hosts.SetTags(r.Context(), subj, r.PathValue("id"), in.Tags)
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	s.MustHandle("POST", p+"/hosts/{id}/retire", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		v, err := d.Hosts.Retire(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	s.MustHandle("GET", p+"/hosts/{id}/snapshots", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		q := r.URL.Query()
		hostID := r.PathValue("id")
		if listquery.Legacy(q) {
			// Legacy cursor/limit (kept one release), plus the total.
			items, err := d.Snapshots.ListForHost(r.Context(), subj, hostID, atoiDefault(q.Get("limit"), 0), q.Get("cursor"))
			if err != nil {
				failSvc(w, err)
				return
			}
			count, err := d.Snapshots.ListPageForHost(r.Context(), subj, hostID, listquery.Request{PageSize: 1})
			if err != nil {
				failSvc(w, err)
				return
			}
			WriteJSON(w, http.StatusOK, map[string]any{"items": items, "total": count.Total})
			return
		}
		req, ok := parseList(w, r, store.SnapshotList)
		if !ok {
			return
		}
		page, err := d.Snapshots.ListPageForHost(r.Context(), subj, hostID, req)
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, page)
	})
	s.MustHandle("GET", p+"/hosts/{id}/changes", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		q := r.URL.Query()
		if listquery.Legacy(q) {
			// Legacy limit (kept one release): the newest changes, now
			// honouring the limit, in the previous shape plus the total.
			page, err := d.Snapshots.ChangesPage(r.Context(), subj, r.PathValue("id"), listquery.Request{PageSize: legacyLimit(q)})
			if err != nil {
				failSvc(w, err)
				return
			}
			WriteJSON(w, http.StatusOK, map[string]any{"items": page.Items, "total": page.Total})
			return
		}
		req, ok := parseList(w, r, store.ChangeList)
		if !ok {
			return
		}
		page, err := d.Snapshots.ChangesPage(r.Context(), subj, r.PathValue("id"), req)
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, page)
	})

	// ---- Snapshots
	s.MustHandle("GET", p+"/snapshots/{id}", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		v, err := d.Snapshots.Get(r.Context(), subj, r.PathValue("id"))
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	s.MustHandle("DELETE", p+"/snapshots/{id}", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		if err := d.Snapshots.Delete(r.Context(), subj, r.PathValue("id")); err != nil {
			failSvc(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	s.MustHandle("GET", p+"/snapshots/{id}/diff/{other}", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		changes, err := d.Snapshots.Diff(r.Context(), subj, r.PathValue("id"), r.PathValue("other"))
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"changes": changes})
	})

	// ---- Agents
	if d.Upgrades != nil {
		s.MustHandle("GET", p+"/agents", s.listFleet(d.Upgrades))
		s.registerUpgrades(d, p)
	} else {
		s.MustHandle("GET", p+"/agents", s.listConnected(d))
	}
	if d.AutoEnroll != nil {
		s.registerAutoEnroll(d.AutoEnroll, p)
	}
	s.MustHandle("POST", p+"/agents/enroll-token", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		var in struct {
			Label      string `json:"label"`
			TTLSeconds int64  `json:"ttl_seconds"`
		}
		if r.ContentLength != 0 {
			if err := DecodeJSON(r, &in, 0); err != nil {
				Fail(w, r, nil, err)
				return
			}
		}
		ttl := time.Duration(in.TTLSeconds) * time.Second
		if ttl <= 0 {
			ttl = 24 * time.Hour
		}
		secret, tok, err := d.Enroll.MintToken(r.Context(), subj.TenantID, subj.ActorID(), in.Label, ttl)
		if err != nil {
			failSvc(w, err)
			return
		}
		// The plaintext secret is returned exactly once here; it is never stored.
		WriteJSON(w, http.StatusCreated, map[string]any{
			"id":         tok.ID,
			"token":      secret,
			"expires_at": tok.ExpiresAt,
			"label":      tok.Label,
		})
	})
	s.MustHandle("POST", p+"/agents/{host_id}/refresh", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		hostID := r.PathValue("host_id")
		agents, err := d.Registry.ListConnected(r.Context(), subj.TenantID)
		if err != nil {
			failSvc(w, err)
			return
		}
		cmd := registry.Command{ID: store.NewID(), Type: "refresh"}
		delivered := false
		for _, a := range agents {
			if a.HostID == hostID {
				ok, derr := d.Registry.Deliver(r.Context(), a.AgentID, cmd)
				if derr != nil {
					failSvc(w, derr)
					return
				}
				if ok {
					delivered = true
					break
				}
			}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"delivered": delivered, "command_id": cmd.ID})
	})
	s.MustHandle("POST", p+"/agents/{id}/revoke", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		if err := d.Enroll.RevokeAgent(r.Context(), subj.TenantID, r.PathValue("id")); err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"revoked": true})
	})

	// ---- Statistics
	s.MustHandle("GET", p+"/statistics/tenant", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		v, err := d.Stats.Tenant(r.Context(), subj)
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, v)
	})
	s.MustHandle("GET", p+"/statistics/system", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		v, err := d.Stats.System(r.Context(), subj)
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]any{"tenants": v})
	})

	// ---- Backup
	s.MustHandle("POST", p+"/backup/export", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		var in struct {
			IncludeHistory bool `json:"include_history"`
		}
		if r.ContentLength != 0 {
			if err := DecodeJSON(r, &in, 0); err != nil {
				Fail(w, r, nil, err)
				return
			}
		}
		b, err := d.Backup.Export(r.Context(), subj, in.IncludeHistory)
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, b)
	})
	s.MustHandle("POST", p+"/backup/import", func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		var in struct {
			Mode   string        `json:"mode"`
			Backup backup.Backup `json:"backup"`
		}
		if err := DecodeJSON(r, &in, MaxImportBytes); err != nil {
			Fail(w, r, nil, err)
			return
		}
		res, err := d.Backup.Import(r.Context(), subj, in.Backup, in.Mode)
		if err != nil {
			failSvc(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, res)
	})

	// ---- Realtime SSE stream (optional: only when a hub is wired).
	if d.Hub != nil {
		s.RegisterStream(d.Hub)
	}
}

// MaxImportBytes bounds a backup import body (matches the route's declared
// x-freya-max-body-bytes).
const MaxImportBytes = 256 << 20

// atoiDefault parses s as an int, returning def when s is empty or invalid.
func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

// listConnected serves GET /agents from the live registry (deployments
// without the upgrade service).
func (s *Server) listConnected(d Deps) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		subj, err := subjects(r)
		if err != nil {
			failSvc(w, err)
			return
		}
		q := r.URL.Query()
		var req listquery.Request
		if !listquery.Legacy(q) {
			var ok bool
			if req, ok = parseList(w, r, store.FleetList); !ok {
				return
			}
		}
		agents, err := d.Registry.ListConnected(r.Context(), subj.TenantID)
		if err != nil {
			failSvc(w, err)
			return
		}
		if agents == nil {
			agents = []registry.ConnectedAgent{}
		}
		if listquery.Legacy(q) {
			WriteJSON(w, http.StatusOK, map[string]any{"items": agents, "total": len(agents)})
			return
		}
		// Live connections carry no hostname or upgrade state: those sorts
		// fall back to the agent id.
		key := func(a registry.ConnectedAgent, field string) any {
			switch field {
			case "version":
				return upgrades.VersionSortKey(a.Version)
			case "last_seen":
				return a.ConnectedAt
			default:
				return a.AgentID
			}
		}
		listquery.SortSlice(agents, req, key, func(a registry.ConnectedAgent) string { return a.AgentID })
		page, total, applied := listquery.Window(agents, req)
		WriteJSON(w, http.StatusOK, listquery.NewPage(page, total, applied))
	}
}
