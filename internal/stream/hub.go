package stream

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Limits and defaults.
const (
	MaxPayload    = 16 << 10
	MaxTargets    = 1000
	DefaultMaxLen = 10000
	connBuffer    = 32 // events queued per connection before it is dropped
)

// Errors.
var (
	ErrTooMany  = errors.New("stream: too many open streams")
	ErrType     = errors.New("stream: invalid event type")
	ErrPayload  = errors.New("stream: payload too large or not JSON-safe")
	ErrTargets  = errors.New("stream: 1..1000 user ids or all=true")
	ErrReserved = errors.New("stream: reserved event type")
)

var typeRE = regexp.MustCompile(`^[a-z][a-z0-9._-]{0,63}$`)

// Reserved event types only the module itself publishes: the certificate
// lifecycle events (issued|renewed|revoked) plus the internal SSE/system
// types (reset|bye).
var Reserved = map[string]bool{"issued": true, "renewed": true, "revoked": true, "reset": true, "bye": true}

// Config bounds the hub.
type Config struct {
	ReplayWindow     time.Duration
	StreamsPerUser   int
	StreamsPerTenant int
	MaxLen           int64
	TrimInterval     time.Duration // default 1 minute
	ReadBlock        time.Duration // default 5 s
	RetryDelay       time.Duration // after a read failure; default 1 s
}

// Event is one delivered event.
type Event struct {
	ID   string
	Type string
	Data string // JSON
}

// Key names a tenant's stream.
func Key(tenantID string) string { return "platform:events:" + tenantID }

// Hub fans tenant streams out to per-user connections.
type Hub struct {
	c       Client
	cfg     Config
	log     *slog.Logger
	now     func() time.Time
	mu      sync.Mutex
	tenants map[string]*tenantSub
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type tenantSub struct {
	conns  map[*Subscription]struct{}
	byUser map[string]int
	cancel context.CancelFunc
}

// Subscription is one open stream.
type Subscription struct {
	hub    *Hub
	tenant string
	user   string
	ch     chan Event
	once   sync.Once
	mu     sync.Mutex
	last   string // highest id delivered (dedupe between replay and live)
	closed bool
}

// NewHub wires the hub; Close stops every subscriber loop.
func NewHub(c Client, cfg Config, log *slog.Logger) *Hub {
	if cfg.ReplayWindow <= 0 {
		cfg.ReplayWindow = 5 * time.Minute
	}
	if cfg.StreamsPerUser <= 0 {
		cfg.StreamsPerUser = 5
	}
	if cfg.StreamsPerTenant <= 0 {
		cfg.StreamsPerTenant = 2000
	}
	if cfg.MaxLen <= 0 {
		cfg.MaxLen = DefaultMaxLen
	}
	if cfg.TrimInterval <= 0 {
		cfg.TrimInterval = time.Minute
	}
	if cfg.ReadBlock <= 0 {
		cfg.ReadBlock = 5 * time.Second
	}
	if cfg.RetryDelay <= 0 {
		cfg.RetryDelay = time.Second
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{c: c, cfg: cfg, log: log, now: time.Now, tenants: map[string]*tenantSub{}, ctx: ctx, cancel: cancel}
}

// SetClock injects the clock (tests).
func (h *Hub) SetClock(now func() time.Time) { h.now = now }

// Close stops the loops and closes every subscription.
func (h *Hub) Close() {
	h.cancel()
	h.mu.Lock()
	subs := []*Subscription{}
	for _, t := range h.tenants {
		for s := range t.conns {
			subs = append(subs, s)
		}
	}
	h.mu.Unlock()
	for _, s := range subs {
		s.Close()
	}
	h.wg.Wait()
}

// OpenStreams counts the open subscriptions.
func (h *Hub) OpenStreams() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, t := range h.tenants {
		n += len(t.conns)
	}
	return n
}

// ValidateType checks an event type (module events may not use reserved ones).
func ValidateType(typ string, module bool) error {
	if !typeRE.MatchString(typ) {
		return ErrType
	}
	if module && Reserved[typ] {
		return ErrReserved
	}
	return nil
}

// Publish appends an event for the users (or everyone) of the tenant.
func (h *Hub) Publish(ctx context.Context, tenantID string, to []string, all bool, typ string, data any) error {
	_, err := h.PublishID(ctx, tenantID, to, all, typ, data, false)
	return err
}

// PublishID is Publish returning the event id; module=true refuses reserved types.
func (h *Hub) PublishID(ctx context.Context, tenantID string, to []string, all bool, typ string, data any, module bool) (string, error) {
	if err := ValidateType(typ, module); err != nil {
		return "", err
	}
	if all == (len(to) > 0) || len(to) > MaxTargets {
		return "", ErrTargets
	}
	var raw []byte
	switch v := data.(type) {
	case nil:
		raw = []byte("{}")
	case []byte:
		raw = v
	case json.RawMessage:
		raw = v
	case string:
		raw = []byte(v)
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", ErrPayload
		}
		raw = b
	}
	if len(raw) > MaxPayload || !json.Valid(raw) || strings.ContainsAny(string(raw), "\n\r") {
		return "", ErrPayload
	}
	target := "*"
	if !all {
		for _, u := range to {
			if u == "" || strings.ContainsAny(u, ", \n") {
				return "", ErrTargets
			}
		}
		target = strings.Join(to, ",")
	}
	return h.c.XAdd(ctx, Key(tenantID), map[string]string{"to": target, "type": typ, "data": string(raw), "at": strconv.FormatInt(h.now().UnixMilli(), 10)}, h.cfg.MaxLen)
}

