package app

import (
	"context"
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentrelease"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/releases"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/upgrades"
)

// upgradeMetrics counts upgrade transitions (inventory.upgrades) on the
// framework meter, rendered on the admin listener.
type upgradeMetrics struct{ transitions metric.Int64Counter }

func newUpgradeMetrics(m metric.Meter) (*upgradeMetrics, error) {
	c, err := m.Int64Counter("inventory.upgrades", metric.WithDescription("Agent upgrade request transitions by state and reason"))
	if err != nil {
		return nil, err
	}
	return &upgradeMetrics{transitions: c}, nil
}

func (u *upgradeMetrics) Transition(state, reason string) {
	u.transitions.Add(context.Background(), 1, metric.WithAttributes(attribute.String("state", state), attribute.String("reason", reason)))
}

// repoRecorder writes audit events synchronously through the repo (release
// imports are rare and must not be dropped).
type repoRecorder struct{ st audit.Store }

func (r repoRecorder) Record(ctx context.Context, e audit.Event) error {
	row, err := audit.Row(e, time.Now().UTC())
	if err != nil {
		return err
	}
	return r.st.AppendAudit(ctx, row)
}

// NewReleases builds the release service over the compiled keyring (also
// used by `inventorysvc agent-release import`).
func NewReleases(st releases.Store, log *slog.Logger, cfg config.AgentReleases) (*releases.Service, error) {
	keys, err := agentrelease.Compiled()
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		log.Warn("agent releases: no release signing key compiled in; agent upgrades are unavailable (AGENT_RELEASE_PUBLIC_KEYS)")
	}
	return releases.New(st, keys, repoRecorder{st}, log, releases.Config{BundleDir: cfg.BundleDir, KeepVersions: cfg.KeepVersions, ChunkBytes: cfg.ChunkBytes}), nil
}

// buildReleases builds the service's release service.
func (a *App) buildReleases() (*releases.Service, error) {
	return NewReleases(a.Repo, a.Log, a.Cfg.AgentReleases)
}

// upgradeWorker seeds the bundled releases once, then every minute sweeps
// expired and stalled upgrade requests and runs the tenants' automatic
// upgrade policies (per-tenant lock across replicas), and applies release
// retention hourly.
func (a *App) upgradeWorker(rel *releases.Service, upg *upgrades.Service) func(context.Context) {
	return func(ctx context.Context) {
		n := rel.SeedBundles(ctx)
		a.Log.Info("agent releases seeded", "releases", n, "current_version", rel.CurrentVersion(ctx), "keys", rel.KeyIDs())
		sweep := time.NewTicker(time.Minute)
		prune := time.NewTicker(time.Hour)
		defer sweep.Stop()
		defer prune.Stop()
		if _, err := rel.Prune(ctx); err != nil {
			a.Log.Warn("agent releases: retention", "err", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-sweep.C:
				if e, f, err := upg.Sweep(ctx); err != nil {
					a.Log.Warn("agent upgrades: sweep", "err", err)
				} else if e+f > 0 {
					a.Log.Info("agent upgrades: swept", "expired", e, "failed", f)
				}
				if n, err := upg.RunPolicies(ctx); err != nil {
					a.Log.Warn("agent upgrades: policy run", "err", err)
				} else if n > 0 {
					a.Log.Info("agent upgrades: policy requests created", "requests", n)
				}
			case <-prune.C:
				if _, err := rel.Prune(ctx); err != nil {
					a.Log.Warn("agent releases: retention", "err", err)
				}
			}
		}
	}
}
