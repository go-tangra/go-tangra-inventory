package httpapi

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-tangra/go-tangra/v4/listquery"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

// Agent self-upgrade routes (feature 023, contracts/inventory-http.md). The
// gateway enforces the declared permissions (agents:manage; the policy and
// version pins need agentupgrades:manage); the tenant always comes from the
// caller's verified token.

// Upgrade request bodies.
const (
	maxUpgradeRequestBytes = 64 << 10
	maxCancelBytes         = 1 << 10
	maxPolicyBytes         = 4 << 10
)

// failUpgrade maps upgrade errors to the closed error vocabulary; details
// are never returned.
func failUpgrade(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, upgrades.ErrInvalid):
		WriteError(w, http.StatusBadRequest, "invalid_argument")
	case errors.Is(err, upgrades.ErrNoRelease):
		WriteError(w, http.StatusConflict, "no_release_available")
	case errors.Is(err, upgrades.ErrNotCancellable):
		WriteError(w, http.StatusConflict, "not_cancellable")
	case errors.Is(err, upgrades.ErrUnknownVersion):
		WriteError(w, http.StatusConflict, "unknown_version")
	case errors.Is(err, repo.ErrNotFound):
		WriteError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, ErrUnauthenticated):
		WriteError(w, http.StatusUnauthorized, "unauthenticated")
	default:
		WriteError(w, ErrUnavailable.Status, ErrUnavailable.Reason)
	}
}

// userActor is the upgrade actor of the verified caller.
func userActor(r *http.Request) (upgrades.Actor, string, error) {
	subj, err := subjects(r)
	if err != nil {
		return upgrades.Actor{}, "", err
	}
	return upgrades.Actor{Kind: upgrades.ActorUser, ID: subj.UserID}, subj.TenantID, nil
}

type releasePlatform struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	InstallType string `json:"install_type"`
	Size        int64  `json:"size"`
}

type releaseInfo struct {
	Version    string            `json:"version"`
	Source     string            `json:"source"`
	KeyID      string            `json:"key_id"`
	ImportedAt time.Time         `json:"imported_at"`
	Platforms  []releasePlatform `json:"platforms"`
}

