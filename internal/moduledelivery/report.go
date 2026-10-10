package moduledelivery

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
	State        string // installed | failed | hook_failed
	Reason       string // agent reason code
	HookExitCode int    // -1 = hook not run; 0..255; 256 = timeout
	Detail       string // <= 256 bytes after sanitising; never hook output
}

var reportEvents = map[string]struct {
	t       audit.EventType
	outcome string
}{
	store.DeliveryInstalled:  {audit.ModuleDeliveryInstalled, audit.OutcomeOK},
	store.DeliveryFailed:     {audit.ModuleDeliveryFailed, audit.OutcomeError},
	store.DeliveryHookFailed: {audit.ModuleDeliveryHookFailed, audit.OutcomeError},
}

var agentReasons = func() map[string]bool {
	m := map[string]bool{}
	for _, r := range store.AgentModuleReasons {
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

// checkReport validates the closed-set fields of an agent report.
func checkReport(r Report) error {
	_, known := reportEvents[r.State]
	switch {
	case !uuidRE.MatchString(r.ItemID):
		return invalid("item_id")
	case !known:
		return invalid("state")
	case r.Reason != "" && !agentReasons[r.Reason]:
		return invalid("reason")
	case r.HookExitCode < -1 || r.HookExitCode > 256:
		return invalid("hook_exit_code")
	}
	return nil
}

// errIgnored rolls back a report for an item that is no longer active (or
// claims an install that was never fetched); errRepeated one that repeats
// the report the item already holds.
var (
	errIgnored  = errors.New("moduledelivery: report ignored")
	errRepeated = errors.New("moduledelivery: report repeated")
)

// Report records agent a's result for its own item: installed and
// hook_failed require a fetched bundle; hook_failed defaults its reason to
// hook_failed and installed carries none. A repeated identical report is
// accepted without a change (idempotent); any other report for an item that
// is no longer active is ignored (accepted false).
func (s *Service) Report(ctx context.Context, a store.Agent, r Report) (bool, error) {
	if !s.cfg.Enabled {
		return false, ErrDisabled
	}
	if err := checkReport(r); err != nil {
		return false, err
	}
	it, err := s.ownItem(ctx, a, r.ItemID, false)
	if err != nil {
		return false, err
	}
	detail := sanitize(r.Detail)
	state, reason := r.State, r.Reason
	switch state {
	case store.DeliveryInstalled:
		reason = ""
	case store.DeliveryHookFailed:
		if reason == "" {
			reason = store.ReasonHookFailed
		}
	}
	notFetched := false
	_, _, err = s.update(ctx, a.TenantID, it.ID, func(cur *store.ModuleDeliveryItem) ([]store.AuditRow, error) {
		if !cur.Active() {
			if cur.State == state && (cur.Reason == reason || r.Reason == "") {
				return nil, errRepeated // the agent repeats a report whose answer it lost
			}
			return nil, errIgnored
		}
		if state != store.DeliveryFailed && cur.FetchedAt == nil {
			notFetched = true
			return nil, errIgnored
		}
		now := s.now()
		s.finish(cur, state, reason)
		cur.HookExitCode, cur.ReportedAt = r.HookExitCode, &now
		ev := reportEvents[state]
		return []store.AuditRow{s.itemRow(ev.t, agentActor(a), ev.outcome, *cur,
			map[string]any{"hook_exit_code": r.HookExitCode, "detail": detail})}, nil
	})
	switch {
	case errors.Is(err, errRepeated):
		return true, nil
	case errors.Is(err, errIgnored):
		if notFetched {
			s.refuse(ctx, a.TenantID, agentActor(a), it.ID, RefusedNotFetched)
		}
		return false, nil
	}
	return err == nil, err
}
