package app

import (
	"context"
	"time"

	"github.com/go-freya/freya/services/inventory/pkg/inventorymanifest"
)

// SeedPermissions registers the module's permissions + built-in role grants with
// the auth service (idempotent). The gateway registers permissions for routing;
// only the module knows the role grants, so it pushes them here.
func (a *App) SeedPermissions(ctx context.Context) error {
	conn, err := a.Freya.Client(ctx, "auth")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return inventorymanifest.SeedPermissions(ctx, conn)
}

// seedLoop seeds at start (retrying until it succeeds) and then every five
// minutes so tenants created later receive the grants.
func (a *App) seedLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if err := a.SeedPermissions(ctx); err == nil {
			break
		} else {
			a.Log.Warn("permission seeding failed; retrying", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Second):
		}
	}
	t := time.NewTicker(5 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := a.SeedPermissions(ctx); err != nil {
				a.Log.Warn("permission seeding", "err", err)
			}
		}
	}
}
