package app

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/moduledelivery"
)

// moduleMetrics observes module deliveries on the framework meter: item
// transitions by state and reason, refused requests by reason, and source
// renders (latency and outcome).
type moduleMetrics struct {
	transitions metric.Int64Counter
	refused     metric.Int64Counter
	source      metric.Float64Histogram
}

func newModuleMetrics(m metric.Meter) (*moduleMetrics, error) {
	t, err := m.Int64Counter("inventory.module_deliveries", metric.WithDescription("Module delivery item transitions by state and reason"))
	if err != nil {
		return nil, err
	}
	r, err := m.Int64Counter("inventory.module_delivery_refusals", metric.WithDescription("Refused module fetches, reports and requests by reason"))
	if err != nil {
		return nil, err
	}
	s, err := m.Float64Histogram("inventory.module_delivery_source_seconds", metric.WithUnit("s"),
		metric.WithDescription("ModuleBundleSource/RenderModuleBundle latency by outcome"))
	if err != nil {
		return nil, err
	}
	return &moduleMetrics{transitions: t, refused: r, source: s}, nil
}

func (c *moduleMetrics) Transition(state, reason string) {
	c.transitions.Add(context.Background(), 1, metric.WithAttributes(attribute.String("state", state), attribute.String("reason", reason)))
}

func (c *moduleMetrics) Refused(reason string) {
	c.refused.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (c *moduleMetrics) Source(d time.Duration, outcome string) {
	c.source.Record(context.Background(), d.Seconds(), metric.WithAttributes(attribute.String("outcome", outcome)))
}

// buildModuleDelivery wires the module delivery relay (feature 037). It is
// always built so the mesh service and the ingest edge answer
// "disabled_on_server" while module_delivery.enabled is false; only an
// enabled relay connects to the bundle source (the gateway's
// ModuleBundleSource, SPIFFE mTLS over the mesh).
func (a *App) buildModuleDelivery(ctx context.Context, instanceID string) (*moduledelivery.Service, error) {
	cfg := a.Cfg.ModuleDelivery
	var src moduledelivery.Source
	if cfg.Enabled {
		conn, err := a.Freya.Client(ctx, cfg.SourceService)
		if err != nil {
			return nil, fmt.Errorf("module_delivery: dial %s: %w", cfg.SourceService, err)
		}
		src = invv1.NewModuleBundleSourceClient(conn)
	}
	svc := moduledelivery.New(a.Repo, src, a.Registry, moduledelivery.Config{Enabled: cfg.Enabled, ReportTimeout: cfg.ReportTimeout(),
		SourceTimeout: cfg.SourceTimeout(), InstanceID: instanceID})
	if m := a.Freya.Metrics(); m != nil {
		mm, err := newModuleMetrics(m.Meter("github.com/go-tangra/go-tangra-inventory/v4"))
		if err != nil {
			return nil, err
		}
		svc.SetMetrics(mm)
	}
	a.Log.Info("module delivery", "enabled", cfg.Enabled, "sources", cfg.Sources, "source_service", cfg.SourceService)
	return svc, nil
}

// moduleWorker sweeps module delivery items every minute: fetched items
// without a report fail, items past their expiry expire.
func (a *App) moduleWorker(svc *moduledelivery.Service) func(context.Context) {
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
					a.Log.Warn("module delivery: sweep", "err", err)
				} else if e+f > 0 {
					a.Log.Info("module delivery: swept", "expired", e, "failed", f)
				}
			}
		}
	}
}
