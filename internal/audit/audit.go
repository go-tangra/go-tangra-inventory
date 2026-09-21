// Package audit records every inventory operation in a closed vocabulary with a
// detail guard that keeps agent credentials, token secrets and hardware serials
// out of the log. Events are buffered and written to the audit hypertable by a
// background goroutine so recording never blocks the caller.
package audit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/store"
)

// EventType is the closed audit vocabulary (data-model.md, AuditAction).
type EventType string

// Event types.
const (
	AgentEnrolled    EventType = "agent_enrolled"
	TokenMinted      EventType = "token_minted"
	TokenRevoked     EventType = "token_revoked"
	SnapshotIngested EventType = "snapshot_ingested"
	SnapshotDeleted  EventType = "snapshot_deleted"
	HostUpdated      EventType = "host_updated"
	HostRetired      EventType = "host_retired"
	HostDeleted      EventType = "host_deleted"
	HostTagged       EventType = "host_tagged"
	RefreshRequested EventType = "refresh_requested"
	RefreshDelivered EventType = "refresh_delivered"
	BackupExported   EventType = "backup_exported"
	BackupImported   EventType = "backup_imported"
	AccessRefused    EventType = "access_refused"
)

// Subject kinds (closed set).
const (
	SubjectHost     = "host"
	SubjectSnapshot = "snapshot"
	SubjectAgent    = "agent"
	SubjectToken    = "token"
	SubjectBackup   = "backup"
	SubjectSystem   = "system"
)

// Outcomes (closed set).
const (
	OutcomeOK      = "ok"
	OutcomeRefused = "refused"
	OutcomeError   = "error"
)

// Actor kinds (closed set).
const (
	ActorAgent   = "agent"
	ActorUser    = "user"
	ActorService = "service"
	ActorSystem  = "system"
)

var known = map[EventType]struct{}{}

func init() {
	for _, t := range []EventType{
		AgentEnrolled, TokenMinted, TokenRevoked,
		SnapshotIngested, SnapshotDeleted,
		HostUpdated, HostRetired, HostDeleted, HostTagged,
		RefreshRequested, RefreshDelivered,
		BackupExported, BackupImported,
		AccessRefused,
	} {
		known[t] = struct{}{}
	}
}

// Known reports whether t is in the vocabulary.
func Known(t string) bool { _, ok := known[EventType(t)]; return ok }

// Event is one record before persistence. At is filled by Record.
type Event struct {
	TenantID    string
	EventType   EventType
	ActorKind   string // agent | user | service | system
	ActorID     string
	SubjectKind string // host | snapshot | agent | token | backup | system
	SubjectID   string
	Outcome     string // ok | refused | error
	Reason      string
	Details     map[string]any
}

// Store persists audit rows. repo.Store satisfies it, as does any test double.
type Store interface {
	AppendAudit(ctx context.Context, row store.AuditRow) error
}

// Validate checks the closed vocabulary and required fields. An unknown event
// type is a programming error and is surfaced to the caller.
func Validate(e Event) error {
	if _, ok := known[e.EventType]; !ok {
		return fmt.Errorf("audit: unknown event type %q", e.EventType)
	}
	if e.TenantID == "" {
		return errors.New("audit: tenant_id is required")
	}
	switch e.ActorKind {
	case ActorAgent, ActorUser, ActorService, ActorSystem:
	default:
		return fmt.Errorf("audit: actor_kind %q", e.ActorKind)
	}
	switch e.SubjectKind {
	case SubjectHost, SubjectSnapshot, SubjectAgent, SubjectToken, SubjectBackup, SubjectSystem:
	default:
		return fmt.Errorf("audit: subject_kind %q", e.SubjectKind)
	}
	switch e.Outcome {
	case OutcomeOK, OutcomeRefused, OutcomeError:
	default:
		return fmt.Errorf("audit: outcome %q", e.Outcome)
	}
	return nil
}

// forbidden detail-key substrings (case-insensitive): sealed credentials, token
// secrets and hardware serial numbers never belong in an audit detail.
var forbidden = []string{"secret", "credential", "token", "serial"}

