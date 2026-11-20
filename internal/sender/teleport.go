package sender

import (
	"context"
	"fmt"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// TeleportClient fetches Teleport join material and reports join progress
// over the agent's authenticated ingest connection (feature 038). It never
// logs the material.
type TeleportClient struct {
	s                   *Sender
	agentID, credential string
}

// TeleportClient returns the Teleport join client of an enrolled agent.
func (s *Sender) TeleportClient(agentID, credential string) *TeleportClient {
	return &TeleportClient{s: s, agentID: agentID, credential: credential}
}

// Fetch makes one FetchTeleportJoin call for an item; every call counts
// against the item's fetch budget on the server. The caller wipes the
// token.
func (c *TeleportClient) Fetch(ctx context.Context, itemID string) (*invv1.TeleportJoinMaterial, error) {
	client, conn, err := c.s.Dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	cctx, cancel := context.WithTimeout(ctx, c.s.timeout)
	defer cancel()
	return client.FetchTeleportJoin(AuthContext(cctx, c.agentID, c.credential), &invv1.FetchTeleportJoinRequest{ItemId: itemID})
}

// Report sends a progress or outcome report, retrying transient errors;
// accepted is false when the server ignored it (terminal or duplicate).
func (c *TeleportClient) Report(ctx context.Context, req *invv1.ReportTeleportJoinRequest) (accepted bool, err error) {
	client, conn, err := c.s.Dial()
	if err != nil {
		return false, err
	}
	defer conn.Close()
	err = c.s.retry(ctx, func(cctx context.Context) error {
		resp, e := client.ReportTeleportJoin(AuthContext(cctx, c.agentID, c.credential), req)
		if e != nil {
			return e
		}
		accepted = resp.GetAccepted()
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("sender: report teleport join: %w", err)
	}
	return accepted, nil
}
