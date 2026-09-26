package app

import (
	"context"
	"log/slog"
	"time"

	"google.golang.org/grpc"

	"github.com/go-tangra/go-tangra-inventory/v4/pkg/inventorymanifest"
)

// Registration cadence: retry until auth accepts, then refresh so tenants
// created later receive the permissions, roles and grants.
const (
	registerRetry   = 5 * time.Second
	registerPeriod  = 5 * time.Minute
	registerTimeout = 15 * time.Second
)

// seedLoop registers the module's permissions, module roles and built-in
// role grants with auth (feature 019). The gateway registers the
// permissions from the manifest for routing; only the module knows its roles
// and grants, so it pushes them to auth here.
func (a *App) seedLoop(ctx context.Context) {
	dial := func(ctx context.Context) (grpc.ClientConnInterface, error) {
		conn, err := a.Freya.Client(ctx, "auth")
		if err != nil {
			return nil, err
		}
		return conn, nil
	}
	registerLoop(ctx, a.Log, dial, registerRetry, registerPeriod)
}

// registerLoop registers at start (retrying every retry until it succeeds)
// and then every period until ctx ends.
func registerLoop(ctx context.Context, log *slog.Logger, dial func(context.Context) (grpc.ClientConnInterface, error), retry, period time.Duration) {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	reg := inventorymanifest.Registration()
	register := func() error {
		conn, err := dial(ctx)
		if err != nil {
			return err
		}
		cctx, cancel := context.WithTimeout(ctx, registerTimeout)
		defer cancel()
		_, err = reg.Register(cctx, conn, log)
		return err
	}
	for ctx.Err() == nil {
		err := register()
		if err == nil {
			break
		}
		log.Warn("auth registration failed; retrying", "err", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(retry):
		}
	}
	t := time.NewTicker(period)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := register(); err != nil {
				log.Warn("auth registration", "err", err)
			}
		}
	}
}