func forbiddenKey(k string) bool {
	lk := strings.ToLower(k)
	for _, f := range forbidden {
		if strings.Contains(lk, f) {
			return true
		}
	}
	return false
}

func guardValue(v any) any {
	switch x := v.(type) {
	case string:
		if len(x) > 256 {
			return x[:256]
		}
		return x
	case map[string]any:
		return guardMap(x)
	case []any:
		out := make([]any, len(x))
		for i, vv := range x {
			out[i] = guardValue(vv)
		}
		return out
	default:
		return v
	}
}

// guardMap returns a copy of m with any key whose lowercased name carries
// secret|credential|token|serial (at any depth) dropped and string values
// truncated to 256 characters.
func guardMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if forbiddenKey(k) {
			continue
		}
		out[k] = guardValue(v)
	}
	return out
}

// Writer buffers events and writes them one row at a time; Record never blocks.
type Writer struct {
	st      Store
	ch      chan store.AuditRow
	wg      sync.WaitGroup
	mu      sync.Mutex
	closed  bool
	dropped int64
	onError func(error)
	flushCh chan chan struct{}
	tick    time.Duration
}

// NewWriter starts the batch writer (queue 10k, flush every 500 ms).
func NewWriter(st Store, onError func(error)) *Writer {
	w := newWriter(st, onError, 10000)
	w.start()
	return w
}

func newWriter(st Store, onError func(error), queue int) *Writer {
	w := &Writer{st: st, ch: make(chan store.AuditRow, queue), onError: onError, flushCh: make(chan chan struct{}), tick: 500 * time.Millisecond}
	if w.onError == nil {
		w.onError = func(error) {}
	}
	return w
}

func (w *Writer) start() {
	w.wg.Add(1)
	go w.run()
}

// Record validates the event, fills At=now, guards the details and enqueues the
// row. An invalid event (unknown type, missing tenant, bad enum) returns an
// error and is not queued.
func (w *Writer) Record(_ context.Context, e Event) error {
	if err := Validate(e); err != nil {
		return err
	}
	row := store.AuditRow{
		ID:          store.NewID(),
		TenantID:    e.TenantID,
		At:          time.Now(),
		ActorKind:   e.ActorKind,
		ActorID:     e.ActorID,
		Action:      string(e.EventType),
		SubjectKind: e.SubjectKind,
		SubjectID:   e.SubjectID,
		Outcome:     e.Outcome,
		Reason:      e.Reason,
		Detail:      guardMap(e.Details),
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.dropped++
		return errors.New("audit: writer closed")
	}
	select {
	case w.ch <- row:
	default:
		w.dropped++
		w.onError(errors.New("audit: queue full, event dropped"))
	}
	return nil
}

// Flush writes everything queued so far and returns when it is stored or ctx is done.
func (w *Writer) Flush(ctx context.Context) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.mu.Unlock()
	done := make(chan struct{})
	select {
	case w.flushCh <- done:
	case <-ctx.Done():
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

// Dropped returns the number of dropped events.
func (w *Writer) Dropped() int64 { w.mu.Lock(); defer w.mu.Unlock(); return w.dropped }

func (w *Writer) writeRow(r store.AuditRow) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := w.st.AppendAudit(ctx, r); err != nil {
		w.onError(err)
	}
}

func (w *Writer) run() {
	defer w.wg.Done()
	t := time.NewTicker(w.tick)
	defer t.Stop()
	for {
		select {
		case r, ok := <-w.ch:
			if !ok {
				return
			}
			w.writeRow(r)
		case <-t.C:
			// nothing buffered locally; rows are written as they arrive.
		case done := <-w.flushCh:
			for {
				select {
				case r := <-w.ch:
					w.writeRow(r)
					continue
				default:
				}
				break
			}
			close(done)
		}
	}
}

// Close drains and stops the writer.
func (w *Writer) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	close(w.ch)
	w.mu.Unlock()
	w.wg.Wait()
}
