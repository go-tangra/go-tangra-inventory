package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// Register writes the connection's registration (online marker, metadata and
// tenant-set membership), starts a heartbeat and a Pub/Sub subscriber, and
// returns the command channel with an idempotent unregister.
//
// The online marker holds a per-connection owner token. The heartbeat
// rewrites the whole registration while the marker is this connection's (or
// gone), so a Valkey restart or a pruned set member heals within ttl/3
// instead of leaving a connected agent listed offline until it reconnects.
// Unregister removes the registration only while this connection still owns
// it, so a superseded connection closing late does not mark the agent that
// reconnected offline.
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
	owner := v.instanceID + "/" + connToken()

	// A new connection takes the registration over unconditionally.
	if err := v.write(ctx, a, owner, sec, true); err != nil {
		return nil, nil, err
	}

	runCtx, cancel := context.WithCancel(context.Background())
	out := make(chan Command, deliverBuffer)
	var wg sync.WaitGroup

	// Heartbeat: keep the registration alive (so a crashed instance's agents
	// expire out of the registry) and restore whatever part of it is missing.
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
				_ = v.write(runCtx, a, owner, sec, false)
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
			v.remove(tctx, a, owner)
			wg.Wait()
			close(out)
		})
	}
	return out, unregister, nil
}

// write stores the registration: the online marker (owner token, with TTL),
// the metadata hash (with TTL) and the tenant-set membership. Without force
// (the heartbeat) it writes nothing while another connection owns the
// marker. Single-key commands only, so the keys may live in different
// cluster slots; the owner check and the writes are not atomic, and a lost
// race heals on the next heartbeat.
func (v *Valkey) write(ctx context.Context, a ConnectedAgent, owner string, sec int64, force bool) error {
	if !force {
		cur, err := v.c.Do(ctx, v.c.B().Get().Key(onlineKey(a.AgentID)).Build()).ToString()
		if err != nil && !valkey.IsValkeyNil(err) {
			return err
		}
		if err == nil && cur != owner {
			return nil // superseded by a newer connection
		}
	}
	if err := v.c.Do(ctx, v.c.B().Set().Key(onlineKey(a.AgentID)).Value(owner).ExSeconds(sec).Build()).Error(); err != nil {
		return err
	}
	// Metadata and membership are best-effort; the heartbeat rewrites them.
	for _, r := range v.c.DoMulti(ctx,
		v.c.B().Hset().Key(metaKey(a.AgentID)).FieldValue().
			FieldValue("agent_id", a.AgentID).
			FieldValue("tenant_id", a.TenantID).
			FieldValue("host_id", a.HostID).
			FieldValue("version", a.Version).
			FieldValue("connected_at", a.ConnectedAt.UTC().Format(time.RFC3339Nano)).
			Build(),
		v.c.B().Expire().Key(metaKey(a.AgentID)).Seconds(sec).Build(),
		v.c.B().Sadd().Key(tenantKey(a.TenantID)).Member(a.AgentID).Build(),
	) {
		_ = r.Error()
	}
	return nil
}

// remove deletes the registration while owner still owns the online marker,
// so a superseded connection closing late leaves the newer one in place.
func (v *Valkey) remove(ctx context.Context, a ConnectedAgent, owner string) {
	cur, err := v.c.Do(ctx, v.c.B().Get().Key(onlineKey(a.AgentID)).Build()).ToString()
	if err == nil && cur != owner {
		return
	}
	if err != nil && !valkey.IsValkeyNil(err) {
		return
	}
	v.c.DoMulti(ctx,
		v.c.B().Del().Key(onlineKey(a.AgentID)).Build(),
		v.c.B().Del().Key(metaKey(a.AgentID)).Build(),
		v.c.B().Srem().Key(tenantKey(a.TenantID)).Member(a.AgentID).Build(),
	)
}

// connToken returns a random token that tells this connection's registration
// apart from a later one of the same agent.
func connToken() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
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
