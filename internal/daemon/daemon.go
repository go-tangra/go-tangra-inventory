// Package daemon runs the endpoint agent's long-lived loop: it enrolls once
// (persisting the issued agent id and credential), submits a collected inventory
// at startup and on the configured interval, and holds a reconnecting
// StreamCommands stream that submits immediately on a refresh command and
// starts a self-upgrade on an upgrade command (feature 023; the agent
// announces its platform and the upgrade.v1 capability on the stream and
// confirms or reports a finished upgrade after its first connect and submit).
// With a local certificate store (feature 033, Linux) it announces cert.v1
// and installs certificates named by CERTIFICATE commands: it fetches the
// bundle by item id, installs it and reports the outcome.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/collector"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Upgrader is the agent's self-upgrade core (selfupdate.Updater).
type Upgrader interface {
	Upgrade(ctx context.Context, cmd selfupdate.Command) error
	Resume(ctx context.Context) error
}

// UpgraderFactory builds the upgrader of an enrolled agent (nil: none).
type UpgraderFactory func(agentID, credential string) Upgrader

const (
	baseBackoff = 1 * time.Second
	maxBackoff  = 2 * time.Minute
)

// state is the small persisted enrollment cursor (the agent id). The credential
// secret is persisted separately in the credential file with 0600 perms.
type state struct {
	AgentID string `json:"agent_id"`
}

// Daemon owns the agent runtime configuration and enrollment identity.
type Daemon struct {
	cfg     config.AgentConfig
	sender  *sender.Sender
	version string

	agentID    string
	credential string

	// Self-upgrade (feature 023).
	platform    agentrelease.Platform
	newUpgrader UpgraderFactory
	upg         Upgrader
	refresh     func(context.Context) error
	connected   atomic.Bool
	submitted   atomic.Bool
	resumed     atomic.Bool

	// Certificate delivery (feature 033).
	certStore     CertInstaller
	newCertClient func(agentID, credential string) CertClient
	certs         *certState
	goos          string
}

// New builds a Daemon for the given agent configuration and version.
func New(cfg config.AgentConfig, version string) *Daemon {
	d := &Daemon{
		cfg: cfg,
		sender: sender.New(cfg.IngestEndpoint, sender.Options{
			Insecure: cfg.Insecure, CAFile: cfg.CAFile, ServerName: cfg.ServerName,
		}),
		version: version,
		certs:   newCertState(),
		goos:    runtime.GOOS,
	}
	d.refresh = d.collectAndSubmit
	d.newCertClient = func(agentID, credential string) CertClient { return d.sender.CertClient(agentID, credential) }
	return d
}

// Sender is the ingest client of the daemon.
func (d *Daemon) Sender() *sender.Sender { return d.sender }

// WithUpgrades enables self-upgrade: the agent announces platform and the
// upgrade.v1 capability and builds its upgrader once enrolled.
func (d *Daemon) WithUpgrades(p agentrelease.Platform, factory UpgraderFactory) *Daemon {
	d.platform, d.newUpgrader = p, factory
	return d
}

// streamRequest is the StreamCommands request: version, platform and the
// capabilities: upgrade.v1 when server-pushed upgrades are enabled and
// possible, cert.v1 when certificate delivery is enabled locally, the
// platform has a store (Linux) and the transport allows it.
func (d *Daemon) streamRequest() *invv1.StreamRequest {
	req := &invv1.StreamRequest{AgentId: d.agentID, AgentVersion: d.version}
	if d.platform.OS != "" {
		req.Platform = &invv1.AgentPlatform{Os: d.platform.OS, Arch: d.platform.Arch, InstallType: d.platform.InstallType}
	}
	if d.upg != nil && d.cfg.Upgrade.Enabled && d.platform.Valid() {
		req.Capabilities = append(req.Capabilities, store.CapUpgradeV1)
	}
	if d.certsActive() {
		req.Capabilities = append(req.Capabilities, store.CapCertV1)
	}
	return req
}

// handleCommand executes one pushed command.
func (d *Daemon) handleCommand(ctx context.Context, cmd *invv1.Command) {
	switch cmd.GetType() {
	case invv1.CommandType_COMMAND_TYPE_REFRESH:
		log.Printf("daemon: refresh command %s received", cmd.GetCommandId())
		if err := d.refresh(ctx); err != nil {
			log.Printf("daemon: refresh submit failed: %v", err)
		} else {
			log.Println("daemon: refresh complete; inventory re-submitted")
		}
	case invv1.CommandType_COMMAND_TYPE_UPGRADE:
		u := cmd.GetUpgrade()
		if d.upg == nil || !d.cfg.Upgrade.Enabled || u == nil {
			log.Printf("daemon: ignoring upgrade command %s (server-pushed upgrades disabled)", cmd.GetCommandId())
			return
		}
		log.Printf("daemon: upgrade request %s to %s received", u.GetRequestId(), u.GetTargetVersion())
		go func() {
			if err := d.upg.Upgrade(ctx, selfupdate.Command{RequestID: u.GetRequestId(), TargetVersion: u.GetTargetVersion(),
				AllowDowngrade: u.GetAllowDowngrade()}); err != nil {
				log.Printf("daemon: upgrade %s refused: %v", u.GetRequestId(), err)
			}
		}()
	case invv1.CommandType_COMMAND_TYPE_CERTIFICATE:
		d.enqueueCertificate(ctx, cmd)
	default:
		log.Printf("daemon: ignoring unknown command type %d (id %s)", cmd.GetType(), cmd.GetCommandId())
	}
}

