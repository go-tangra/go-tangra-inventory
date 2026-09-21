// Package snapshots is the inventory snapshot service: it ingests collected
// inventory payloads (resolving/creating the host, persisting the immutable
// snapshot, computing the change history against the host's previous snapshot,
// and publishing realtime events) and serves snapshots and their diffs back to
// callers. A snapshot's full payload is a detail-only projection: List returns
// a payload-free summary.
package snapshots

import (
	"context"
	"errors"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/authz"
	"github.com/go-freya/freya/services/inventory/internal/diff"
	"github.com/go-freya/freya/services/inventory/internal/events"
	"github.com/go-freya/freya/services/inventory/internal/hosts"
	"github.com/go-freya/freya/services/inventory/internal/repo"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

// ErrNotFound is returned when a snapshot does not exist within the caller's
// tenant.
var ErrNotFound = errors.New("snapshots: not found")

// Service ingests and serves snapshots.
type Service struct {
	st    repo.Store
	hosts *hosts.Service
	pub   events.Publisher
	now   func() time.Time
}

// New builds the service.
func New(st repo.Store, hostsSvc *hosts.Service, pub events.Publisher) *Service {
	return &Service{st: st, hosts: hostsSvc, pub: pub, now: time.Now}
}

// SetClock injects the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// View is the JSON projection of a snapshot. Payload is present only in the
// full-detail projection (Get / GetLatestForHost); List omits it.
type View struct {
	ID           string           `json:"id"`
	TenantID     string           `json:"tenant_id"`
	HostID       string           `json:"host_id"`
	CollectedAt  time.Time        `json:"collected_at"`
	ReceivedAt   time.Time        `json:"received_at"`
	AgentVersion string           `json:"agent_version,omitempty"`
	Source       string           `json:"source"`
	OSName       string           `json:"os_name,omitempty"`
	OSVersion    string           `json:"os_version,omitempty"`
	Manufacturer string           `json:"manufacturer,omitempty"`
	Model        string           `json:"model,omitempty"`
	Payload      *store.Inventory `json:"payload,omitempty"`
}

// detailView projects a snapshot including its full payload.
func detailView(s store.Snapshot) View {
	v := summaryView(s)
	p := s.Payload
	v.Payload = &p
	return v
}

// summaryView projects a snapshot without its payload (list/summary contexts).
func summaryView(s store.Snapshot) View {
	return View{
		ID: s.ID, TenantID: s.TenantID, HostID: s.HostID, CollectedAt: s.CollectedAt,
		ReceivedAt: s.ReceivedAt, AgentVersion: s.AgentVersion, Source: s.Source,
		OSName: s.OSName, OSVersion: s.OSVersion, Manufacturer: s.Manufacturer, Model: s.Model,
	}
}

// Get returns a snapshot with its full payload.
func (s *Service) Get(ctx context.Context, subj authz.Subjects, id string) (View, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return View{}, err
	}
	snap, err := s.st.GetSnapshot(ctx, subj.TenantID, id)
	if err != nil {
		return View{}, mapNF(err)
	}
	return detailView(snap), nil
}

// GetLatestForHost returns a host's most recent snapshot with its full payload.
func (s *Service) GetLatestForHost(ctx context.Context, subj authz.Subjects, hostID string) (View, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return View{}, err
	}
	snap, err := s.st.GetLatestForHost(ctx, subj.TenantID, hostID)
	if err != nil {
		return View{}, mapNF(err)
	}
	return detailView(snap), nil
}

// ListForHost returns a host's snapshots newest-first as payload-free summaries.
func (s *Service) ListForHost(ctx context.Context, subj authz.Subjects, hostID string, limit int, cursorID string) ([]View, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return nil, err
	}
	rows, err := s.st.ListSnapshotsForHost(ctx, subj.TenantID, hostID, limit, cursorID)
	if err != nil {
		return nil, err
	}
	out := make([]View, 0, len(rows))
	for _, snap := range rows {
		out = append(out, summaryView(snap))
	}
	return out, nil
}

// Delete removes a snapshot.
func (s *Service) Delete(ctx context.Context, subj authz.Subjects, id string) error {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return err
	}
	return mapNF(s.st.DeleteSnapshot(ctx, subj.TenantID, id))
}

// Diff loads two snapshots and returns the component changes from A to B.
func (s *Service) Diff(ctx context.Context, subj authz.Subjects, snapAID, snapBID string) ([]store.Change, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return nil, err
	}
	a, err := s.st.GetSnapshot(ctx, subj.TenantID, snapAID)
	if err != nil {
		return nil, mapNF(err)
	}
	b, err := s.st.GetSnapshot(ctx, subj.TenantID, snapBID)
	if err != nil {
		return nil, mapNF(err)
	}
	return diff.Diff(a.Payload, b.Payload), nil
}

// ListChanges returns the recorded change history for a host, newest-first.
func (s *Service) ListChanges(ctx context.Context, subj authz.Subjects, hostID string) ([]store.Change, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return nil, err
	}
	return s.st.ListChangesForHost(ctx, subj.TenantID, hostID, 0)
}

func mapNF(err error) error {
	if errors.Is(err, repo.ErrNotFound) {
		return ErrNotFound
	}
	return err
}
