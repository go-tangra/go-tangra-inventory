package ingest

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	inventoryv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

// Agent self-upgrade on the ingest edge (feature 023, research D9). Every
// RPC is authenticated by the per-agent credential (auth.go) and bound to
// the verified agent: downloads and reports only ever concern its own
// request; nothing here reaches another tenant or another agent.

// maxCapabilities bounds the capabilities an agent may announce.
const maxCapabilities = 16

// UpgradeService is the upgrade lifecycle the edge drives.
type UpgradeService interface {
	OnConnect(ctx context.Context, a store.Agent) ([]registry.Command, error)
	Check(ctx context.Context, a store.Agent, current string, p agentrelease.Platform, apply bool) (upgrades.CheckResult, error)
	AuthorizeDownload(ctx context.Context, a store.Agent, requestID, version string) (string, error)
	Report(ctx context.Context, a store.Agent, r upgrades.Report) (bool, error)
}

// ReleaseSource serves stored release artifacts.
type ReleaseSource interface {
	OpenArtifact(ctx context.Context, version string, p agentrelease.Platform) (*releases.Stream, error)
}

// upgradeEdge holds the upgrade wiring and the download caps.
type upgradeEdge struct {
	svc      UpgradeService
	rel      ReleaseSource
	slots    chan struct{} // global concurrent downloads
	mu       sync.Mutex
	perAgent map[string]bool // agents with a running download
}

// WithUpgrades attaches agent self-upgrade: at most maxDownloads concurrent
// artifact downloads (one per agent), each bounded by deadline.
func (s *Server) WithUpgrades(svc UpgradeService, rel ReleaseSource, maxDownloads int, deadline time.Duration) *Server {
	if maxDownloads < 1 {
		maxDownloads = 1
	}
	s.upgradeEdge = &upgradeEdge{svc: svc, rel: rel, slots: make(chan struct{}, maxDownloads), perAgent: map[string]bool{}}
	s.downloadDeadline = deadline
	return s
}

func (s *Server) activeDownloads() int {
	if s.upgradeEdge == nil {
		return 0
	}
	return len(s.upgradeEdge.slots)
}

var (
	agentOSes     = set("linux", "windows")
	agentArches   = set("amd64", "arm64")
	installTypes  = set(agentrelease.InstallDeb, agentrelease.InstallRPM, agentrelease.InstallBinary)
	errNoUpgrades = status.Error(codes.Unimplemented, "agent upgrades are not enabled")
)

// platformFromPB keeps only closed-set platform values.
func platformFromPB(p *inventoryv1.AgentPlatform) agentrelease.Platform {
	return agentrelease.Platform{OS: enum(p.GetOs(), agentOSes, ""), Arch: enum(p.GetArch(), agentArches, ""),
		InstallType: enum(p.GetInstallType(), installTypes, "")}
}

// capabilitiesFromPB keeps at most maxCapabilities well-formed tokens.
func capabilitiesFromPB(in []string) []string {
	var out []string
	for _, c := range in {
		if upgrades.ValidCapability(c) && len(out) < maxCapabilities {
			out = append(out, c)
		}
	}
	return out
}

// recordPlatform stores the platform and capabilities announced on
// StreamCommands and returns the agent with them.
func (s *Server) recordPlatform(ctx context.Context, a store.Agent, req *inventoryv1.StreamRequest) store.Agent {
	p := platformFromPB(req.GetPlatform())
	caps := capabilitiesFromPB(req.GetCapabilities())
	if err := s.st.SetAgentPlatform(ctx, a.ID, p.OS, p.Arch, p.InstallType, caps, time.Now().UTC()); err == nil {
		a.OS, a.Arch, a.InstallType, a.Capabilities = p.OS, p.Arch, p.InstallType, caps
	}
	return a
}

// pendingCommands returns the upgrade commands to send right after connect.
func (s *Server) pendingCommands(ctx context.Context, a store.Agent) []registry.Command {
	if s.upgradeEdge == nil {
		return nil
	}
	cmds, err := s.upgradeEdge.svc.OnConnect(ctx, a)
	if err != nil {
		return nil // delivered on the next connect
	}
	return cmds
}

// commandToPB maps a registry command to the wire message.
func commandToPB(cmd registry.Command) *inventoryv1.Command {
	out := &inventoryv1.Command{CommandId: cmd.ID, Type: commandType(cmd.Type)}
	if u := cmd.Upgrade; u != nil {
		out.Type = inventoryv1.CommandType_COMMAND_TYPE_UPGRADE
		out.Upgrade = &inventoryv1.UpgradeCommand{RequestId: u.RequestID, TargetVersion: u.TargetVersion, AllowDowngrade: u.AllowDowngrade}
	}
	// Certificate deliveries carry the item id and name only (feature 033).
	if c := cmd.Certificate; c != nil {
		out.Type = inventoryv1.CommandType_COMMAND_TYPE_CERTIFICATE
		out.Certificate = &inventoryv1.CertificateCommand{ItemId: c.ItemID, Name: c.Name}
	}
	return out
}