// maybeResume finishes an upgrade in flight once the agent has connected
// and submitted (it confirms a new version or reports a rollback).
func (d *Daemon) maybeResume(ctx context.Context) {
	if d.upg == nil || !d.connected.Load() || !d.submitted.Load() || d.resumed.Load() {
		return
	}
	if err := d.upg.Resume(ctx); err != nil {
		log.Printf("daemon: upgrade confirmation pending: %v", err)
		return
	}
	d.resumed.Store(true)
}

// Run performs enroll-if-needed, an initial submit, then runs the periodic
// submit loop and the reconnecting command stream until ctx is canceled.
func (d *Daemon) Run(ctx context.Context) error {
	inv, err := collector.CollectWith(ctx, collector.OptionsFrom(d.cfg))
	if err != nil {
		return fmt.Errorf("daemon: initial collect: %w", err)
	}
	if err := d.ensureEnrolled(ctx, inv.Identity); err != nil {
		return fmt.Errorf("daemon: enroll: %w", err)
	}
	if d.newUpgrader != nil {
		d.upg = d.newUpgrader(d.agentID, d.credential)
	}
	if err := d.submit(ctx, inv); err != nil {
		log.Printf("daemon: initial submit failed: %v", err)
	} else {
		log.Println("daemon: initial inventory submitted; entering daemon mode")
	}

	go d.periodicLoop(ctx)
	go d.certLoop(ctx)
	d.reconnectLoop(ctx)
	return nil
}

// SubmitCollected enrolls if needed and submits an already-collected inventory
// once, returning the snapshot id. It backs the agent's one-shot mode.
func (d *Daemon) SubmitCollected(ctx context.Context, inv store.Inventory) (string, error) {
	if err := d.ensureEnrolled(ctx, inv.Identity); err != nil {
		return "", fmt.Errorf("daemon: enroll: %w", err)
	}
	return d.sender.Submit(ctx, d.agentID, d.credential, inv)
}

// Credentials returns the persisted agent id and credential (the `update`
// command runs only on an enrolled agent).
func (d *Daemon) Credentials() (agentID, credential string, ok bool) { return d.loadEnrollment() }

// ensureEnrolled loads a persisted agent id + credential, or consumes the
// enrollment token to obtain and persist a fresh one.
func (d *Daemon) ensureEnrolled(ctx context.Context, ident store.Identity) error {
	if id, cred, ok := d.loadEnrollment(); ok {
		d.agentID, d.credential = id, cred
		log.Printf("daemon: using persisted credential for agent %s", id)
		return nil
	}

	var id, cred string
	// A token that is present wins; without one, the auto-enrollment key is
	// used (the packaged config always names a token_file).
	if d.cfg.AutoEnroll.Configured() && !fileExists(d.cfg.TokenFile) {
		key, err := d.readAutoKey()
		if err != nil {
			return err
		}
		if id, cred, err = d.sender.EnrollAuto(ctx, key, d.cfg.AutoEnroll.KeyID, ident, d.version); err != nil {
			return err
		}
	} else {
		token, err := d.readToken()
		if err != nil {
			return err
		}
		if id, cred, err = d.sender.Enroll(ctx, token, ident, d.version); err != nil {
			return err
		}
	}
	d.agentID, d.credential = id, cred
	if err := d.saveEnrollment(id, cred); err != nil {
		return fmt.Errorf("persist credential: %w", err)
	}
	log.Printf("daemon: enrolled as agent %s", id)
	return nil
}

// periodicLoop submits a freshly collected inventory on the configured interval.
func (d *Daemon) periodicLoop(ctx context.Context) {
	t := time.NewTicker(d.cfg.AgentInterval())
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := d.collectAndSubmit(ctx); err != nil {
				log.Printf("daemon: periodic submit failed: %v", err)
			} else {
				log.Println("daemon: periodic inventory submitted")
			}
		}
	}
}

