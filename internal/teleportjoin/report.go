package teleportjoin

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/audit"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// Report is an agent's result for one item.
type Report struct {
	ItemID       string
	State        string // installing | joined | failed
	Reason       string // agent reason code (failed only)
	NodeHostUUID string // joined only: /var/lib/teleport/host_uuid
	Detail       string // <= 256 bytes after sanitising; never command output or the token
}

var reportEvents = map[string]struct {
	t       audit.EventType
	outcome string
}{
	store.TeleportInstalling: {audit.TeleportJoinInstalling, audit.OutcomeOK},
	store.TeleportJoined:     {audit.TeleportJoinJoined, audit.OutcomeOK},
	store.DeliveryFailed:     {audit.TeleportJoinFailed, audit.OutcomeError},
}

var agentReasons = func() map[string]bool {
	m := map[string]bool{}
	for _, r := range store.AgentTeleportReasons {
		m[r] = true
	}
	return m
}()

// sanitize keeps at most store.MaxDetailBytes of printable text (control
// characters become spaces); text carrying PEM is dropped entirely.
func sanitize(d string) string {
	if strings.Contains(d, "-----BEGIN") {
		return ""
	}
	d = strings.ToValidUTF8(d, "?")
	d = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return ' '
		}
		return r
	}, d)
	for len(d) > store.MaxDetailBytes {
		_, size := utf8.DecodeLastRuneInString(d)
		d = d[:len(d)-size]
	}
	return d
}

// checkReport validates the closed-set fields of an agent report: failed
// carries a reason, joined a node host uuid, installing neither.
func checkReport(r Report) error {
	_, known := reportEvents[r.State]
	switch {
	case !uuidRE.MatchString(r.ItemID):
		return invalid("item_id")
	case !known:
		return invalid("state")
	case r.State == store.DeliveryFailed && !agentReasons[r.Reason]:
		return invalid("reason")
	case r.State != store.DeliveryFailed && r.Reason != "":
		return invalid("reason")
	case r.State == store.TeleportJoined && !uuidRE.MatchString(r.NodeHostUUID):
		return invalid("node_host_uuid")
	case r.State != store.TeleportJoined && r.NodeHostUUID != "":
		return invalid("node_host_uuid")
	}
	return nil
}

// errIgnored rolls back a report for an item that is no longer active, a
// repeated one, or one claiming progress on material that was never
// fetched.
var errIgnored = errors.New("teleportjoin: report ignored")

// Report records agent a's result for its own item. installing and joined
// require fetched material; installing only follows fetched, and joined
// and failed end the item. A report for an item that is no longer active
// (terminal, or a repeat of the terminal report) and a repeated installing
// are ignored (accepted false).
func (s *Service) Report(ctx context.Context, a store.Agent, r Report) (bool, error) {
	if !s.cfg.Enabled {
		return false, ErrDisabled
	}
	if err := checkReport(r); err != nil {
		return false, err
	}
	it, err := s.ownItem(ctx, a, strings.ToLower(r.ItemID), false)
	if err != nil {
		return false, err
	}
	detail := sanitize(r.Detail)
	node := strings.ToLower(r.NodeHostUUID)
	notFetched := false
	_, _, err = s.update(ctx, a.TenantID, it.ID, func(cur *store.TeleportJoinItem) ([]store.AuditRow, error) {
		switch {
		case !cur.Active():
			return nil, errIgnored
		case r.State == store.TeleportInstalling && cur.State != store.DeliveryFetched:
			notFetched = cur.State != store.TeleportInstalling
			return nil, errIgnored
		case r.State != store.DeliveryFailed && cur.FetchedAt == nil:
			notFetched = true
			return nil, errIgnored
		}
		now := s.now()
		s.finish(cur, r.State, r.Reason)
		cur.ReportedAt = &now
		if r.State == store.TeleportJoined {
			cur.NodeHostUUID = node
		}
		ev := reportEvents[r.State]
		return []store.AuditRow{s.itemRow(ev.t, agentActor(a), ev.outcome, *cur, map[string]any{"detail": detail})}, nil
	})
	if errors.Is(err, errIgnored) {
		if notFetched {
			s.refuse(ctx, a.TenantID, agentActor(a), it.ID, RefusedNotFetched)
		}
		return false, nil
	}
	return err == nil, err
}
