package app

import (
	"context"
	"fmt"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/certdelivery"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/lcmclient"
)

// certMetrics observes certificate deliveries on the framework meter:
// item transitions by state and reason, refused requests by reason, and lcm
// downloads (latency and outcome).
type certMetrics struct {
	transitions metric.Int64Counter
	refused     metric.Int64Counter
	lcm         metric.Float64Histogram
}

func newCertMetrics(m metric.Meter) (*certMetrics, error) {
	t, err := m.Int64Counter("inventory.cert_deliveries", metric.WithDescription("Certificate delivery item transitions by state and reason"))
	if err != nil {
		return nil, err
	}
	r, err := m.Int64Counter("inventory.cert_delivery_refusals", metric.WithDescription("Refused certificate fetches, reports and requests by reason"))
	if err != nil {
		return nil, err
	}
	l, err := m.Float64Histogram("inventory.cert_delivery_lcm_seconds", metric.WithUnit("s"),
		metric.WithDescription("lcm Certificates/Download latency by outcome"))
	if err != nil {
		return nil, err
	}
	return &certMetrics{transitions: t, refused: r, lcm: l}, nil
}

func (c *certMetrics) Transition(state, reason string) {
	c.transitions.Add(context.Background(), 1, metric.WithAttributes(attribute.String("state", state), attribute.String("reason", reason)))
}

func (c *certMetrics) Refused(reason string) {
	c.refused.Add(context.Background(), 1, metric.WithAttributes(attribute.String("reason", reason)))
}

func (c *certMetrics) LCM(d time.Duration, outcome string) {
	c.lcm.Record(context.Background(), d.Seconds(), metric.WithAttributes(attribute.String("outcome", outcome)))
}

// buildCertDelivery wires the certificate delivery relay (feature 033). It
// is always built so the mesh service and the ingest edge answer
// "certificate delivery is disabled" while cert_delivery.enabled is false;
// only an enabled relay connects to lcm.
func (a *App) buildCertDelivery(ctx context.Context, pub events.Publisher, instanceID string) (*certdelivery.Service, error) {
	cfg := a.Cfg.CertDelivery
	var lcm certdelivery.LCM
	if cfg.Enabled {
		c, err := lcmclient.Dial(ctx, a.Freya, cfg.LCMService, cfg.LCMTimeout())
		if err != nil {
			return nil, fmt.Errorf("cert_delivery: %w", err)
		}
		lcm = c
	}
	svc := certdelivery.New(a.Repo, lcm, a.Registry, pub, certdelivery.Config{Enabled: cfg.Enabled, PendingTTL: cfg.PendingTTL(),
		ReportTimeout: cfg.ReportTimeout(), InstanceID: instanceID})
	if m := a.Freya.Metrics(); m != nil {
		cm, err := newCertMetrics(m.Meter("github.com/go-tangra/go-tangra-inventory/v4"))
		if err != nil {
			return nil, err
		}
		svc.SetMetrics(cm)
	}
	a.Log.Info("certificate delivery", "enabled", cfg.Enabled, "sources", cfg.Sources, "lcm_service", cfg.LCMService)
	return svc, nil
}

// certWorker sweeps certificate delivery items every minute: fetched items
// without a report fail, items past their delivery window expire.
func (a *App) certWorker(svc *certdelivery.Service) func(context.Context) {
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
					a.Log.Warn("certificate delivery: sweep", "err", err)
				} else if e+f > 0 {
					a.Log.Info("certificate delivery: swept", "expired", e, "failed", f)
				}
			}
		}
	}
}
