package snapshots

import (
	"context"
	"errors"

	"github.com/go-freya/freya/services/inventory/internal/diff"
	"github.com/go-freya/freya/services/inventory/internal/events"
	"github.com/go-freya/freya/services/inventory/internal/repo"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

// Ingest is the agent/import write path. It resolves (or creates) the host from
// the payload's identity and summary, persists the immutable snapshot, computes
// the change history against the host's previous latest snapshot, and publishes
// realtime events. It returns the stored snapshot.
//
// The host is upserted BEFORE the snapshot is written, and the previous latest
// snapshot is read BEFORE the new one is inserted so the diff compares against
// the genuine prior state.
func (s *Service) Ingest(ctx context.Context, tenantID string, inv store.Inventory, source string) (store.Snapshot, error) {
	if tenantID == "" {
		return store.Snapshot{}, errInvalidTenant
	}
	now := s.now().UTC()
	collectedAt := inv.CollectedAt
	if collectedAt.IsZero() {
		collectedAt = now
	}

	manufacturer := summaryManufacturer(inv)
	model := summaryModel(inv)

	// 1. Resolve/create the host from the reported identity + summary fields.
	host, err := s.hosts.Resolve(ctx, tenantID, store.Host{
		Hostname:     inv.Identity.Hostname,
		MachineID:    inv.Identity.MachineID,
		HardwareUUID: inv.Identity.HardwareUUID,
		SystemSerial: inv.System.SerialNumber,
		Manufacturer: manufacturer,
		Model:        model,
		OSName:       inv.OS.Name,
		OSVersion:    inv.OS.Version,
		OSArch:       inv.OS.Arch,
		AgentVersion: inv.AgentVersion,
		LastSeen:     collectedAt,
	})
	if err != nil {
		return store.Snapshot{}, err
	}

	// 2. Read the previous latest snapshot (for the change diff) before insert.
	var prev store.Snapshot
	havePrev := false
	if p, gerr := s.st.GetLatestForHost(ctx, tenantID, host.ID); gerr == nil {
		prev = p
		havePrev = true
	} else if !errors.Is(gerr, repo.ErrNotFound) {
		return store.Snapshot{}, gerr
	}

	// 3. Build and persist the immutable snapshot.
	snap := store.Snapshot{
		ID:           store.NewID(),
		TenantID:     tenantID,
		HostID:       host.ID,
		CollectedAt:  collectedAt,
		ReceivedAt:   now,
		AgentVersion: inv.AgentVersion,
		Source:       sourceOrDefault(source),
		OSName:       inv.OS.Name,
		OSVersion:    inv.OS.Version,
		Manufacturer: manufacturer,
		Model:        model,
		Payload:      inv,
	}
	if err := s.st.InsertSnapshot(ctx, snap); err != nil {
		return store.Snapshot{}, err
	}

	// 4. Compute + persist the change history against the previous snapshot.
	var changes []store.Change
	if havePrev {
		raw := diff.Diff(prev.Payload, inv)
		if len(raw) > 0 {
			changes = make([]store.Change, 0, len(raw))
			for _, c := range raw {
				c.ID = store.NewID()
				c.TenantID = tenantID
				c.HostID = host.ID
				c.SnapshotID = snap.ID
				c.PrevSnapshotID = prev.ID
				c.DetectedAt = now
				changes = append(changes, c)
			}
			if err := s.st.InsertChanges(ctx, changes); err != nil {
				return store.Snapshot{}, err
			}
		}
	}

	// 5. Publish realtime events (content-free).
	s.pub.Publish(ctx, tenantID, events.SnapshotReceived,
		events.SnapshotReceivedPayload(host.ID, snap.ID, host.Hostname, collectedAt))
	if len(changes) > 0 {
		s.pub.Publish(ctx, tenantID, events.HostChanged,
			events.HostChangedPayload(host.ID, len(changes)))
	}

	return snap, nil
}

var errInvalidTenant = errors.New("snapshots: tenant required")

// summaryManufacturer lifts the host manufacturer from the payload, preferring
// the system record and falling back to the baseboard.
func summaryManufacturer(inv store.Inventory) string {
	if inv.System.Manufacturer != "" {
		return inv.System.Manufacturer
	}
	return inv.Baseboard.Manufacturer
}

// summaryModel lifts the host model from the payload, preferring the system
// product name and falling back to the baseboard product.
func summaryModel(inv store.Inventory) string {
	if inv.System.ProductName != "" {
		return inv.System.ProductName
	}
	return inv.Baseboard.Product
}

func sourceOrDefault(src string) string {
	if src == "" {
		return store.SourceAgent
	}
	return src
}
