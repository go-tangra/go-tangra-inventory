package daemon

import (
	"context"
	"log"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/agentteleport"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TeleportJoiner is the local Teleport installer (agentteleport.Installer).
type TeleportJoiner interface {
	Preflight(ctx context.Context) string
	Join(ctx context.Context, m agentteleport.Material, installing func() bool) agentteleport.Result
}

// TeleportClient fetches join material and reports join progress
// (sender.TeleportClient).
type TeleportClient interface {
	Fetch(ctx context.Context, itemID string) (*invv1.TeleportJoinMaterial, error)
	Report(ctx context.Context, req *invv1.ReportTeleportJoinRequest) (bool, error)
}

// WithTeleport sets the local Teleport installer (Linux with systemd; nil
// elsewhere: joins are then reported unsupported_platform).
func (d *Daemon) WithTeleport(j TeleportJoiner) *Daemon {
	d.teleport = j
	return d
}

// enqueueTeleport queues a TELEPORT_JOIN command (ids only), ignoring
// duplicates of an item attempt that is queued, running or already
// reported. One join runs at a time: another item meanwhile is reported
// busy.
func (d *Daemon) enqueueTeleport(ctx context.Context, cmd *invv1.Command) {
	tj := cmd.GetTeleportJoin()
	id := tj.GetItemId()
	if id == "" {
		log.Printf("daemon: ignoring teleport join command %s without an item id", cmd.GetCommandId())
		return
	}
	key := certKey(id, tj.GetAttempt())
	if !d.tps.claim(key) {
		log.Printf("daemon: teleport join item %s already handled; duplicate command ignored", id)
		return
	}
	if !d.tpBusy.CompareAndSwap(false, true) {
		log.Printf("daemon: teleport join item %s refused: another join is running", id)
		go d.sendTeleportReport(ctx, key, &invv1.ReportTeleportJoinRequest{ItemId: id, State: store.DeliveryFailed, Reason: store.ReasonBusy})
		return
	}
	d.tps.queue <- itemJob{ctx: ctx, itemID: id, key: key}
}

// teleportLoop handles queued joins one at a time until ctx ends.
func (d *Daemon) teleportLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-d.tps.queue:
			d.processTeleport(ctx, j)
		}
	}
}

// processTeleport fetches the join material, joins and reports. The token
// is held only for the join, wiped afterwards and never logged.
func (d *Daemon) processTeleport(ctx context.Context, j itemJob) {
	defer d.tpBusy.Store(false)
	if r, ok := d.tps.takeUnsent(j.itemID); ok {
		d.sendTeleportReport(ctx, j.key, r)
		return
	}
	failed := func(reason string) {
		d.sendTeleportReport(ctx, j.key, &invv1.ReportTeleportJoinRequest{ItemId: j.itemID, State: store.DeliveryFailed, Reason: reason})
	}
	switch {
	case !d.cfg.Teleport.Accepts(d.cfg.Insecure):
		failed(store.ReasonDisabledLocally)
		return
	case d.teleport == nil || d.goos != "linux":
		failed(store.ReasonUnsupportedPlatform)
		return
	}
	// Refuse before fetching: the single-use token is not spent on a host
	// that cannot use it.
	if r := d.teleport.Preflight(ctx); r != "" {
		failed(r)
		return
	}
	pb, err := d.fetchTeleport(j.ctx, j.itemID)
	if err != nil {
		log.Printf("daemon: teleport join item %s: fetch: %s", j.itemID, status.Code(err))
		switch {
		case status.Code(err) == codes.NotFound:
			d.tps.finish(j.key, j.itemID) // not ours or no longer active
		case transientFetch(err) || j.ctx.Err() != nil:
			d.tps.release(j.key) // replayed on the next connect
		default:
			failed(store.ReasonFetchFailed)
		}
		return
	}
	m := agentteleport.FromPB(pb)
	if m.ItemID != j.itemID {
		m.Wipe()
		failed(store.ReasonInvalidMaterial)
		return
	}
	jctx, cancel := context.WithTimeout(ctx, d.cfg.Teleport.Timeout())
	defer cancel()
	client := d.newTeleportClient(d.agentID, d.credential)
	res := d.teleport.Join(jctx, m, func() bool {
		accepted, err := client.Report(ctx, &invv1.ReportTeleportJoinRequest{ItemId: j.itemID, State: store.TeleportInstalling})
		if err != nil {
			log.Printf("daemon: teleport join item %s: report installing failed: %v", j.itemID, err)
			return true // the outcome report follows; the server accepts it from fetched
		}
		return accepted
	})
	if res.Aborted {
		log.Printf("daemon: teleport join item %s no longer accepted by the server; not installed", j.itemID)
		d.tps.finish(j.key, j.itemID)
		return
	}
	d.sendTeleportReport(ctx, j.key, teleportReportOf(j.itemID, res))
}

// fetchTeleport calls FetchTeleportJoin, retrying transient errors with
// backoff while ctx (the stream) lives.
func (d *Daemon) fetchTeleport(ctx context.Context, id string) (*invv1.TeleportJoinMaterial, error) {
	client := d.newTeleportClient(d.agentID, d.credential)
	for attempt := 1; ; attempt++ {
		m, err := client.Fetch(ctx, id)
		if err == nil {
			return m, nil
		}
		if !transientFetch(err) || ctx.Err() != nil {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d.tps.backoff(attempt)):
		}
	}
}

// sendTeleportReport reports an outcome; a failed report is kept for the
// next command of the item (the server replays it while the item is
// active).
func (d *Daemon) sendTeleportReport(ctx context.Context, key string, r *invv1.ReportTeleportJoinRequest) {
	accepted, err := d.newTeleportClient(d.agentID, d.credential).Report(ctx, r)
	if err != nil {
		log.Printf("daemon: teleport join item %s: report %s failed: %v", r.GetItemId(), r.GetState(), err)
		d.tps.keepUnsent(key, r.GetItemId(), r)
		return
	}
	if !accepted {
		log.Printf("daemon: teleport join item %s: report %s ignored by the server", r.GetItemId(), r.GetState())
	}
	d.tps.finish(key, r.GetItemId())
}

// teleportReportOf maps an installer result to the report (detail bounded;
// the installer never puts the token or command output in it).
func teleportReportOf(itemID string, res agentteleport.Result) *invv1.ReportTeleportJoinRequest {
	detail := res.Detail
	if len(detail) > maxReportDetail {
		detail = detail[:maxReportDetail]
	}
	return &invv1.ReportTeleportJoinRequest{ItemId: itemID, State: res.State, Reason: res.Reason, NodeHostUuid: res.NodeHostUUID, Detail: detail}
}
