// Package daemon runs the endpoint agent's long-lived loop: it enrolls once
// (persisting the issued agent id and credential), submits a collected inventory
// at startup and on the configured interval, and holds a reconnecting
// StreamCommands stream that submits immediately on a refresh command.
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
	"strings"
	"time"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/collector"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

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
}

// New builds a Daemon for the given agent configuration and version.
func New(cfg config.AgentConfig, version string) *Daemon {
	return &Daemon{
		cfg: cfg,
		sender: sender.New(cfg.IngestEndpoint, sender.Options{
			Insecure: cfg.Insecure, CAFile: cfg.CAFile, ServerName: cfg.ServerName,
		}),
		version: version,
	}
}

// Run performs enroll-if-needed, an initial submit, then runs the periodic
// submit loop and the reconnecting command stream until ctx is canceled.
func (d *Daemon) Run(ctx context.Context) error {
	inv, err := collector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("daemon: initial collect: %w", err)
	}
	if err := d.ensureEnrolled(ctx, inv.Identity); err != nil {
		return fmt.Errorf("daemon: enroll: %w", err)
	}
	if err := d.submit(ctx, inv); err != nil {
		log.Printf("daemon: initial submit failed: %v", err)
	} else {
		log.Println("daemon: initial inventory submitted; entering daemon mode")
	}

	go d.periodicLoop(ctx)
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

// ensureEnrolled loads a persisted agent id + credential, or consumes the
// enrollment token to obtain and persist a fresh one.
func (d *Daemon) ensureEnrolled(ctx context.Context, ident store.Identity) error {
	if id, cred, ok := d.loadEnrollment(); ok {
		d.agentID, d.credential = id, cred
		log.Printf("daemon: using persisted credential for agent %s", id)
		return nil
	}

	token, err := d.readToken()
	if err != nil {
		return err
	}
	id, cred, err := d.sender.Enroll(ctx, token, ident, d.version)
	if err != nil {
		return err
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

	authCtx := sender.AuthContext(ctx, d.agentID, d.credential)
	stream, err := client.StreamCommands(authCtx, &invv1.StreamRequest{
		AgentId:      d.agentID,
		AgentVersion: d.version,
	})
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}
	log.Printf("daemon: command stream connected to %s", d.cfg.IngestEndpoint)

	for {
		cmd, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("stream closed by server")
			}
			return fmt.Errorf("recv: %w", err)
		}
		switch cmd.GetType() {
		case invv1.CommandType_COMMAND_TYPE_REFRESH:
			log.Printf("daemon: refresh command %s received", cmd.GetCommandId())
			if err := d.collectAndSubmit(ctx); err != nil {
				log.Printf("daemon: refresh submit failed: %v", err)
			} else {
				log.Println("daemon: refresh complete; inventory re-submitted")
			}
		default:
			log.Printf("daemon: ignoring unknown command type %d (id %s)", cmd.GetType(), cmd.GetCommandId())
		}
	}
}

// collectAndSubmit collects a fresh inventory and submits it.
func (d *Daemon) collectAndSubmit(ctx context.Context) error {
	inv, err := collector.Collect(ctx)
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
