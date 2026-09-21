// Command inventorysvc runs the inventory service. `inventorysvc -config <path>`
// starts the service (applying migrations); `inventorysvc bootstrap -config
// <path>` applies migrations and exits.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/go-freya/freya/services/inventory/internal/app"
	"github.com/go-freya/freya/services/inventory/internal/config"
	"github.com/go-freya/freya/services/inventory/internal/store"
	"github.com/go-freya/freya/services/inventory/ui"
)

func main() {
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
	fs := flag.NewFlagSet("inventorysvc", flag.ContinueOnError)
	path := fs.String("config", "deploy/container.yaml", "config file path")
	if err := fs.Parse(args); err != nil {
		return config.Config{}, err
	}
	cfg, err := config.Load(*path)
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
