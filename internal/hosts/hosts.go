// Package hosts is the inventory host service: read, list, tag, retire, delete,
// and identity-resolution of managed endpoints. Authorization is coarse (the
// caller is scoped to its tenant); the concrete store enforces per-tenant RLS.
// Store not-found is masked into the package's own ErrNotFound; authorization
// failures surface as authz.ErrForbidden.
package hosts

import (
	"context"
	"errors"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/authz"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// ErrNotFound is returned when a host does not exist within the caller's tenant.
var ErrNotFound = errors.New("hosts: not found")

// Service manages hosts.
type Service struct {
	st  repo.Store
	now func() time.Time
}

// New builds the service.
func New(st repo.Store) *Service { return &Service{st: st, now: time.Now} }

// SetClock injects the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// View is the JSON projection of a host. Hosts carry no secrets, so every field
// is safe to return.
type View struct {
	ID             string            `json:"id"`
	TenantID       string            `json:"tenant_id"`
	Hostname       string            `json:"hostname"`
	MachineID      string            `json:"machine_id,omitempty"`
	HardwareUUID   string            `json:"hardware_uuid,omitempty"`
	SystemSerial   string            `json:"system_serial,omitempty"`
	IdentityKey    string            `json:"identity_key,omitempty"`
	Manufacturer   string            `json:"manufacturer,omitempty"`
	Model          string            `json:"model,omitempty"`
	OSName         string            `json:"os_name,omitempty"`
	OSVersion      string            `json:"os_version,omitempty"`
	OSArch         string            `json:"os_arch,omitempty"`
	AgentVersion   string            `json:"agent_version,omitempty"`
	AssignedUser   string            `json:"assigned_user,omitempty"`
	Status         string            `json:"status"`
	Tags           map[string]string `json:"tags,omitempty"`
	FirstSeen      time.Time         `json:"first_seen"`
	LastSeen       time.Time         `json:"last_seen"`
	LastSnapshotID string            `json:"last_snapshot_id,omitempty"`
	CreatedAt      time.Time         `json:"created_at"`
	UpdatedAt      time.Time         `json:"updated_at"`
}

// view projects a stored host.
func view(h store.Host) View {
	return View{
		ID: h.ID, TenantID: h.TenantID, Hostname: h.Hostname, MachineID: h.MachineID,
		HardwareUUID: h.HardwareUUID, SystemSerial: h.SystemSerial, IdentityKey: h.IdentityKey,
		Manufacturer: h.Manufacturer, Model: h.Model, OSName: h.OSName, OSVersion: h.OSVersion,
		OSArch: h.OSArch, AgentVersion: h.AgentVersion, AssignedUser: h.AssignedUser, Status: h.Status,
		Tags: h.Tags, FirstSeen: h.FirstSeen, LastSeen: h.LastSeen, LastSnapshotID: h.LastSnapshotID,
		CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}

// Get returns one host in the caller's tenant.
func (s *Service) Get(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return View{}, err
	}
	h, err := s.st.GetHost(ctx, subj.TenantID, id)
	if err != nil {
		return View{}, mapNF(err)
	}
	return view(h), nil
}

// List returns the caller's hosts matching f.
func (s *Service) List(ctx context.Context, subj authz.Subjects, f store.HostFilter) ([]View, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return nil, err
	}
	rows, err := s.st.ListHosts(ctx, subj.TenantID, f)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(rows))
	for _, h := range rows {
		out = append(out, view(h))
	}
	return out, nil
}

// SetTags replaces a host's tag set and returns the updated host.
func (s *Service) SetTags(ctx context.Context, subj authz.Subjects, id string, tags map[string]string) (View, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return View{}, err
	}
	if err := s.st.SetHostTags(ctx, subj.TenantID, id, tags); err != nil {
		return View{}, mapNF(err)
	}
	h, err := s.st.GetHost(ctx, subj.TenantID, id)
	if err != nil {
		return View{}, mapNF(err)
	}
	return view(h), nil
}

// Retire marks a host retired (kept for history) and returns it.
func (s *Service) Retire(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return View{}, err
	}
	if err := s.st.RetireHost(ctx, subj.TenantID, id); err != nil {
		return View{}, mapNF(err)
	}
	h, err := s.st.GetHost(ctx, subj.TenantID, id)
	if err != nil {
		return View{}, mapNF(err)
	}
	return view(h), nil
}

// Delete removes a host and cascades its snapshots and changes.
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) error {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return err
	}
	return mapNF(s.st.DeleteHost(ctx, subj.TenantID, id))
}

// Resolve upserts a host by its reported identity within tenantID and returns
// the stored row. It is the ingest path's entry point and takes no subject: the
// caller (snapshot ingest) has already authenticated the agent/tenant.
func (s *Service) Resolve(ctx context.Context, tenantID string, h store.Host) (store.Host, error) {
	if tenantID == "" {
		return store.Host{}, authz.ErrForbidden
	}
	h.TenantID = tenantID
	resolved, err := s.st.ResolveHost(ctx, tenantID, h)
	if err != nil {
		return store.Host{}, mapNF(err)
	}
	return resolved, nil
}

func mapNF(err error) error {
	if errors.Is(err, repo.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
