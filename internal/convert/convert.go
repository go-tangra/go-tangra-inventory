package convert

import (
	"fmt"
	"time"

	collectorv1 "github.com/go-tangra/go-tangra-inventory/gen/go/inventory/collector/v1"
	"github.com/go-tangra/go-tangra-inventory/internal/store"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// InventoryToRecord converts a proto Inventory to a store record.
func InventoryToRecord(inv *collectorv1.Inventory) (*store.InventoryRecord, error) {
	jsonBytes, err := protojson.Marshal(inv)
	if err != nil {
		return nil, fmt.Errorf("marshal inventory to JSON: %w", err)
	}

	var collectedAt time.Time
	if inv.CollectedAt != nil {
		collectedAt = inv.CollectedAt.AsTime()
	} else {
		collectedAt = time.Now().UTC()
	}

	var systemUUID, systemSerial string
	if inv.System != nil {
		systemUUID = inv.System.Uuid
		systemSerial = inv.System.SerialNumber
	}

	return &store.InventoryRecord{
		Hostname:      inv.Hostname,
		Username:      inv.Username,
		SystemUUID:    systemUUID,
		SystemSerial:  systemSerial,
		CollectedAt:   collectedAt,
		InventoryJSON: string(jsonBytes),
	}, nil
}

// RecordToInventory converts a store record back to a proto Inventory.
func RecordToInventory(rec *store.InventoryRecord) (*collectorv1.Inventory, error) {
	var inv collectorv1.Inventory
	if err := protojson.Unmarshal([]byte(rec.InventoryJSON), &inv); err != nil {
		return nil, fmt.Errorf("unmarshal inventory JSON: %w", err)
	}
	return &inv, nil
}

// RecordToSummary converts a store record to an InventorySummary proto.
func RecordToSummary(rec *store.InventoryRecord) *collectorv1.InventorySummary {
	return &collectorv1.InventorySummary{
		Id:           rec.ID,
		Hostname:     rec.Hostname,
		Username:     rec.Username,
		SystemUuid:   rec.SystemUUID,
		SystemSerial: rec.SystemSerial,
		CollectedAt:  timestamppb.New(rec.CollectedAt),
		StoredAt:     timestamppb.New(rec.StoredAt),
	}
}
