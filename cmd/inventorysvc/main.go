// Command inventorysvc runs the inventory service. `inventorysvc -config <path>`
// starts the service (applying migrations); `inventorysvc bootstrap -config
// <path>` applies migrations and exits; `inventorysvc agent-release import
// -config <path> <dir>` verifies a signed agent release directory (manifest,
// signature, artifacts — e.g. downloaded from the GitHub release for an
// offline site) and stores it for agent self-upgrade.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/app"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/ui"
)

func main() {
	if len(os.Args) > 2 && os.Args[1] == "agent-release" && os.Args[2] == "import" {
		if err := importRelease(os.Args[3:], os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "inventorysvc:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		if err := bootstrap(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "inventorysvc:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "inventorysvc:", err)
		os.Exit(1)
	}
}

func loadConfig(args []string) (config.Config, error) {
	cfg, _, err := loadConfigArgs(args)
	return cfg, err
}

// loadConfigArgs parses -config and returns the remaining arguments.
func loadConfigArgs(args []string) (config.Config, []string, error) {
	fs := flag.NewFlagSet("inventorysvc", flag.ContinueOnError)
	path := fs.String("config", "deploy/container.yaml", "config file path")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, nil, err
	}
	cfg, err := loadConfigFile(*path)
	return cfg, fs.Args(), err
}

func loadConfigFile(path string) (config.Config, error) {
	cfg, err := config.Load(path)
	if err != nil {
		return cfg, err
	}
	for _, w := range cfg.Warnings() {
		fmt.Fprintln(os.Stderr, "inventorysvc: warning:", w)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

func run(args []string) error {
	cfg, err := loadConfig(args)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	opts := app.Options{Migrate: true}
	// Serve the embedded federated UI remote (present only in -tags ui builds).
	if remote, ok := ui.Remote(); ok {
		opts.Remote = remote
	}
	a, err := app.Build(ctx, cfg, opts)
	if err != nil {
		return err
	}
	defer a.Close()
	return a.Run(ctx)
}

func bootstrap(args []string) error {
	cfg, err := loadConfig(args)
	if err != nil {
		return err
	}
	mdsn := cfg.DB.MigrateDSN
	if mdsn == "" {
		mdsn = cfg.DB.DSN
	}
	return store.Migrate(context.Background(), mdsn)
}

// importRelease stores a signed agent release directory (feature 023).
const storeReleaseImport = store.ReleaseImport

func importRelease(args []string, out io.Writer) error {
	cfg, rest, err := loadConfigArgs(args)
	if err != nil {
		return err
	}
	if len(rest) != 1 {
		return fmt.Errorf("usage: inventorysvc agent-release import -config <path> <release directory>")
	}
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.DB.DSN, 2)
	if err != nil {
		return err
	}
	defer st.Close()
	svc, err := app.NewReleases(repodb.New(st), slog.New(slog.NewTextHandler(os.Stderr, nil)), cfg.AgentReleases)
	if err != nil {
		return err
	}
	rel, imported, err := svc.ImportDir(ctx, rest[0], storeReleaseImport)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "agent release %s (key %s): %s\n", rel.Version, rel.KeyID, map[bool]string{true: "imported", false: "already stored"}[imported])
	for _, a := range rel.Artifacts {
		fmt.Fprintf(out, "  %s/%s %s  %s  %d bytes\n", a.OS, a.Arch, a.InstallType, a.File, a.Size)
	}
	return nil
}