// Subscribe opens a stream for a user; lastID replays the missed events
// inside the window (or yields a reset event beyond it).
func (h *Hub) Subscribe(ctx context.Context, tenantID, userID, lastID string) (*Subscription, error) {
	h.mu.Lock()
	if h.ctx.Err() != nil {
		h.mu.Unlock()
		return nil, errors.New("stream: hub closed")
	}
	t, ok := h.tenants[tenantID]
	if !ok {
		// The loop starts after the current tail so that nothing published
		// once this call returns can be missed (XREAD $ alone would race).
		start, err := h.c.XLast(ctx, Key(tenantID))
		if err != nil {
			h.mu.Unlock()
			return nil, err
		}
		if start == "" {
			start = "0-0"
		}
		t = &tenantSub{conns: map[*Subscription]struct{}{}, byUser: map[string]int{}}
		h.tenants[tenantID] = t
		lctx, cancel := context.WithCancel(h.ctx)
		t.cancel = cancel
		h.wg.Add(1)
		go h.loop(lctx, tenantID, t, start)
	}
	if t.byUser[userID] >= h.cfg.StreamsPerUser || len(t.conns) >= h.cfg.StreamsPerTenant {
		h.mu.Unlock()
		return nil, ErrTooMany
	}
	s := &Subscription{hub: h, tenant: tenantID, user: userID, ch: make(chan Event, connBuffer)}
	t.conns[s] = struct{}{}
	t.byUser[userID]++
	h.mu.Unlock()
	if lastID != "" {
		h.replay(ctx, s, lastID)
	}
	return s, nil
}

// replay pushes the events since lastID, or a reset when the id is older than the window.
func (h *Hub) replay(ctx context.Context, s *Subscription, lastID string) {
	ms, _, ok := ParseID(lastID)
	if !ok || time.UnixMilli(ms).Before(h.now().Add(-h.cfg.ReplayWindow)) {
		s.push(Event{Type: "reset", Data: `{"reason":"replay_window"}`})
		return
	}
	entries, err := h.c.XRange(ctx, Key(s.tenant), lastID, int64(h.cfg.MaxLen))
	if err != nil {
		s.push(Event{Type: "reset", Data: `{"reason":"replay_unavailable"}`})
		return
	}
	for _, e := range entries {
		if targets(e.Fields["to"], s.user) {
			s.push(Event{ID: e.ID, Type: e.Fields["type"], Data: e.Fields["data"]})
		}
	}
}

// targets reports whether an entry's recipients include the user.
func targets(to, user string) bool {
	if to == "*" {
		return true
	}
	for _, u := range strings.Split(to, ",") {
		if u == user {
			return true
		}
	}
	return false
}

// loop reads the tenant stream and fans out; it stops when the last
// subscription closes or the hub closes.
func (h *Hub) loop(ctx context.Context, tenantID string, t *tenantSub, last string) {
	defer h.wg.Done()
	key := Key(tenantID)
	trim := time.NewTicker(h.cfg.TrimInterval)
	defer trim.Stop()
	for ctx.Err() == nil {
		select {
		case <-trim.C:
			_ = h.c.XTrimMinID(ctx, key, ID(h.now().Add(-h.cfg.ReplayWindow), 0))
		default:
		}
		entries, err := h.c.XRead(ctx, key, last, h.cfg.ReadBlock, 100)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			h.log.Warn("stream read failed; retrying", "tenant", tenantID, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(h.cfg.RetryDelay):
			}
			continue
		}
		for _, e := range entries {
			last = e.ID
			h.dispatch(t, e)
		}
	}
}

func (h *Hub) dispatch(t *tenantSub, e Entry) {
	ev := Event{ID: e.ID, Type: e.Fields["type"], Data: e.Fields["data"]}
	h.mu.Lock()
	subs := make([]*Subscription, 0, len(t.conns))
	for s := range t.conns {
		if targets(e.Fields["to"], s.user) {
			subs = append(subs, s)
		}
	}
	h.mu.Unlock()
	for _, s := range subs {
		if !s.push(ev) {
			s.Close() // stalled reader: its buffer is full
		}
	}
}

// push queues an event unless it was already delivered (replay overlap) or
// the buffer is full; returns false when the subscriber stalled.
func (s *Subscription) push(ev Event) bool {
	s.mu.Lock()
	if s.closed || (ev.ID != "" && s.last != "" && !Less(s.last, ev.ID)) {
		s.mu.Unlock()
		return true
	}
	if ev.ID != "" {
		s.last = ev.ID
	}
	s.mu.Unlock()
	select {
	case s.ch <- ev:
		return true
	default:
		return false
	}
}

// Events yields the subscription's events; closed when the subscription ends.
func (s *Subscription) Events() <-chan Event { return s.ch }

// Close ends the subscription (idempotent).
func (s *Subscription) Close() {
	s.once.Do(func() {
		h := s.hub
		h.mu.Lock()
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
		if t, ok := h.tenants[s.tenant]; ok {
			delete(t.conns, s)
			t.byUser[s.user]--
			if t.byUser[s.user] <= 0 {
				delete(t.byUser, s.user)
			}
			if len(t.conns) == 0 {
				t.cancel()
				delete(h.tenants, s.tenant)
			}
		}
		h.mu.Unlock()
		close(s.ch)
	})
}