// CheckAgentUpdate tells the agent whether an upgrade is available; with
// apply it creates the (audited) request.
func (s *Server) CheckAgentUpdate(ctx context.Context, req *inventoryv1.CheckAgentUpdateRequest) (*inventoryv1.CheckAgentUpdateResponse, error) {
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	if s.upgradeEdge == nil {
		return nil, errNoUpgrades
	}
	res, err := s.upgradeEdge.svc.Check(ctx, agent, req.GetCurrentVersion(), platformFromPB(req.GetPlatform()), req.GetApply())
	if errors.Is(err, upgrades.ErrInvalid) {
		return nil, status.Error(codes.InvalidArgument, "invalid platform")
	}
	if err != nil {
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	return &inventoryv1.CheckAgentUpdateResponse{Available: res.Available, TargetVersion: res.TargetVersion, RequestId: res.RequestID,
		Reason: res.Reason, AllowDowngrade: res.AllowDowngrade}, nil
}

// acquire takes a download slot for agentID (one per agent, a global cap).
func (e *upgradeEdge) acquire(agentID string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.perAgent[agentID] {
		return false
	}
	select {
	case e.slots <- struct{}{}:
	default:
		return false
	}
	e.perAgent[agentID] = true
	return true
}

func (e *upgradeEdge) release(agentID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.perAgent, agentID)
	<-e.slots
}

// DownloadAgentRelease streams the artifact of the requested version for the
// agent's stored platform: first the signed release header, then the bytes
// in order. Only the target of the agent's own active request or its own
// current version (rollback package) is served.
func (s *Server) DownloadAgentRelease(req *inventoryv1.DownloadAgentReleaseRequest, stream inventoryv1.IngestService_DownloadAgentReleaseServer) error {
	ctx := stream.Context()
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "agent credential required")
	}
	e := s.upgradeEdge
	if e == nil {
		return errNoUpgrades
	}
	if !e.acquire(agent.ID) {
		return status.Error(codes.ResourceExhausted, "too many downloads")
	}
	defer e.release(agent.ID)
	ctx, cancel := context.WithTimeout(ctx, s.downloadDeadline)
	defer cancel()

	version, err := e.svc.AuthorizeDownload(ctx, agent, req.GetRequestId(), req.GetVersion())
	switch {
	case errors.Is(err, repo.ErrNotFound):
		return status.Error(codes.NotFound, "no such release for this agent")
	case errors.Is(err, upgrades.ErrNotActive):
		return status.Error(codes.FailedPrecondition, "upgrade request is not active")
	case err != nil:
		return status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	p := agentrelease.Platform{OS: agent.OS, Arch: agent.Arch, InstallType: agent.InstallType}
	art, err := e.rel.OpenArtifact(ctx, version, p)
	if err != nil {
		return status.Error(codes.NotFound, "no artifact for this platform")
	}
	h := art.Header()
	if err := stream.Send(&inventoryv1.DownloadAgentReleaseResponse{Part: &inventoryv1.DownloadAgentReleaseResponse_Header{Header: &inventoryv1.ReleaseHeader{
		Manifest: h.Manifest, Signature: h.Signature, KeyId: h.KeyID, File: h.Artifact.File, Size: h.Artifact.Size, Sha256: h.Artifact.SHA256,
	}}}); err != nil {
		return err
	}
	for {
		if ctx.Err() != nil {
			return status.Error(codes.DeadlineExceeded, "download deadline exceeded")
		}
		off, data, err := art.Next(ctx)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return status.Error(codes.Unavailable, "temporarily_unavailable")
		}
		if err := stream.Send(&inventoryv1.DownloadAgentReleaseResponse{Part: &inventoryv1.DownloadAgentReleaseResponse_Chunk{
			Chunk: &inventoryv1.ArtifactChunk{Offset: off, Data: data}}}); err != nil {
			return err
		}
	}
}

// ReportUpgrade applies the agent's progress report to its own request.
func (s *Server) ReportUpgrade(ctx context.Context, req *inventoryv1.ReportUpgradeRequest) (*inventoryv1.ReportUpgradeResponse, error) {
	agent, ok := AgentFromContext(ctx)
	if !ok {
		return nil, status.Error(codes.Unauthenticated, "agent credential required")
	}
	if s.upgradeEdge == nil {
		return nil, errNoUpgrades
	}
	accepted, err := s.upgradeEdge.svc.Report(ctx, agent, upgrades.Report{RequestID: req.GetRequestId(), State: req.GetState(),
		FromVersion: req.GetFromVersion(), ToVersion: req.GetToVersion(), Reason: req.GetReason(), Detail: req.GetDetail()})
	switch {
	case errors.Is(err, upgrades.ErrInvalid):
		return nil, status.Error(codes.InvalidArgument, "invalid report")
	case errors.Is(err, repo.ErrNotFound):
		return nil, status.Error(codes.NotFound, "no such upgrade request")
	case err != nil:
		return nil, status.Error(codes.Unavailable, "temporarily_unavailable")
	}
	return &inventoryv1.ReportUpgradeResponse{Accepted: accepted}, nil
}
