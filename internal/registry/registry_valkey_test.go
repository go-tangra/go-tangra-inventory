package registry

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/valkey-io/valkey-go"
)

// miniredis refuses commands on a subscribed RESP3 connection; RESP2 makes
// the client keep Pub/Sub on a connection of its own.
func newTestValkey(t *testing.T) (*Valkey, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{mr.Addr()}, DisableCache: true, AlwaysRESP2: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	v := NewValkey(c, "inst-1")
	v.ttl = 3 * time.Second // heartbeat every second
	return v, mr
}

func connectedIDs(t *testing.T, v *Valkey, tenant string) map[string]ConnectedAgent {
	t.Helper()
	list, err := v.ListConnected(context.Background(), tenant)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]ConnectedAgent{}
	for _, a := range list {
		out[a.AgentID] = a
	}
	return out
}

// A Valkey restart (or a pruned tenant-set member) must not leave a still
// connected agent listed offline: the heartbeat restores the registration.
func TestValkeyHeartbeatRestoresLostRegistration(t *testing.T) {
	v, mr := newTestValkey(t)
	_, unreg, err := v.Register(context.Background(), ConnectedAgent{AgentID: "a1", TenantID: "t1", HostID: "h1", Version: "4.12.0"})
	if err != nil {
		t.Fatal(err)
	}
	defer unreg()
	if _, ok := connectedIDs(t, v, "t1")["a1"]; !ok {
		t.Fatal("agent not listed after Register")
	}

	mr.FlushAll() // what a Valkey restart without persistence leaves behind
	if len(connectedIDs(t, v, "t1")) != 0 {
		t.Fatal("registry not empty after flush")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if a, ok := connectedIDs(t, v, "t1")["a1"]; ok {
			if a.HostID != "h1" || a.Version != "4.12.0" || a.ConnectedAt.IsZero() {
				t.Fatalf("restored metadata = %+v", a)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("heartbeat did not restore the registration")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// A superseded connection closing after the agent reconnected must not take
// the new connection's registration with it.
func TestValkeySupersededUnregisterKeepsNewConnection(t *testing.T) {
	v, _ := newTestValkey(t)
	a := ConnectedAgent{AgentID: "a1", TenantID: "t1", HostID: "h1"}
	_, unregOld, err := v.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	_, unregNew, err := v.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond) // let the old connection's heartbeat run once
	unregOld()
	if _, ok := connectedIDs(t, v, "t1")["a1"]; !ok {
		t.Fatal("old connection's unregister removed the new connection")
	}
	if on, _ := v.IsOnline(context.Background(), "a1"); !on {
		t.Fatal("agent not online after old unregister")
	}
	unregNew()
	if _, ok := connectedIDs(t, v, "t1")["a1"]; ok {
		t.Fatal("agent still listed after its connection closed")
	}
}
