package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"runtime"
	"time"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/daemon"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/selfupdate"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrader"
)

// Exit codes of `inventory-agent update` (contracts/agent-cli.md).
const (
	exitOK        = 0
	exitError     = 1
	exitBusy      = 2
	exitAvailable = 10
)

// updater is what the update command needs of selfupdate.Updater.
type updater interface {
	Check(ctx context.Context) (selfupdate.CheckResult, error)
	Update(ctx context.Context) (selfupdate.CheckResult, error)
}

// cliDeps are the environment of the upgrade commands (tests replace them).
type cliDeps struct {
	isAdmin     func() bool
	credentials func(cfg config.AgentConfig) (agentID, credential string, ok bool)
	newUpdater  func(cfg config.AgentConfig, configPath, agentID, credential string) (updater, agentrelease.Platform, error)
	apply       func(ctx context.Context, cfg config.AgentConfig, configPath, statePath string) error
}

func defaultDeps() cliDeps {
	return cliDeps{
		isAdmin: isAdmin,
		credentials: func(cfg config.AgentConfig) (string, string, bool) {
			return daemon.New(cfg, version).Credentials()
		},
		newUpdater: func(cfg config.AgentConfig, configPath, agentID, credential string) (updater, agentrelease.Platform, error) {
			u, p, err := buildUpdater(cfg, configPath, agentID, credential)
			return u, p, err
		},
		apply: applyUpgrade,
	}
}

func isAdmin() bool {
	if runtime.GOOS == "windows" {
		return true // the SCM and the file ACLs decide; the service runs as SYSTEM
	}
	return os.Geteuid() == 0
}

// stagingDir is the configured staging directory or the platform default.
func stagingDir(cfg config.AgentConfig) string {
	if cfg.Upgrade.StagingDir != "" {
		return cfg.Upgrade.StagingDir
	}
	return upgrader.DefaultStagingDir()
}

// buildUpdater wires the self-upgrade core of an enrolled agent: the
// compiled keyring, the detected platform, the ingest connection as the
// only upgrade source and the OS glue.
func buildUpdater(cfg config.AgentConfig, configPath, agentID, credential string) (*selfupdate.Updater, agentrelease.Platform, error) {
	keys, err := agentrelease.Compiled()
	if err != nil {
		return nil, agentrelease.Platform{}, err
	}
	exe, err := upgrader.Executable()
	if err != nil {
		return nil, agentrelease.Platform{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	runner := upgrader.ExecRunner{}
	p := upgrader.Platform(ctx, runner)
	client := daemon.New(cfg, version).Sender().UpgradeClient(agentID, credential)
	u := selfupdate.New(selfupdate.Config{Version: version, Platform: p, Keys: keys, StagingDir: stagingDir(cfg), Executable: exe,
		ConfirmTimeout: cfg.ConfirmTimeout()}, client, upgrader.OSFS{}, upgrader.NewInstaller(runner, configPath), selfupdate.RealClock(),
		slog.New(slog.NewTextHandler(log.Writer(), nil)))
	return u, p, nil
}

// runUpdate is `inventory-agent update [-check]`: it checks the enrolled
// platform for a newer agent and (without -check) upgrades the same way as a
// server-pushed request. Exit 0 up to date or upgrade started, 10 upgrade
// available (-check), 2 another upgrade in progress, 1 error.
func runUpdate(args []string, out io.Writer, deps cliDeps) int {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(out)
	check := fs.Bool("check", false, "only report whether an upgrade is available")
	configPath := fs.String("config", "", "path to agent config YAML")
	ingest := fs.String("ingest", "", "ingest endpoint host:port (overrides config)")
	insecure := fs.Bool("insecure", false, "plaintext connection (development only)")
	caFile := fs.String("ca-file", "", "PEM CA bundle of the ingest server")
	serverName := fs.String("server-name", "", "name to verify in the ingest server certificate")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if !deps.isAdmin() {
		fmt.Fprintln(out, "error: update must run as root (Administrator on Windows)")
		return exitError
	}
	cfg, err := resolveConfig(*configPath, flags{ingest: *ingest, insecure: *insecure, caFile: *caFile, serverName: *serverName})
	if err != nil {
		fmt.Fprintf(out, "error: config: %v\n", err)
		return exitError
	}
	id, cred, ok := deps.credentials(cfg)
	if !ok {
		fmt.Fprintln(out, "error: update requires an enrolled agent (credential_file)")
		return exitError
	}
	u, p, err := deps.newUpdater(cfg, *configPath, id, cred)
	if err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return exitError
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if *check {
		res, err := u.Check(ctx)
		if err != nil {
			fmt.Fprintf(out, "error: %v\n", err)
			return exitError
		}
		if !res.Available {
			fmt.Fprintf(out, "current %s, up to date\n", version)
			return exitOK
		}
		fmt.Fprintf(out, "current %s, available %s (%s/%s %s)\n", version, res.TargetVersion, p.OS, p.Arch, p.InstallType)
		return exitAvailable
	}
	res, err := u.Update(ctx)
	switch {
	case errors.Is(err, selfupdate.ErrBusy):
		fmt.Fprintln(out, "error: another upgrade is in progress")
		return exitBusy
	case err != nil:
		fmt.Fprintf(out, "error: %v\n", err)
		return exitError
	case !res.Available:
		fmt.Fprintf(out, "current %s, up to date\n", version)
		return exitOK
	}
	fmt.Fprintf(out, "upgrade to %s started (request %s); see the agent list or %s/state.json\n", res.TargetVersion, res.RequestID, stagingDir(cfg))
	return exitOK
}

// runApply is `upgrade-apply -state <file> [-config <file>]`, the helper the
// agent starts outside its service to install a verified upgrade.
func runApply(args []string, out io.Writer, deps cliDeps) int {
	fs := flag.NewFlagSet("upgrade-apply", flag.ContinueOnError)
	fs.SetOutput(out)
	state := fs.String("state", "", "upgrade state file")
	configPath := fs.String("config", "", "path to agent config YAML")
	if err := fs.Parse(args); err != nil || *state == "" {
		fmt.Fprintln(out, "usage: inventory-agent upgrade-apply -state <file> [-config <file>]")
		return exitError
	}
	if !deps.isAdmin() {
		fmt.Fprintln(out, "error: upgrade-apply must run as root")
		return exitError
	}
	cfg, err := resolveConfig(*configPath, flags{})
	if err != nil {
		fmt.Fprintf(out, "error: config: %v\n", err)
		return exitError
	}
	if err := deps.apply(context.Background(), cfg, *configPath, *state); err != nil {
		fmt.Fprintf(out, "error: %v\n", err)
		return exitError
	}
	return exitOK
}

// applyUpgrade runs the helper; the state file carries everything but the
// keyring (compiled in) and the staging directory (configured).
func applyUpgrade(ctx context.Context, cfg config.AgentConfig, configPath, statePath string) error {
	keys, err := agentrelease.Compiled()
	if err != nil {
		return err
	}
	u := selfupdate.New(selfupdate.Config{Version: version, Keys: keys, StagingDir: stagingDir(cfg)}, nil, upgrader.OSFS{},
		upgrader.NewInstaller(upgrader.ExecRunner{}, configPath), selfupdate.RealClock(), slog.New(slog.NewTextHandler(os.Stderr, nil)))
	return u.Apply(ctx, statePath)
}
