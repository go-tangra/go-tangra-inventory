// Package stream delivers live certificate events to signed-in people:
// events are appended to one Valkey stream per tenant (fan-out across service
// instances plus a bounded replay window), every instance fans them out to
// the SSE connections it holds, and reconnecting clients replay from their
// Last-Event-ID. The same Valkey client backs the renewal lease and counts
// rate limits.
package stream

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Entry is one stream entry.
type Entry struct {
	ID     string
	Fields map[string]string
}

// Client is the subset of Valkey the package needs (Memory in tests).
type Client interface {
	XAdd(ctx context.Context, key string, fields map[string]string, maxLen int64) (string, error)
	XRead(ctx context.Context, key, afterID string, block time.Duration, count int64) ([]Entry, error)
	XRange(ctx context.Context, key, afterID string, count int64) ([]Entry, error)
	XTrimMinID(ctx context.Context, key, minID string) error
	// XLast returns the id of the newest entry ("" for an empty stream).
	XLast(ctx context.Context, key string) (string, error)
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Ping(ctx context.Context) error
	Close()
}

// Memory is an in-process Client with the same semantics (tests, single instance).
type Memory struct {
	mu       sync.Mutex
	streams  map[string][]Entry
	counters map[string]counter
	seq      int64
	notify   chan struct{}
	Now      func() time.Time
	Err      error // returned by every call when set (outage injection)
}

type counter struct {
	n   int64
	exp time.Time
}

// NewMemory returns an empty in-process client.
func NewMemory() *Memory {
	return &Memory{streams: map[string][]Entry{}, counters: map[string]counter{}, notify: make(chan struct{}), Now: time.Now}
}

// ID builds a stream id from a time and a sequence (test helper).
func ID(at time.Time, seq int64) string {
	return strconv.FormatInt(at.UnixMilli(), 10) + "-" + strconv.FormatInt(seq, 10)
}

// ParseID splits a stream id into its millisecond and sequence parts.
func ParseID(id string) (ms int64, seq int64, ok bool) {
	parts := strings.SplitN(id, "-", 2)
	if len(parts) != 2 {
		return 0, 0, false
	}
	ms, err1 := strconv.ParseInt(parts[0], 10, 64)
	seq, err2 := strconv.ParseInt(parts[1], 10, 64)
	return ms, seq, err1 == nil && err2 == nil
}

// Less orders stream ids.
func Less(a, b string) bool {
	am, as, _ := ParseID(a)
	bm, bs, _ := ParseID(b)
	if am != bm {
		return am < bm
	}
	return as < bs
}

func (m *Memory) XAdd(_ context.Context, key string, fields map[string]string, maxLen int64) (string, error) {
	m.mu.Lock()
	if m.Err != nil {
		m.mu.Unlock()
		return "", m.Err
	}
	m.seq++
	id := ID(m.Now(), m.seq)
	f := make(map[string]string, len(fields))
	for k, v := range fields {
		f[k] = v
	}
	m.streams[key] = append(m.streams[key], Entry{ID: id, Fields: f})
	if maxLen > 0 && int64(len(m.streams[key])) > maxLen {
		m.streams[key] = m.streams[key][len(m.streams[key])-int(maxLen):]
	}
	ch := m.notify
	m.notify = make(chan struct{})
	m.mu.Unlock()
	close(ch)
	return id, nil
}

func (m *Memory) after(key, afterID string, count int64) []Entry {
	var out []Entry
	for _, e := range m.streams[key] {
		if afterID == "" || Less(afterID, e.ID) {
			out = append(out, e)
			if count > 0 && int64(len(out)) == count {
				break
			}
		}
	}
	return out
}

func (m *Memory) XRead(ctx context.Context, key, afterID string, block time.Duration, count int64) ([]Entry, error) {
	m.mu.Lock()
	if m.Err != nil {
		m.mu.Unlock()
		return nil, m.Err
	}
	if afterID == "" || afterID == "$" {
		afterID = "0-0"
		if n := len(m.streams[key]); n > 0 {
			afterID = m.streams[key][n-1].ID
		}
	}
	out := m.after(key, afterID, count)
	ch := m.notify
	m.mu.Unlock()
	if len(out) > 0 || block <= 0 {
		return out, nil
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(block):
		return nil, nil
	case <-ch:
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	return m.after(key, afterID, count), nil
}

func (m *Memory) XRange(_ context.Context, key, afterID string, count int64) ([]Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return nil, m.Err
	}
	return m.after(key, afterID, count), nil
}

func (m *Memory) XLast(_ context.Context, key string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return "", m.Err
	}
	if n := len(m.streams[key]); n > 0 {
		return m.streams[key][n-1].ID, nil
	}
	return "", nil
}

func (m *Memory) XTrimMinID(_ context.Context, key, minID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	var keep []Entry
	for _, e := range m.streams[key] {
		if !Less(e.ID, minID) {
			keep = append(keep, e)
		}
	}
	m.streams[key] = keep
	return nil
}

func (m *Memory) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return 0, m.Err
	}
	now := m.Now()
	c := m.counters[key]
	if c.n == 0 || (!c.exp.IsZero() && !now.Before(c.exp)) {
		c = counter{}
		if ttl > 0 {
			c.exp = now.Add(ttl)
		}
	}
	c.n++
	m.counters[key] = c
	return c.n, nil
}

func (m *Memory) Ping(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.Err
}

// Close drops every stream and counter.
func (m *Memory) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streams, m.counters = map[string][]Entry{}, map[string]counter{}
}

// Len reports the entries of a stream (test helper).
func (m *Memory) Len(key string) int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.streams[key]) }

// Limiter counts events per subject and window.
type Limiter struct {
	c Client
}

// NewLimiter wraps a client.
func NewLimiter(c Client) *Limiter { return &Limiter{c: c} }

// RateKey names a rate-limit window.
func RateKey(kind, subject string, at time.Time) string {
	return "lcm:rl:" + kind + ":" + subject + ":" + strconv.FormatInt(at.Unix()/60, 10)
}

// Limited reports whether the subject exceeded limit within the current
// minute (limit ≤ 0 = never). A counter failure counts as limited: a send
// must never bypass the limit because the counter is down.
func (l *Limiter) Limited(ctx context.Context, kind, subject string, limit int, at time.Time) (bool, error) {
	if limit <= 0 {
		return false, nil
	}
	n, err := l.c.Incr(ctx, RateKey(kind, subject, at), 2*time.Minute)
	if err != nil {
		return true, err
	}
	return n > int64(limit), nil
}
