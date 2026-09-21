package registry

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Valkey is a cross-instance Registry backed by a Valkey server. Each connected
// agent holds a short-lived online key (owned by the instance that holds the
// connection and refreshed by a heartbeat) plus a metadata hash and membership
// in a per-tenant set; commands are fanned out over a per-agent Pub/Sub
// channel, so any instance can Deliver to an agent connected elsewhere.
//
// The Memory implementation is the fully tested path; this type is exercised by
// the integration suite against a real server. Wire-level cadences are marked
// with TODO where a real deployment may want to tune them.
type Valkey struct {
	c          valkey.Client
	instanceID string
	ttl        time.Duration // online-key TTL; heartbeat refreshes at ttl/3
}

// NewValkey builds a Valkey-backed registry. instanceID identifies this
// process (stored as the online-key owner). A zero ttl defaults to 30s.
func NewValkey(c valkey.Client, instanceID string) *Valkey {
	return &Valkey{c: c, instanceID: instanceID, ttl: 30 * time.Second}
}

// Key helpers keep the namespace in one place.
func onlineKey(agentID string) string { return "inv:agent:online:" + agentID }
func metaKey(agentID string) string   { return "inv:agent:meta:" + agentID }
func cmdChan(agentID string) string   { return "inv:agent:cmd:" + agentID }
func tenantKey(tenantID string) string {
	return "inv:agent:tenant:" + tenantID
}

// Register writes the online marker + metadata, joins the tenant set, starts a
// heartbeat and a Pub/Sub subscriber, and returns the command channel with an
// idempotent unregister.
func (v *Valkey) Register(ctx context.Context, a ConnectedAgent) (<-chan Command, func(), error) {
	if a.AgentID == "" {
		return nil, nil, ErrInvalid
	}
	if a.ConnectedAt.IsZero() {
		a.ConnectedAt = time.Now()
	}
	sec := int64(v.ttl / time.Second)
	if sec <= 0 {
		sec = 30
	}

	// Online marker owned by this instance.
	if err := v.c.Do(ctx, v.c.B().Set().Key(onlineKey(a.AgentID)).Value(v.instanceID).ExSeconds(sec).Build()).Error(); err != nil {
		return nil, nil, err
	}
	// Metadata hash (best-effort; do not fail Register if these lag).
	_ = v.c.Do(ctx, v.c.B().Hset().Key(metaKey(a.AgentID)).FieldValue().
		FieldValue("agent_id", a.AgentID).
		FieldValue("tenant_id", a.TenantID).
		FieldValue("host_id", a.HostID).
		FieldValue("version", a.Version).
		FieldValue("connected_at", a.ConnectedAt.UTC().Format(time.RFC3339Nano)).
		Build()).Error()
	_ = v.c.Do(ctx, v.c.B().Expire().Key(metaKey(a.AgentID)).Seconds(sec).Build()).Error()
	_ = v.c.Do(ctx, v.c.B().Sadd().Key(tenantKey(a.TenantID)).Member(a.AgentID).Build()).Error()

	runCtx, cancel := context.WithCancel(context.Background())
	out := make(chan Command, deliverBuffer)
	var wg sync.WaitGroup

	// Heartbeat: refresh the online marker (and metadata TTL) so a crashed
	// instance's agents expire out of the registry.
	// TODO(ops): tune the refresh fraction and add jitter for large fleets.
	wg.Add(1)
	go func() {
		defer wg.Done()
		t := time.NewTicker(v.ttl / 3)
		defer t.Stop()
		for {
			select {
			case <-runCtx.Done():
				return
			case <-t.C:
				_ = v.c.Do(runCtx, v.c.B().Set().Key(onlineKey(a.AgentID)).Value(v.instanceID).ExSeconds(sec).Build()).Error()
				_ = v.c.Do(runCtx, v.c.B().Expire().Key(metaKey(a.AgentID)).Seconds(sec).Build()).Error()
			}
		}
	}()

	// Subscriber: relay published commands to the connection's channel.
	// Receive blocks until runCtx is cancelled (unregister) or the connection
	// drops; a dropped subscription simply stops delivery for this instance.
	wg.Add(1)
	go func() {
		defer wg.Done()
		sub := v.c.B().Subscribe().Channel(cmdChan(a.AgentID)).Build()
		_ = v.c.Receive(runCtx, sub, func(msg valkey.PubSubMessage) {
			var cmd Command
			if err := json.Unmarshal([]byte(msg.Message), &cmd); err != nil {
				return
			}
			select {
			case out <- cmd:
			default: // reader not keeping up; drop (best-effort push)
			}
		})
	}()

	var once sync.Once
	unregister := func() {
		once.Do(func() {
			cancel()
			// Best-effort teardown with a fresh short context.
			tctx, tcancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer tcancel()
			_ = v.c.Do(tctx, v.c.B().Del().Key(onlineKey(a.AgentID)).Build()).Error()
			_ = v.c.Do(tctx, v.c.B().Del().Key(metaKey(a.AgentID)).Build()).Error()
			_ = v.c.Do(tctx, v.c.B().Srem().Key(tenantKey(a.TenantID)).Member(a.AgentID).Build()).Error()
			wg.Wait()
			close(out)
		})
	}
	return out, unregister, nil
}

