package sender

import (
	"context"
	"errors"
	"fmt"

	"google.golang.org/grpc"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
)

// UpgradeClient implements selfupdate.Client over the agent's authenticated
// ingest connection: the only place an agent ever obtains a release from
// (SR-002).
type UpgradeClient struct {
	s                   *Sender
	agentID, credential string
}

var _ selfupdate.Client = (*UpgradeClient)(nil)

// UpgradeClient returns the self-upgrade client of an enrolled agent.
func (s *Sender) UpgradeClient(agentID, credential string) *UpgradeClient {
	return &UpgradeClient{s: s, agentID: agentID, credential: credential}
}

func platformPB(p agentrelease.Platform) *invv1.AgentPlatform {
	return &invv1.AgentPlatform{Os: p.OS, Arch: p.Arch, InstallType: p.InstallType}
}

// Check asks whether an upgrade is available (apply: create the request).
func (c *UpgradeClient) Check(ctx context.Context, current string, p agentrelease.Platform, apply bool) (selfupdate.CheckResult, error) {
	client, conn, err := c.s.Dial()
	if err != nil {
		return selfupdate.CheckResult{}, err
	}
	defer conn.Close()
	var resp *invv1.CheckAgentUpdateResponse
	err = c.s.retry(ctx, func(cctx context.Context) error {
		var e error
		resp, e = client.CheckAgentUpdate(AuthContext(cctx, c.agentID, c.credential),
			&invv1.CheckAgentUpdateRequest{CurrentVersion: current, Platform: platformPB(p), Apply: apply})
		return e
	})
	if err != nil {
		return selfupdate.CheckResult{}, fmt.Errorf("sender: check update: %w", err)
	}
	return selfupdate.CheckResult{Available: resp.GetAvailable(), TargetVersion: resp.GetTargetVersion(), RequestID: resp.GetRequestId(),
		Reason: resp.GetReason(), AllowDowngrade: resp.GetAllowDowngrade()}, nil
}

// Report sends a progress report.
func (c *UpgradeClient) Report(ctx context.Context, r selfupdate.Report) error {
	client, conn, err := c.s.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	return c.s.retry(ctx, func(cctx context.Context) error {
		_, e := client.ReportUpgrade(AuthContext(cctx, c.agentID, c.credential), &invv1.ReportUpgradeRequest{
			RequestId: r.RequestID, State: r.State, FromVersion: r.FromVersion, ToVersion: r.ToVersion, Reason: r.Reason, Detail: r.Detail})
		return e
	})
}

// download is an open DownloadAgentRelease stream.
type download struct {
	conn   *grpc.ClientConn
	cancel context.CancelFunc
	stream grpc.ServerStreamingClient[invv1.DownloadAgentReleaseResponse]
	header selfupdate.Header
}

// Download opens the artifact stream of version (the request's target, or
// the running version's package without a request).
func (c *UpgradeClient) Download(ctx context.Context, requestID, version string) (selfupdate.Download, error) {
	client, conn, err := c.s.Dial()
	if err != nil {
		return nil, err
	}
	sctx, cancel := context.WithCancel(ctx)
	stream, err := client.DownloadAgentRelease(AuthContext(sctx, c.agentID, c.credential),
		&invv1.DownloadAgentReleaseRequest{RequestId: requestID, Version: version})
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, err
	}
	first, err := stream.Recv()
	if err != nil {
		cancel()
		_ = conn.Close()
		return nil, err
	}
	h := first.GetHeader()
	if h == nil {
		cancel()
		_ = conn.Close()
		return nil, errors.New("sender: download did not start with a release header")
	}
	return &download{conn: conn, cancel: cancel, stream: stream, header: selfupdate.Header{
		Manifest: h.GetManifest(), Signature: h.GetSignature(), KeyID: h.GetKeyId(), File: h.GetFile(), Size: h.GetSize(), SHA256: h.GetSha256(),
	}}, nil
}

func (d *download) Header() selfupdate.Header { return d.header }

// Next returns the next chunk; io.EOF at the end of the stream.
func (d *download) Next() (int64, []byte, error) {
	msg, err := d.stream.Recv()
	if err != nil {
		return 0, nil, err // io.EOF at the end
	}
	ch := msg.GetChunk()
	if ch == nil {
		return 0, nil, errors.New("sender: unexpected message in the artifact stream")
	}
	return ch.GetOffset(), ch.GetData(), nil
}

func (d *download) Close() error {
	d.cancel()
	return d.conn.Close()
}
