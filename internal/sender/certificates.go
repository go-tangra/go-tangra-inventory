package sender

import (
	"context"
	"fmt"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
)

// CertClient fetches certificate delivery items and reports their outcome
// over the agent's authenticated ingest connection (feature 033). It never
// logs a bundle.
type CertClient struct {
	s                   *Sender
	agentID, credential string
}

// CertClient returns the certificate delivery client of an enrolled agent.
func (s *Sender) CertClient(agentID, credential string) *CertClient {
	return &CertClient{s: s, agentID: agentID, credential: credential}
}

// Fetch makes one FetchCertificate call for an item (the caller retries
// transient errors while the item is active; every fetch counts against the
// item's fetch limit on the server).
func (c *CertClient) Fetch(ctx context.Context, itemID string) (*invv1.CertificateBundle, error) {
	client, conn, err := c.s.Dial()
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	cctx, cancel := context.WithTimeout(ctx, c.s.timeout)
	defer cancel()
	return client.FetchCertificate(AuthContext(cctx, c.agentID, c.credential), &invv1.FetchCertificateRequest{ItemId: itemID})
}

// Report sends the outcome of an item, retrying transient errors; accepted
// is false when the server ignored it (terminal or duplicate).
func (c *CertClient) Report(ctx context.Context, req *invv1.ReportCertificateRequest) (accepted bool, err error) {
	client, conn, err := c.s.Dial()
	if err != nil {
		return false, err
	}
	defer conn.Close()
	err = c.s.retry(ctx, func(cctx context.Context) error {
		resp, e := client.ReportCertificate(AuthContext(cctx, c.agentID, c.credential), req)
		if e != nil {
			return e
		}
		accepted = resp.GetAccepted()
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("sender: report certificate: %w", err)
	}
	return accepted, nil
}