// Deliver publishes a command to the agent's channel. It reports delivered when
// the publish reached at least one subscriber; as a fallback (some deployments
// under-report receiver counts) it reports delivered when the agent's online
// key still exists.
func (v *Valkey) Deliver(ctx context.Context, agentID string, cmd Command) (bool, error) {
	online, err := v.exists(ctx, onlineKey(agentID))
	if err != nil {
		return false, err
	}
	if !online {
		return false, nil
	}
	payload, err := json.Marshal(cmd)
	if err != nil {
		return false, err
	}
	receivers, err := v.c.Do(ctx, v.c.B().Publish().Channel(cmdChan(agentID)).Message(string(payload)).Build()).AsInt64()
	if err != nil {
		return false, err
	}
	if receivers > 0 {
		return true, nil
	}
	return online, nil
}

// ListConnected returns the tenant's currently-online agents, pruning stale
// set members whose online key has expired.
func (v *Valkey) ListConnected(ctx context.Context, tenantID string) ([]ConnectedAgent, error) {
	ids, err := v.c.Do(ctx, v.c.B().Smembers().Key(tenantKey(tenantID)).Build()).AsStrSlice()
	if err != nil {
		return nil, err
	}
	out := make([]ConnectedAgent, 0, len(ids))
	for _, id := range ids {
		online, err := v.exists(ctx, onlineKey(id))
		if err != nil {
			return nil, err
		}
		if !online {
			_ = v.c.Do(ctx, v.c.B().Srem().Key(tenantKey(tenantID)).Member(id).Build()).Error()
			continue
		}
		fields, err := v.c.Do(ctx, v.c.B().Hgetall().Key(metaKey(id)).Build()).AsStrMap()
		if err != nil {
			return nil, err
		}
		ca := ConnectedAgent{
			AgentID:  id,
			TenantID: tenantID,
			HostID:   fields["host_id"],
			Version:  fields["version"],
		}
		if ts, ok := fields["connected_at"]; ok {
			if parsed, perr := time.Parse(time.RFC3339Nano, ts); perr == nil {
				ca.ConnectedAt = parsed
			}
		}
		out = append(out, ca)
	}
	return out, nil
}

// IsOnline reports whether the agent's online key currently exists.
func (v *Valkey) IsOnline(ctx context.Context, agentID string) (bool, error) {
	return v.exists(ctx, onlineKey(agentID))
}

func (v *Valkey) exists(ctx context.Context, key string) (bool, error) {
	n, err := v.c.Do(ctx, v.c.B().Exists().Key(key).Build()).AsInt64()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

var _ Registry = (*Valkey)(nil)