func (s *Server) registerUpgrades(d Deps, p string) {
	u := d.Upgrades
	s.MustHandle("GET", p+"/agents/{id}", func(w http.ResponseWriter, r *http.Request) {
		_, tenant, err := userActor(r)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		e, recent, err := u.Agent(r.Context(), tenant, r.PathValue("id"))
		if err != nil {
			failUpgrade(w, err)
			return
		}
		if recent == nil {
			recent = []store.AgentUpgrade{}
		}
		WriteJSON(w, http.StatusOK, struct {
			upgrades.FleetEntry
			RecentUpgrades []store.AgentUpgrade `json:"recent_upgrades"`
		}{e, recent})
	})
	s.MustHandle("GET", p+"/agents/upgrades", func(w http.ResponseWriter, r *http.Request) {
		_, tenant, err := userActor(r)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		q := r.URL.Query()
		limit := clampLimit(q.Get("limit"), 100)
		items, err := u.List(r.Context(), tenant, repo.UpgradeFilter{State: q.Get("state"), AgentID: q.Get("agent_id"), CursorID: q.Get("cursor"), Limit: limit + 1})
		if err != nil {
			failUpgrade(w, err)
			return
		}
		next := ""
		if len(items) > limit {
			items = items[:limit]
			next = items[limit-1].ID
		}
		if items == nil {
			items = []store.AgentUpgrade{}
		}
		WriteJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
	})
	s.MustHandle("POST", p+"/agents/upgrades", func(w http.ResponseWriter, r *http.Request) {
		actor, tenant, err := userActor(r)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		var in struct {
			AgentIDs    []string `json:"agent_ids"`
			AllOutdated *bool    `json:"all_outdated"`
		}
		if err := DecodeJSON(r, &in, maxUpgradeRequestBytes); err != nil {
			Fail(w, r, nil, err)
			return
		}
		all := in.AllOutdated != nil && *in.AllOutdated
		if (in.AllOutdated != nil && !all) || uniqueCount(in.AgentIDs) != len(in.AgentIDs) {
			failUpgrade(w, upgrades.ErrInvalid)
			return
		}
		res, err := u.Request(r.Context(), tenant, actor, in.AgentIDs, all)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		WriteJSON(w, http.StatusAccepted, res)
	})
	s.MustHandle("POST", p+"/agents/upgrades/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		actor, tenant, err := userActor(r)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		if r.ContentLength > maxCancelBytes {
			WriteError(w, ErrBodyTooLarge.Status, ErrBodyTooLarge.Reason)
			return
		}
		res, err := u.Cancel(r.Context(), tenant, actor, r.PathValue("id"))
		if err != nil {
			failUpgrade(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, res)
	})
	s.MustHandle("GET", p+"/agents/upgrade-policy", func(w http.ResponseWriter, r *http.Request) {
		_, tenant, err := userActor(r)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		pol, err := u.GetPolicy(r.Context(), tenant)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, pol)
	})
	s.MustHandle("PUT", p+"/agents/upgrade-policy", func(w http.ResponseWriter, r *http.Request) {
		actor, tenant, err := userActor(r)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		// Every editable field is required; read-only fields are unknown here.
		var in struct {
			Enabled       *bool   `json:"enabled"`
			WindowStart   *string `json:"window_start"`
			WindowEnd     *string `json:"window_end"`
			Timezone      *string `json:"timezone"`
			MaxConcurrent *int    `json:"max_concurrent"`
			TargetVersion *string `json:"target_version"`
		}
		if err := DecodeJSON(r, &in, maxPolicyBytes); err != nil {
			Fail(w, r, nil, err)
			return
		}
		if in.Enabled == nil || in.WindowStart == nil || in.WindowEnd == nil || in.Timezone == nil || in.MaxConcurrent == nil || in.TargetVersion == nil {
			failUpgrade(w, upgrades.ErrInvalid)
			return
		}
		pol, err := u.UpdatePolicy(r.Context(), tenant, actor, upgrades.PolicyInput{Enabled: *in.Enabled, WindowStart: *in.WindowStart,
			WindowEnd: *in.WindowEnd, Timezone: *in.Timezone, MaxConcurrent: *in.MaxConcurrent, TargetVersion: *in.TargetVersion})
		if err != nil {
			failUpgrade(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, pol)
	})
	s.MustHandle("POST", p+"/agents/upgrade-policy/resume", func(w http.ResponseWriter, r *http.Request) {
		actor, tenant, err := userActor(r)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		if r.ContentLength > maxCancelBytes {
			WriteError(w, ErrBodyTooLarge.Status, ErrBodyTooLarge.Reason)
			return
		}
		pol, err := u.ResumePolicy(r.Context(), tenant, actor)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, pol)
	})
	s.MustHandle("GET", p+"/agent-releases", func(w http.ResponseWriter, r *http.Request) {
		if _, _, err := userActor(r); err != nil {
			failUpgrade(w, err)
			return
		}
		rels, err := d.Releases.Releases(r.Context())
		if err != nil {
			failUpgrade(w, err)
			return
		}
		items := []releaseInfo{}
		for _, rel := range rels {
			info := releaseInfo{Version: rel.Version, Source: rel.Source, KeyID: rel.KeyID, ImportedAt: rel.ImportedAt, Platforms: []releasePlatform{}}
			for _, a := range rel.Artifacts {
				info.Platforms = append(info.Platforms, releasePlatform{OS: a.OS, Arch: a.Arch, InstallType: a.InstallType, Size: a.Size})
			}
			items = append(items, info)
		}
		WriteJSON(w, http.StatusOK, map[string]any{"current_version": d.Releases.CurrentVersion(r.Context()), "items": items})
	})
}

// listFleet serves GET /agents from the fleet view.
func (s *Server) listFleet(u *upgrades.Service) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		_, tenant, err := userActor(r)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		q := r.URL.Query()
		f := upgrades.FleetFilter{State: q.Get("state"), Outdated: q.Get("outdated") == "true"}
		if v := q.Get("online"); v != "" {
			b := v == "true"
			f.Online = &b
		}
		if listquery.Legacy(q) {
			// Legacy cursor/limit (kept one release): the id-ordered page in
			// its previous shape, plus the total.
			count, _, err := u.FleetPage(r.Context(), tenant, f, listquery.Request{PageSize: 1})
			if err != nil {
				failUpgrade(w, err)
				return
			}
			f.Cursor, f.Limit = q.Get("cursor"), legacyLimit(q)
			items, current, err := u.Fleet(r.Context(), tenant, f)
			if err != nil {
				failUpgrade(w, err)
				return
			}
			WriteJSON(w, http.StatusOK, map[string]any{"items": items, "current_version": current, "total": count.Total})
			return
		}
		req, ok := parseList(w, r, store.FleetList)
		if !ok {
			return
		}
		page, current, err := u.FleetPage(r.Context(), tenant, f, req)
		if err != nil {
			failUpgrade(w, err)
			return
		}
		WriteJSON(w, http.StatusOK, fleetPage{Page: page, CurrentVersion: current})
	}
}

// fleetPage is the GET /agents response: a list contract page plus the
// tenant target version.
type fleetPage struct {
	listquery.Page[upgrades.FleetEntry]
	CurrentVersion string `json:"current_version"`
}

func uniqueCount(ids []string) int {
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	return len(seen)
}
