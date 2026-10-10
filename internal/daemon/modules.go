package daemon

import (
	"context"
	"errors"
	"log"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentcerts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentmodules"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// ModuleInstaller is the local module writer (agentmodules.Writer).
type ModuleInstaller interface {
	Install(ctx context.Context, req agentmodules.Request) agentmodules.Result
}

// ModuleClient fetches module items and reports their outcome
// (sender.ModuleClient).
type ModuleClient interface {
	Fetch(ctx context.Context, itemID string) (*invv1.ModuleBundleHeader, []byte, error)
	Report(ctx context.Context, req *invv1.ReportModuleRequest) (bool, error)
}

// WithModules enables module delivery with the local writer (Linux only;
// the caller passes nil elsewhere). The agent announces module.v1 when the
// local configuration enables it and the transport allows it.
func (d *Daemon) WithModules(w ModuleInstaller) *Daemon {
	d.moduleWriter = w
	return d
}

// modulesActive reports whether the agent announces and handles module.v1.
func (d *Daemon) modulesActive() bool {
	return d.moduleWriter != nil && d.cfg.Modules.Announce(d.goos, d.cfg.Insecure)
}

// enqueueModule queues a MODULE command (ids only), ignoring duplicates of
// an item attempt that is queued, running or already reported. Items are
// handled one at a time; a full queue is reported busy.
func (d *Daemon) enqueueModule(ctx context.Context, cmd *invv1.Command) {
	m := cmd.GetModule()
	id := m.GetItemId()
	if id == "" {
		log.Printf("daemon: ignoring module command %s without an item id", cmd.GetCommandId())
		return
	}
	key := certKey(id, m.GetAttempt())
	if !d.mods.claim(key) {
		log.Printf("daemon: module item %s already handled; duplicate command ignored", id)
		return
	}
	select {
	case d.mods.queue <- itemJob{ctx: ctx, itemID: id, key: key, name: m.GetModule()}:
	default:
		log.Printf("daemon: module item %s refused: queue full", id)
		go d.sendModuleReport(ctx, key, &invv1.ReportModuleRequest{ItemId: id, State: store.DeliveryFailed, Reason: store.ReasonBusy,
			HookExitCode: agentcerts.HookNotRun})
	}
}

// moduleLoop handles queued module commands one at a time until ctx ends.
func (d *Daemon) moduleLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-d.mods.queue:
			d.processModule(ctx, j)
		}
	}
}

// processModule fetches, writes and reports one item. The bundle holds
// secrets: it is held only for the write, zeroed afterwards and never
// logged.
func (d *Daemon) processModule(ctx context.Context, j itemJob) {
	if r, ok := d.mods.takeUnsent(j.itemID); ok {
		d.sendModuleReport(ctx, j.key, r)
		return
	}
	failed := func(reason string) {
		d.sendModuleReport(ctx, j.key, &invv1.ReportModuleRequest{ItemId: j.itemID, State: store.DeliveryFailed, Reason: reason,
			HookExitCode: agentcerts.HookNotRun})
	}
	if !d.modulesActive() {
		failed(store.ReasonDisabledLocally)
		return
	}
	h, data, err := d.fetchModule(j.ctx, j.itemID)
	defer clear(data)
	switch {
	case errors.Is(err, sender.ErrBundleTooLarge):
		failed(store.ReasonTooLarge)
		return
	case errors.Is(err, sender.ErrBundleMalformed):
		failed(store.ReasonInvalidBundle)
		return
	case err != nil:
		log.Printf("daemon: module item %s: fetch: %s", j.itemID, status.Code(err))
		switch {
		case status.Code(err) == codes.NotFound:
			d.mods.finish(j.key, j.itemID) // not ours or no longer active
		case transientFetch(err) || j.ctx.Err() != nil:
			d.mods.release(j.key) // replayed on the next connect
		default:
			failed(store.ReasonFetchFailed)
		}
		return
	}
	if h.GetItemId() != j.itemID || h.GetModule() != j.name {
		failed(store.ReasonInvalidBundle)
		return
	}
	res := d.moduleWriter.Install(ctx, agentmodules.Request{ItemID: j.itemID, Module: h.GetModule(), Version: h.GetVersion(),
		Size: h.GetSize(), SHA256: h.GetSha256(), Data: data})
	d.sendModuleReport(ctx, j.key, moduleReportOf(j.itemID, res))
}

// fetchModule calls FetchModuleBundle, retrying transient errors with
// backoff while ctx (the stream) lives.
func (d *Daemon) fetchModule(ctx context.Context, id string) (*invv1.ModuleBundleHeader, []byte, error) {
	client := d.newModuleClient(d.agentID, d.credential)
	for attempt := 1; ; attempt++ {
		h, data, err := client.Fetch(ctx, id)
		if err == nil {
			return h, data, nil
		}
		if !transientFetch(err) || ctx.Err() != nil {
			return nil, nil, err
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(d.mods.backoff(attempt)):
		}
	}
}

// sendModuleReport reports a result; a failed report is kept for the next
// command of the item (the server replays it while the item is active).
func (d *Daemon) sendModuleReport(ctx context.Context, key string, r *invv1.ReportModuleRequest) {
	accepted, err := d.newModuleClient(d.agentID, d.credential).Report(ctx, r)
	if err != nil {
		log.Printf("daemon: module item %s: report %s failed: %v", r.GetItemId(), r.GetState(), err)
		d.mods.keepUnsent(key, r.GetItemId(), r)
		return
	}
	if !accepted {
		log.Printf("daemon: module item %s: report %s ignored by the server", r.GetItemId(), r.GetState())
	}
	d.mods.finish(key, r.GetItemId())
}

// moduleReportOf maps a writer result to the report (detail bounded, never
// hook output).
func moduleReportOf(itemID string, res agentmodules.Result) *invv1.ReportModuleRequest {
	detail := res.Detail
	if len(detail) > maxReportDetail {
		detail = detail[:maxReportDetail]
	}
	return &invv1.ReportModuleRequest{ItemId: itemID, State: res.State, Reason: res.Reason,
		HookExitCode: int32(res.HookExitCode), Detail: detail} // #nosec G115 -- -1..256
}
