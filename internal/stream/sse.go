package stream

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

// SSE timing (contracts/stream.md).
const (
	Heartbeat = 15 * time.Second
	MaxAge    = 290 * time.Second
	Retry     = 3000
)

// Frame formats one SSE frame; data never carries a bare line break.
func Frame(ev Event) string {
	var b strings.Builder
	if ev.ID != "" {
		b.WriteString("id: " + ev.ID + "\n")
	}
	b.WriteString("event: " + ev.Type + "\n")
	data := strings.NewReplacer("\r", "", "\n", "").Replace(ev.Data)
	b.WriteString("data: " + data + "\n\n")
	return b.String()
}

// ServeSSE writes the subscription to w until the client leaves, the
// subscription closes or maxAge passes (then `event: bye`).
func ServeSSE(w http.ResponseWriter, r *http.Request, sub *Subscription, instance string, heartbeat, maxAge time.Duration) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "retry: %d\n: connected %s\n\n", Retry, instance)
	flusher.Flush()
	if heartbeat <= 0 {
		heartbeat = Heartbeat
	}
	if maxAge <= 0 {
		maxAge = MaxAge
	}
	ping := time.NewTicker(heartbeat)
	defer ping.Stop()
	deadline := time.NewTimer(maxAge)
	defer deadline.Stop()
	defer sub.Close()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-deadline.C:
			_, _ = fmt.Fprint(w, "event: bye\ndata: {}\n\n")
			flusher.Flush()
			return
		case <-ping.C:
			_, _ = fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, open := <-sub.Events():
			if !open {
				return
			}
			_, _ = fmt.Fprint(w, Frame(ev))
			flusher.Flush()
		}
	}
}
