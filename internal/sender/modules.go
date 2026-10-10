package sender

import (
	"context"
	"errors"
	"fmt"
	"io"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Bundle stream errors (feature 037): the agent reports them as too_large
// and invalid_bundle instead of retrying.
var (
	// ErrBundleTooLarge: the header or the stream exceeds 12 MiB.
	ErrBundleTooLarge = errors.New("sender: module bundle over 12 MiB")
	// ErrBundleMalformed: no header first, a second header, or chunks that
	// are not contiguous or exceed the announced size.
	ErrBundleMalformed = errors.New("sender: malformed module bundle stream")
)

// ModuleClient fetches module delivery items and reports their outcome
// over the agent's authenticated ingest connection (feature 037). It never
// logs a bundle.
type ModuleClient struct {
	s                   *Sender
	agentID, credential string
}

// ModuleClient returns the module delivery client of an enrolled agent.
func (s *Sender) ModuleClient(agentID, credential string) *ModuleClient {
	return &ModuleClient{s: s, agentID: agentID, credential: credential}
}

// chunkSource is the receiving side of FetchModuleBundle.
type chunkSource interface {
	Recv() (*invv1.ModuleBundleChunk, error)
}

// Fetch makes one FetchModuleBundle call for an item and reads the whole
// stream into memory (at most 12 MiB; the caller checks the digest, writes
// and zeroes the bytes). Every call counts against the item's fetch budget
// on the server.
func (c *ModuleClient) Fetch(ctx context.Context, itemID string) (*invv1.ModuleBundleHeader, []byte, error) {
	client, conn, err := c.s.Dial()
	if err != nil {
		return nil, nil, err
	}
	defer conn.Close()
	cctx, cancel := context.WithTimeout(ctx, c.s.timeout)
	defer cancel()
	st, err := client.FetchModuleBundle(AuthContext(cctx, c.agentID, c.credential), &invv1.FetchModuleBundleRequest{ItemId: itemID})
	if err != nil {
		return nil, nil, err
	}
	return readBundle(st)
}

// readBundle reads the header and the contiguous chunks up to the announced
// size; the buffer is zeroed on failure.
func readBundle(st chunkSource) (*invv1.ModuleBundleHeader, []byte, error) {
	first, err := st.Recv()
	if err != nil {
		return nil, nil, err
	}
	h := first.GetHeader()
	switch {
	case h == nil || h.GetSize() < 0:
		return nil, nil, ErrBundleMalformed
	case h.GetSize() > store.MaxModuleBundleBytes:
		return nil, nil, ErrBundleTooLarge
	}
	data := make([]byte, 0, h.GetSize())
	for {
		m, err := st.Recv()
		if errors.Is(err, io.EOF) {
			return h, data, nil
		}
		if err == nil {
			c := m.GetChunk()
			switch {
			case c == nil || c.GetOffset() != int64(len(data)):
				err = ErrBundleMalformed
			case int64(len(data)+len(c.GetData())) > h.GetSize():
				err = ErrBundleMalformed
			default:
				data = append(data, c.GetData()...)
				continue
			}
		}
		clear(data[:cap(data)])
		return nil, nil, fmt.Errorf("sender: fetch module bundle: %w", err)
	}
}

// Report sends the outcome of an item, retrying transient errors; accepted
// is false when the server ignored it (terminal or duplicate).
func (c *ModuleClient) Report(ctx context.Context, req *invv1.ReportModuleRequest) (accepted bool, err error) {
	client, conn, err := c.s.Dial()
	if err != nil {
		return false, err
	}
	defer conn.Close()
	err = c.s.retry(ctx, func(cctx context.Context) error {
		resp, e := client.ReportModule(AuthContext(cctx, c.agentID, c.credential), req)
		if e != nil {
			return e
		}
		accepted = resp.GetAccepted()
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("sender: report module: %w", err)
	}
	return accepted, nil
}
