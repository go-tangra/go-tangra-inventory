// Package backup exports and imports a tenant's inventory data (hosts, their
// snapshots, and — with history — the change log and tags) for backup or tenant
// migration. Agent credentials and enrollment tokens are NEVER exported: the
// backup carries only host inventory, so a restored tenant cannot impersonate an
// agent. Entity ids are preserved on import so snapshot/change references remain
// valid. Duplicate handling is per id: skip or overwrite.
package backup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/authz"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// SchemaVersion is the export format version.
const SchemaVersion = 1

// Import modes.
const (
	ModeSkip      = "skip"
	ModeOverwrite = "overwrite"
)

// ErrBadSchema is returned when importing an unsupported schema version.
var ErrBadSchema = errors.New("backup: unsupported schema version")

// HostExport is a host record in a backup. It carries no agent/enrollment
// secrets (there are none on a host row) — only identity, summary, tags, and
// lifecycle timestamps.
type HostExport struct {
	ID           string            `json:"id"`
	Hostname     string            `json:"hostname"`
	MachineID    string            `json:"machine_id,omitempty"`
	HardwareUUID string            `json:"hardware_uuid,omitempty"`
	SystemSerial string            `json:"system_serial,omitempty"`
	IdentityKey  string            `json:"identity_key,omitempty"`
	Manufacturer string            `json:"manufacturer,omitempty"`
	Model        string            `json:"model,omitempty"`
	OSName       string            `json:"os_name,omitempty"`
	OSVersion    string            `json:"os_version,omitempty"`
	OSArch       string            `json:"os_arch,omitempty"`
	AgentVersion string            `json:"agent_version,omitempty"`
	AssignedUser string            `json:"assigned_user,omitempty"`
	Status       string            `json:"status"`
	Tags         map[string]string `json:"tags,omitempty"`
	FirstSeen    time.Time         `json:"first_seen"`
	LastSeen     time.Time         `json:"last_seen"`
	CreatedAt    time.Time         `json:"created_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// SnapshotExport is a snapshot (with its full payload) in a backup.
type SnapshotExport struct {
	ID           string          `json:"id"`
	HostID       string          `json:"host_id"`
	CollectedAt  time.Time       `json:"collected_at"`
	ReceivedAt   time.Time       `json:"received_at"`
	AgentVersion string          `json:"agent_version,omitempty"`
	Source       string          `json:"source"`
	OSName       string          `json:"os_name,omitempty"`
	OSVersion    string          `json:"os_version,omitempty"`
	Manufacturer string          `json:"manufacturer,omitempty"`
	Model        string          `json:"model,omitempty"`
	Payload      store.Inventory `json:"payload"`
}

// ChangeExport is one change-history row in a backup.
type ChangeExport struct {
	ID             string    `json:"id"`
	HostID         string    `json:"host_id"`
	SnapshotID     string    `json:"snapshot_id"`
	PrevSnapshotID string    `json:"prev_snapshot_id,omitempty"`
	DetectedAt     time.Time `json:"detected_at"`
	Category       string    `json:"category"`
	ChangeType     string    `json:"change_type"`
	ComponentKey   string    `json:"component_key"`
	Before         string    `json:"before,omitempty"`
	After          string    `json:"after,omitempty"`
}

// Backup is the export document.
type Backup struct {
	SchemaVersion  int              `json:"schema_version"`
	ExportedAt     time.Time        `json:"exported_at"`
	IncludeHistory bool             `json:"include_history"`
	Hosts          []HostExport     `json:"hosts"`
	Snapshots      []SnapshotExport `json:"snapshots"`
	Changes        []ChangeExport   `json:"changes"`
}

// Result reports what an import did.
type Result struct {
	HostsImported     int `json:"hosts_imported"`
	HostsSkipped      int `json:"hosts_skipped"`
	SnapshotsImported int `json:"snapshots_imported"`
	SnapshotsSkipped  int `json:"snapshots_skipped"`
	ChangesImported   int `json:"changes_imported"`
}

// Service exports and imports tenant data.
type Service struct {
	st  repo.Store
	now func() time.Time
}

// New builds the service.
func New(st repo.Store) *Service { return &Service{st: st, now: time.Now} }

// SetClock injects the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Export builds a backup of the caller's tenant. When includeHistory is false
// only each host's latest snapshot is exported and the change log is omitted;
// when true, all snapshots and the full change history are exported. Agent
// credentials and enrollment tokens are never included.
func (s *Service) Export(ctx context.Context, subj authz.Subjects, includeHistory bool) (Backup, error) {
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return Backup{}, err
	}
	b := Backup{SchemaVersion: SchemaVersion, ExportedAt: s.now().UTC(), IncludeHistory: includeHistory}

	hosts, err := s.st.ListHosts(ctx, subj.TenantID, store.HostFilter{})
	if err != nil {
		return Backup{}, err
	}
	for _, h := range hosts {
		b.Hosts = append(b.Hosts, toHostExport(h))

		if includeHistory {
			snaps, err := s.st.ListSnapshotsForHost(ctx, subj.TenantID, h.ID, 0, "")
			if err != nil {
				return Backup{}, err
			}
			for _, snap := range snaps {
				b.Snapshots = append(b.Snapshots, toSnapshotExport(snap))
			}
			changes, err := s.st.ListChangesForHost(ctx, subj.TenantID, h.ID, 0)
			if err != nil {
				return Backup{}, err
			}
			for _, c := range changes {
				b.Changes = append(b.Changes, toChangeExport(c))
			}
			continue
		}

		// Latest-only: skip hosts that have never reported.
		latest, err := s.st.GetLatestForHost(ctx, subj.TenantID, h.ID)
		if errors.Is(err, repo.ErrNotFound) {
			continue
		}
		if err != nil {
			return Backup{}, err
		}
		b.Snapshots = append(b.Snapshots, toSnapshotExport(latest))
	}

	return b, nil
}

// Import recreates the backup's hosts, snapshots, and changes in the caller's
// tenant, preserving ids. mode is skip (default) or overwrite for id
// collisions. An unsupported schema version returns ErrBadSchema.
func (s *Service) Import(ctx context.Context, subj authz.Subjects, b Backup, mode string) (Result, error) {
	var res Result
	if err := authz.RequireTenant(subj, subj.TenantID); err != nil {
		return res, err
	}
	if b.SchemaVersion != SchemaVersion {
		return res, fmt.Errorf("%w: %d", ErrBadSchema, b.SchemaVersion)
	}
	if mode != ModeOverwrite {
		mode = ModeSkip
	}
	tenant := subj.TenantID

	// Hosts, deduped by id.
	for _, he := range b.Hosts {
		if _, err := s.st.GetHost(ctx, tenant, he.ID); err == nil {
			if mode == ModeSkip {
				res.HostsSkipped++
				continue
			}
			if err := s.st.DeleteHost(ctx, tenant, he.ID); err != nil {
				return res, err
			}
		} else if !errors.Is(err, repo.ErrNotFound) {
			return res, err
		}
		if _, err := s.st.ResolveHost(ctx, tenant, fromHostExport(tenant, he)); err != nil {
			return res, err
		}
		res.HostsImported++
	}

	// Snapshots, deduped by id.
	for _, se := range b.Snapshots {
		if _, err := s.st.GetSnapshot(ctx, tenant, se.ID); err == nil {
			if mode == ModeSkip {
				res.SnapshotsSkipped++
				continue
			}
			if err := s.st.DeleteSnapshot(ctx, tenant, se.ID); err != nil {
				return res, err
			}
		} else if !errors.Is(err, repo.ErrNotFound) {
			return res, err
		}
		if err := s.st.InsertSnapshot(ctx, fromSnapshotExport(tenant, se)); err != nil {
			return res, err
		}
		res.SnapshotsImported++
	}

	// Changes: inserted preserving ids (history rows have no natural dedup key).
	if len(b.Changes) > 0 {
		changes := make([]store.Change, 0, len(b.Changes))
		for _, ce := range b.Changes {
			changes = append(changes, fromChangeExport(tenant, ce))
		}
		if err := s.st.InsertChanges(ctx, changes); err != nil {
			return res, err
		}
		res.ChangesImported += len(changes)
	}

	return res, nil
}

func toHostExport(h store.Host) HostExport {
	return HostExport{
		ID: h.ID, Hostname: h.Hostname, MachineID: h.MachineID, HardwareUUID: h.HardwareUUID,
		SystemSerial: h.SystemSerial, IdentityKey: h.IdentityKey, Manufacturer: h.Manufacturer,
		Model: h.Model, OSName: h.OSName, OSVersion: h.OSVersion, OSArch: h.OSArch,
		AgentVersion: h.AgentVersion, AssignedUser: h.AssignedUser, Status: h.Status, Tags: h.Tags,
		FirstSeen: h.FirstSeen, LastSeen: h.LastSeen, CreatedAt: h.CreatedAt, UpdatedAt: h.UpdatedAt,
	}
}

func fromHostExport(tenant string, he HostExport) store.Host {
	return store.Host{
		ID: he.ID, TenantID: tenant, Hostname: he.Hostname, MachineID: he.MachineID,
		HardwareUUID: he.HardwareUUID, SystemSerial: he.SystemSerial, IdentityKey: he.IdentityKey,
		Manufacturer: he.Manufacturer, Model: he.Model, OSName: he.OSName, OSVersion: he.OSVersion,
		OSArch: he.OSArch, AgentVersion: he.AgentVersion, AssignedUser: he.AssignedUser,
		Status: he.Status, Tags: he.Tags, FirstSeen: he.FirstSeen, LastSeen: he.LastSeen,
		CreatedAt: he.CreatedAt, UpdatedAt: he.UpdatedAt,
	}
}

func toSnapshotExport(s store.Snapshot) SnapshotExport {
	return SnapshotExport{
		ID: s.ID, HostID: s.HostID, CollectedAt: s.CollectedAt, ReceivedAt: s.ReceivedAt,
		AgentVersion: s.AgentVersion, Source: s.Source, OSName: s.OSName, OSVersion: s.OSVersion,
		Manufacturer: s.Manufacturer, Model: s.Model, Payload: s.Payload,
	}
}

func fromSnapshotExport(tenant string, se SnapshotExport) store.Snapshot {
	return store.Snapshot{
		ID: se.ID, TenantID: tenant, HostID: se.HostID, CollectedAt: se.CollectedAt,
		ReceivedAt: se.ReceivedAt, AgentVersion: se.AgentVersion, Source: se.Source,
		OSName: se.OSName, OSVersion: se.OSVersion, Manufacturer: se.Manufacturer,
		Model: se.Model, Payload: se.Payload,
	}
}

func toChangeExport(c store.Change) ChangeExport {
	return ChangeExport{
		ID: c.ID, HostID: c.HostID, SnapshotID: c.SnapshotID, PrevSnapshotID: c.PrevSnapshotID,
		DetectedAt: c.DetectedAt, Category: c.Category, ChangeType: c.ChangeType,
		ComponentKey: c.ComponentKey, Before: c.Before, After: c.After,
	}
}

func fromChangeExport(tenant string, ce ChangeExport) store.Change {
	return store.Change{
		ID: ce.ID, TenantID: tenant, HostID: ce.HostID, SnapshotID: ce.SnapshotID,
		PrevSnapshotID: ce.PrevSnapshotID, DetectedAt: ce.DetectedAt, Category: ce.Category,
		ChangeType: ce.ChangeType, ComponentKey: ce.ComponentKey, Before: ce.Before, After: ce.After,
	}
}
