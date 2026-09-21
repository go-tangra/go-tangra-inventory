package audit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/store"
)

// fakeStore captures appended audit rows and can be told to fail.
type fakeStore struct {
	mu      sync.Mutex
	rows    []store.AuditRow
	failNow bool
	block   chan struct{} // if non-nil, AppendAudit blocks until closed
}

func (f *fakeStore) AppendAudit(ctx context.Context, row store.AuditRow) error {
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failNow {
		return errors.New("boom")
	}
	f.rows = append(f.rows, row)
	return nil
}

func (f *fakeStore) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func (f *fakeStore) snapshot() []store.AuditRow {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]store.AuditRow, len(f.rows))
	copy(out, f.rows)
	return out
}

func validEvent() Event {
	return Event{
		TenantID:    "t1",
		EventType:   HostUpdated,
		ActorKind:   ActorUser,
		ActorID:     "u1",
		SubjectKind: SubjectHost,
		SubjectID:   "h1",
		Outcome:     OutcomeOK,
	}
}

func TestKnown(t *testing.T) {
	if !Known(string(AgentEnrolled)) {
		t.Fatal("agent_enrolled should be known")
	}
	if Known("not_a_real_event") {
		t.Fatal("bogus event should not be known")
	}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Event)
		wantErr bool
	}{
		{"ok", func(e *Event) {}, false},
		{"unknown type", func(e *Event) { e.EventType = "nope" }, true},
		{"missing tenant", func(e *Event) { e.TenantID = "" }, true},
		{"bad actor", func(e *Event) { e.ActorKind = "robot" }, true},
		{"bad subject", func(e *Event) { e.SubjectKind = "planet" }, true},
		{"bad outcome", func(e *Event) { e.Outcome = "maybe" }, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			e := validEvent()
			tc.mutate(&e)
			err := Validate(e)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Validate err=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}

func TestRecordAndFlush(t *testing.T) {
	fs := &fakeStore{}
	w := NewWriter(fs, nil)
	defer w.Close()

	e := validEvent()
	e.Details = map[string]any{"note": "hello", "count": 3}
	if err := w.Record(context.Background(), e); err != nil {
		t.Fatalf("Record: %v", err)
	}
	w.Flush(context.Background())

	rows := fs.snapshot()
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if r.TenantID != "t1" || r.Action != string(HostUpdated) || r.Outcome != OutcomeOK {
		t.Fatalf("unexpected row: %+v", r)
	}
	if r.ID == "" || r.At.IsZero() {
		t.Fatalf("row missing ID/At: %+v", r)
	}
	if r.Detail["note"] != "hello" {
		t.Fatalf("detail not preserved: %+v", r.Detail)
	}
}

func TestRecordInvalidNotQueued(t *testing.T) {
	fs := &fakeStore{}
	w := NewWriter(fs, nil)
	defer w.Close()
	bad := validEvent()
	bad.EventType = "bogus"
	if err := w.Record(context.Background(), bad); err == nil {
		t.Fatal("expected error for invalid event")
	}
	w.Flush(context.Background())
	if fs.count() != 0 {
		t.Fatalf("invalid event should not be stored, got %d", fs.count())
	}
}

func TestRedaction(t *testing.T) {
	fs := &fakeStore{}
	w := NewWriter(fs, nil)
	defer w.Close()

	long := strings.Repeat("x", 300)
	e := validEvent()
	e.Details = map[string]any{
		"secret_key":    "s3cr3t",
		"credential":    "creds",
		"access_token":  "tok",
		"system_serial": "SN123",
		"kept":          "value",
		"long":          long,
		"nested": map[string]any{
			"api_token": "nope",
			"ok":        "yes",
		},
		"list": []any{
			map[string]any{"token": "drop", "keep": "in"},
		},
	}
	if err := w.Record(context.Background(), e); err != nil {
		t.Fatalf("Record: %v", err)
	}
	w.Flush(context.Background())

	d := fs.snapshot()[0].Detail
	for _, k := range []string{"secret_key", "credential", "access_token", "system_serial"} {
		if _, ok := d[k]; ok {
			t.Fatalf("forbidden key %q not dropped: %+v", k, d)
		}
	}
	if d["kept"] != "value" {
		t.Fatalf("safe key dropped: %+v", d)
	}
	if s, ok := d["long"].(string); !ok || len(s) != 256 {
		t.Fatalf("long string not truncated to 256: len=%d", len(d["long"].(string)))
	}
	nested, ok := d["nested"].(map[string]any)
	if !ok {
		t.Fatalf("nested map missing: %+v", d)
	}
	if _, ok := nested["api_token"]; ok {
		t.Fatalf("nested forbidden key not dropped: %+v", nested)
	}
	if nested["ok"] != "yes" {
		t.Fatalf("nested safe key dropped: %+v", nested)
	}
	list, ok := d["list"].([]any)
	if !ok || len(list) != 1 {
		t.Fatalf("list not preserved: %+v", d["list"])
	}
	elem := list[0].(map[string]any)
	if _, ok := elem["token"]; ok {
		t.Fatalf("forbidden key in list element not dropped: %+v", elem)
	}
	if elem["keep"] != "in" {
		t.Fatalf("safe key in list element dropped: %+v", elem)
	}
}

func TestDroppedOnOverflow(t *testing.T) {
	fs := &fakeStore{block: make(chan struct{})} // block the writer goroutine
	var mu sync.Mutex
	var errCount int
	w := newWriter(fs, func(error) { mu.Lock(); errCount++; mu.Unlock() }, 2)
	w.start()

	// Fill the queue plus overflow. The single buffered row may be pulled by the
	// goroutine (then blocked in AppendAudit), so send generously.
	dropped := false
	for i := 0; i < 50; i++ {
		_ = w.Record(context.Background(), validEvent())
		if w.Dropped() > 0 {
			dropped = true
			break
		}
	}
	if !dropped {
		t.Fatal("expected some events to be dropped on overflow")
	}
	if w.Dropped() < 1 {
		t.Fatalf("Dropped()=%d, want >=1", w.Dropped())
	}
	mu.Lock()
	ec := errCount
	mu.Unlock()
	if ec < 1 {
		t.Fatal("onError should have fired on overflow")
	}
	close(fs.block) // unblock so Close can drain
	w.Close()
}

func TestRecordAfterClose(t *testing.T) {
	fs := &fakeStore{}
	w := NewWriter(fs, nil)
	w.Close()
	w.Close() // idempotent
	before := w.Dropped()
	if err := w.Record(context.Background(), validEvent()); err == nil {
		t.Fatal("Record after Close should error")
	}
	if w.Dropped() != before+1 {
		t.Fatalf("Dropped should increment after close, got %d", w.Dropped())
	}
	// Flush on a closed writer returns immediately.
	w.Flush(context.Background())
}

func TestWriteRowError(t *testing.T) {
	fs := &fakeStore{failNow: true}
	var mu sync.Mutex
	var gotErr error
	w := NewWriter(fs, func(e error) { mu.Lock(); gotErr = e; mu.Unlock() })
	defer w.Close()
	if err := w.Record(context.Background(), validEvent()); err != nil {
		t.Fatalf("Record: %v", err)
	}
	w.Flush(context.Background())
	// Give the goroutine a beat to surface the store error.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		e := gotErr
		mu.Unlock()
		if e != nil {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("onError never fired for store failure")
}

func TestFlushCanceled(t *testing.T) {
	fs := &fakeStore{}
	w := NewWriter(fs, nil)
	defer w.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w.Flush(ctx) // should return promptly despite canceled ctx
}
