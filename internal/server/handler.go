package server

import (
	"context"
	"database/sql"
	"errors"
	"log"

	"github.com/google/uuid"

	collectorv1 "github.com/go-tangra/go-tangra-inventory/gen/go/inventory/collector/v1"
	"github.com/go-tangra/go-tangra-inventory/internal/convert"
	"github.com/go-tangra/go-tangra-inventory/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Handler implements the InventoryCollectorService gRPC service.
type Handler struct {
	collectorv1.UnimplementedInventoryCollectorServiceServer
	store  *store.Store
	cmdReg *CommandRegistry
}

// NewHandler creates a new gRPC handler backed by the given store.
func NewHandler(s *store.Store, reg *CommandRegistry) *Handler {
	return &Handler{store: s, cmdReg: reg}
}

func (h *Handler) SubmitInventory(ctx context.Context, req *collectorv1.SubmitInventoryRequest) (*collectorv1.SubmitInventoryResponse, error) {
	if req.Inventory == nil {
		return nil, status.Error(codes.InvalidArgument, "inventory is required")
	}
	if req.Inventory.Hostname == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}

	rec, err := convert.InventoryToRecord(req.Inventory)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "convert inventory: %v", err)
	}

	id, storedAt, err := h.store.Insert(ctx, rec)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "store inventory: %v", err)
	}

	return &collectorv1.SubmitInventoryResponse{
		Id:       id,
		StoredAt: timestamppb.New(storedAt),
	}, nil
}

func (h *Handler) GetInventory(ctx context.Context, req *collectorv1.GetInventoryRequest) (*collectorv1.GetInventoryResponse, error) {
	rec, err := h.store.Get(ctx, req.Id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "inventory %d not found", req.Id)
		}
		return nil, status.Errorf(codes.Internal, "get inventory: %v", err)
	}

	inv, err := convert.RecordToInventory(rec)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decode inventory: %v", err)
	}

	return &collectorv1.GetInventoryResponse{
		Id:        rec.ID,
		Inventory: inv,
		StoredAt:  timestamppb.New(rec.StoredAt),
	}, nil
}

func (h *Handler) ListInventories(ctx context.Context, req *collectorv1.ListInventoriesRequest) (*collectorv1.ListInventoriesResponse, error) {
	filter := store.ListFilter{
		Hostname:   req.Hostname,
		Username:   req.Username,
		SystemUUID: req.SystemUuid,
		PageSize:   int(req.PageSize),
		Page:       int(req.Page),
	}
	if req.CollectedAfter != nil {
		t := req.CollectedAfter.AsTime()
		filter.CollectedAfter = &t
	}
	if req.CollectedBefore != nil {
		t := req.CollectedBefore.AsTime()
		filter.CollectedBefore = &t
	}

	records, total, err := h.store.List(ctx, filter)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list inventories: %v", err)
	}

	summaries := make([]*collectorv1.InventorySummary, len(records))
	for i := range records {
		summaries[i] = convert.RecordToSummary(&records[i])
	}

	return &collectorv1.ListInventoriesResponse{
		Inventories: summaries,
		TotalCount:  int32(total),
	}, nil
}

func (h *Handler) DeleteInventory(ctx context.Context, req *collectorv1.DeleteInventoryRequest) (*collectorv1.DeleteInventoryResponse, error) {
	err := h.store.Delete(ctx, req.Id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "inventory %d not found", req.Id)
		}
		return nil, status.Errorf(codes.Internal, "delete inventory: %v", err)
	}
	return &collectorv1.DeleteInventoryResponse{}, nil
}

func (h *Handler) GetLatestByHostname(ctx context.Context, req *collectorv1.GetLatestByHostnameRequest) (*collectorv1.GetLatestByHostnameResponse, error) {
	if req.Hostname == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}

	rec, err := h.store.GetLatestByHostname(ctx, req.Hostname)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, status.Errorf(codes.NotFound, "no inventory found for hostname %q", req.Hostname)
		}
		return nil, status.Errorf(codes.Internal, "get latest inventory: %v", err)
	}

	inv, err := convert.RecordToInventory(rec)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "decode inventory: %v", err)
	}

	return &collectorv1.GetLatestByHostnameResponse{
		Id:        rec.ID,
		Inventory: inv,
		StoredAt:  timestamppb.New(rec.StoredAt),
	}, nil
}

func (h *Handler) StreamCommands(req *collectorv1.StreamCommandsRequest, stream grpc.ServerStreamingServer[collectorv1.InventoryCommand]) error {
	if req.ClientId == "" {
		return status.Error(codes.InvalidArgument, "client_id is required")
	}

	ch := h.cmdReg.Register(req.ClientId, req.ClientVersion)
	defer h.cmdReg.Unregister(req.ClientId)

	log.Printf("Agent %q connected (version: %s)", req.ClientId, req.ClientVersion)

	for {
		select {
		case cmd, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(cmd); err != nil {
				return err
			}
		case <-stream.Context().Done():
			log.Printf("Agent %q disconnected", req.ClientId)
			return stream.Context().Err()
		}
	}
}

func (h *Handler) RefreshInventory(ctx context.Context, req *collectorv1.RefreshInventoryRequest) (*collectorv1.RefreshInventoryResponse, error) {
	if req.Hostname == "" {
		return nil, status.Error(codes.InvalidArgument, "hostname is required")
	}

	if !h.cmdReg.IsConnected(req.Hostname) {
		return nil, status.Errorf(codes.NotFound, "agent %q is not connected", req.Hostname)
	}

	cmdID := uuid.NewString()
	cmd := &collectorv1.InventoryCommand{
		CommandId:   cmdID,
		CommandType: collectorv1.InventoryCommandType_INVENTORY_COMMAND_TYPE_REFRESH,
	}

	if err := h.cmdReg.Send(req.Hostname, cmd); err != nil {
		return nil, status.Errorf(codes.Internal, "send refresh command: %v", err)
	}

	log.Printf("Sent refresh command %s to agent %q", cmdID, req.Hostname)

	return &collectorv1.RefreshInventoryResponse{
		Sent:      true,
		CommandId: cmdID,
	}, nil
}

func (h *Handler) ListConnectedAgents(_ context.Context, _ *collectorv1.ListConnectedAgentsRequest) (*collectorv1.ListConnectedAgentsResponse, error) {
	agents := h.cmdReg.ListConnected()

	pbAgents := make([]*collectorv1.ConnectedAgent, len(agents))
	for i, a := range agents {
		pbAgents[i] = &collectorv1.ConnectedAgent{
			ClientId:    a.ClientID,
			Version:     a.Version,
			ConnectedAt: timestamppb.New(a.ConnectedAt),
		}
	}

	return &collectorv1.ListConnectedAgentsResponse{
		Agents: pbAgents,
	}, nil
}