// reconnectLoop keeps the command stream connected, backing off exponentially
// between reconnects, until ctx is canceled.
func (d *Daemon) reconnectLoop(ctx context.Context) {
	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		err := d.streamLoop(ctx)
		if ctx.Err() != nil {
			return
		}
		attempt++
		backoff := calcBackoff(attempt)
		log.Printf("daemon: command stream disconnected (attempt %d): %v; reconnecting in %s", attempt, err, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

// streamLoop opens one StreamCommands stream and processes commands until it
// errors or ctx is canceled. A clean connect resets the caller's backoff.
func (d *Daemon) streamLoop(ctx context.Context) error {
	client, conn, err := d.sender.Dial()
	if err != nil {
		return err
	}
	defer conn.Close()

	// Certificate fetches are bounded by this stream: the server replays
	// active items on the next connect.
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	authCtx := sender.AuthContext(ctx, d.agentID, d.credential)
	stream, err := client.StreamCommands(authCtx, d.streamRequest())
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	log.Printf("daemon: command stream connected to %s", d.cfg.IngestEndpoint)
	d.connected.Store(true)
	defer d.connected.Store(false)
	d.maybeResume(ctx)

	for {
		cmd, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("stream closed by server")
			}
			return fmt.Errorf("recv: %w", err)
		}
		if cmd.GetType() == invv1.CommandType_COMMAND_TYPE_CERTIFICATE {
			d.enqueueCertificate(sctx, cmd)
			continue
		}
		d.handleCommand(ctx, cmd)
	}
}

// collectAndSubmit collects a fresh inventory and submits it.
func (d *Daemon) collectAndSubmit(ctx context.Context) error {
	inv, err := collector.CollectWith(ctx, collector.OptionsFrom(d.cfg))
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}
	return d.submit(ctx, inv)
}

func (d *Daemon) submit(ctx context.Context, inv store.Inventory) error {
	snapID, err := d.sender.Submit(ctx, d.agentID, d.credential, inv)
	if err != nil {
		return err
	}
	log.Printf("daemon: submitted snapshot %s", snapID)
	d.submitted.Store(true)
	d.maybeResume(ctx)
	return nil
}

// --- enrollment persistence ---

func (d *Daemon) loadEnrollment() (agentID, credential string, ok bool) {
	if d.cfg.StateFile == "" || d.cfg.CredentialFile == "" {
		return "", "", false
	}
	sb, err := os.ReadFile(d.cfg.StateFile) // #nosec G304 -- operator-supplied state path
	if err != nil {
		return "", "", false
	}
	var st state
	if err := json.Unmarshal(sb, &st); err != nil || st.AgentID == "" {
		return "", "", false
	}
	cb, err := os.ReadFile(d.cfg.CredentialFile) // #nosec G304 -- operator-supplied credential path
	if err != nil {
		return "", "", false
	}
	cred := strings.TrimSpace(string(cb))
	if cred == "" {
		return "", "", false
	}
	return st.AgentID, cred, true
}

func (d *Daemon) saveEnrollment(agentID, credential string) error {
	if d.cfg.CredentialFile == "" || d.cfg.StateFile == "" {
		return errors.New("credential_file and state_file are required to persist enrollment")
	}
	if err := writeFileSecure(d.cfg.CredentialFile, []byte(credential+"\n"), 0o600); err != nil {
		return err
	}
	sb, err := json.Marshal(state{AgentID: agentID})
	if err != nil {
		return err
	}
	return writeFileSecure(d.cfg.StateFile, sb, 0o600)
}

func (d *Daemon) readToken() (string, error) {
	if d.cfg.TokenFile == "" {
		return "", errors.New("no persisted credential and token_file is empty")
	}
	b, err := os.ReadFile(d.cfg.TokenFile) // #nosec G304 -- operator-supplied token path
	if err != nil {
		return "", fmt.Errorf("read token_file: %w", err)
	}
	token := strings.TrimSpace(string(b))
	if token == "" {
		return "", errors.New("token_file is empty")
	}
	return token, nil
}

// fileExists reports whether path names an existing file ("" does not).
func fileExists(path string) bool {
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// readAutoKey reads the auto-enrollment key secret (feature 029).
func (d *Daemon) readAutoKey() (string, error) {
	if fi, err := os.Stat(d.cfg.AutoEnroll.KeyFile); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		log.Printf("daemon: warning: %s is readable by other users (mode %o); restrict it to 0600", d.cfg.AutoEnroll.KeyFile, fi.Mode().Perm())
	}
	b, err := os.ReadFile(d.cfg.AutoEnroll.KeyFile) // #nosec G304 -- operator-supplied key path
	if err != nil {
		return "", fmt.Errorf("read auto_enroll.key_file: %w", err)
	}
	key := strings.TrimSpace(string(b))
	if key == "" {
		return "", errors.New("auto_enroll.key_file is empty")
	}
	return key, nil
}

func writeFileSecure(path string, data []byte, perm os.FileMode) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, perm)
}

func calcBackoff(attempt int) time.Duration {
	d := baseBackoff << (attempt - 1)
	if d > maxBackoff || d <= 0 {
		d = maxBackoff
	}
	return d
}
