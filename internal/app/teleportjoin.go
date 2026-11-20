package app

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/teleportjoin"
)

// teleportMetrics observes Teleport joins on the framework meter: item
// transitions by state and reason, refused requests by reason, and source
// renders (latency and outcome).
type teleportMetrics struct {
	transitions metric.Int64Counter
	refused     metric.Int64Counter
	source      metric.Float64Histogram
}

func newTeleportMetrics(m metric.Meter) (*teleportMetrics, error) {
	t, err := m.Int64Counter("inventory.teleport_joins", metric.WithDescription("Teleport join item transitions by state and reason"))
	if err != nil {
		return nil, err
	}
	r, err := m.Int64Counter("inventory.teleport_join_refusals", metric.WithDescription("Refused Teleport join fetches, reports and requests by reason"))
	if err != nil {
		return nil, err
	}
	s, err := m.Float64Histogram("inventory.teleport_join_source_seconds", metric.WithUnit("s"),
		metric.WithDescription("TeleportJoinSource/RenderTeleportJoin latency by outcome"))
	if err != nil {
		return nil, err
	}
	return &teleportMetrics{transitions: t, refused: r, source: s}, nil
}

func (c *teleportMetrics) Transition(state, reason string) {
	c.transitions.Add(context.Background(), 1, metric.WithAttributes(attribute.String("state", state), attribute.String("reason", reason)))
}

func (c *teleportMetrics) Refused(reason string) {
	c.refused.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (c *teleportMetrics) Source(d time.Duration, outcome string) {
	c.source.Record(context.Background(), d.Seconds(), metric.WithAttributes(attribute.String("outcome", outcome)))
}

// buildTeleportJoin wires the Teleport join relay (feature 038). It is
// always built so the mesh service and the ingest edge answer
// "disabled_on_server" while teleport_join.enabled is false; only an
// enabled relay connects to the join source (the teleport module's
// TeleportJoinSource, SPIFFE mTLS over the mesh).
func (a *App) buildTeleportJoin(ctx context.Context, instanceID string) (*teleportjoin.Service, error) {
	cfg := a.Cfg.TeleportJoin
	var src teleportjoin.Source
	if cfg.Enabled {
		conn, err := a.Freya.Client(ctx, cfg.SourceService)
		if err != nil {
			return nil, fmt.Errorf("teleport_join: dial %s: %w", cfg.SourceService, err)
		}
		src = invv1.NewTeleportJoinSourceClient(conn)
	}
	svc := teleportjoin.New(a.Repo, src, a.Registry, teleportjoin.Config{Enabled: cfg.Enabled, ReportTimeout: cfg.ReportTimeout(),
		SourceTimeout: cfg.SourceTimeout(), SourceService: cfg.SourceService, InstanceID: instanceID})
	svc.SetLogger(a.Log)
	if m := a.Freya.Metrics(); m != nil {
		tm, err := newTeleportMetrics(m.Meter("github.com/go-tangra/go-tangra-inventory/v4"))
		if err != nil {
			return nil, err
		}
		svc.SetMetrics(tm)
	}
	a.Log.Info("teleport join", "enabled", cfg.Enabled, "sources", cfg.Sources, "source_service", cfg.SourceService)
	return svc, nil
}

// teleportWorker sweeps Teleport join items every minute: fetched and
// installing items without a report fail, unfetched items past their
// expiry expire.
func (a *App) teleportWorker(svc *teleportjoin.Service) func(context.Context) {
	return func(ctx context.Context) {
		if !svc.Enabled() {
			return
		}
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if e, f, err := svc.Sweep(ctx); err != nil {
					a.Log.Warn("teleport join: sweep", "err", err)
				} else if e+f > 0 {
					a.Log.Info("teleport join: swept", "expired", e, "failed", f)
				}
			}
		}
	}
}
