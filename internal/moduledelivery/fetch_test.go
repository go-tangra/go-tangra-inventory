package moduledelivery

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	invv1 "github.com/go-tangra/go-tangra-inventory/sdk/v4/api/proto/inventory/v1"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

// TestFetchRelaysBundle (T007): the source is asked with the tenant, the
// delivery id, the VERIFIED agent's host and the item id under a deadline;
// the bundle is returned with the item's ids; the item is fetched and the
// fetch is counted and audited without material; Wipe zeroes the bytes.
func TestFetchRelaysBundle(t *testing.T) {
	f := newFix(t)
	h, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	b, err := f.svc.Fetch(ctx, a, v.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(b.Data, f.src.bundle) || b.ItemID != v.ID || b.DeliveryID != did(1) || b.Module != "sms-gw" || b.Version != "1.2.0" ||
		len(b.SHA256) != 64 {
		t.Fatalf("bundle = %+v", b)
	}
	r := f.src.reqs[0]
	if r.GetTenantId() != tenant || r.GetDeliveryId() != did(1) || r.GetHostId() != h.ID || r.GetItemId() != v.ID || !f.src.deadline {
		t.Fatalf("source request = %v (deadline %v)", r, f.src.deadline)
	}
	i := f.item(t, v.ID)
	if i.State != store.DeliveryFetched || i.Fetches != 1 || i.FetchedAt == nil {
		t.Fatalf("item = %+v", i)
	}
	for _, row := range f.mem.AuditRows() {
		for k, val := range row.Detail {
			if s, ok := val.(string); ok && strings.Contains(s, "secrets") {
				t.Fatalf("audit %s.%s carries bundle bytes", row.Action, k)
			}
		}
		if row.Action == "module_delivery_fetched" && (row.Detail["sha256"] != b.SHA256 || row.Detail["fetches"] != 1 || row.ActorID != a.ID) {
			t.Fatalf("fetched row = %+v", row)
		}
	}
	data := b.Data
	b.Wipe()
	if b.Data != nil || bytes.ContainsFunc(data, func(r rune) bool { return r != 0 }) {
		t.Fatal("bundle not zeroed")
	}
	if f.met.source["ok"] != 1 {
		t.Fatalf("source metrics = %v", f.met.source)
	}
	// Without a source timeout the call carries no deadline of ours.
	f.svc.cfg.SourceTimeout = 0
	if _, err := f.svc.Fetch(ctx, a, v.ID); err != nil || f.src.deadline {
		t.Fatalf("re-fetch: %v deadline %v", err, f.src.deadline)
	}
	if f.item(t, v.ID).Fetches != 2 {
		t.Fatal("re-fetch not counted")
	}
}

// TestFetchOnlyOwnActiveItem (T007, SC-003): another agent's, another
// tenant's, a forged, a terminal or an expired item is NotFound and audited.
func TestFetchOnlyOwnActiveItem(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	_, b := f.modHost(t, "gw-2", true)
	v := f.create(t, did(1), "gw-1")
	foreign := store.Agent{ID: a.ID, TenantID: other, HostID: a.HostID}
	moved := a
	moved.HostID = b.HostID
	for name, c := range map[string]struct {
		agent store.Agent
		id    string
	}{
		"other agent":  {b, v.ID},
		"other tenant": {foreign, v.ID},
		"forged id":    {a, store.NewID()},
		"moved agent":  {moved, v.ID},
	} {
		if _, err := f.svc.Fetch(ctx, c.agent, c.id); !errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if f.met.refused[RefusedNotFound] != 4 || f.count("module_delivery_refused") < 2 {
		t.Fatalf("refusals = %v", f.met.refused)
	}
	var ie *InvalidError
	if _, err := f.svc.Fetch(ctx, a, "../x"); !errors.As(err, &ie) {
		t.Fatalf("malformed id: %v", err)
	}
	// Expired: not served.
	f.now = v.ExpiresAt
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) || f.met.refused[RefusedNotActive] != 1 {
		t.Fatalf("expired: %v %v", err, f.met.refused)
	}
	// Terminal: not served.
	f.now = t0
	if _, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryFailed, Reason: store.ReasonBusy, HookExitCode: -1}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) || f.met.refused[RefusedNotActive] != 2 {
		t.Fatalf("terminal: %v", err)
	}
	if len(f.src.reqs) != 0 {
		t.Fatal("the source was asked for a refused fetch")
	}
	f.svc.RefusePlaintext(ctx, a, v.ID)
	if f.met.refused[RefusedPlaintext] != 1 {
		t.Fatal("plaintext refusal not counted")
	}
}

// TestFetchBudget (T007): every fetch counts; transient source failures
// answer ErrSourceUnavailable until the fifth, which fails the item
// (fetch_budget); a further fetch is refused (fetch_limit / not active).
func TestFetchBudget(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	f.src.callErr = status.Error(codes.Unavailable, "auth down")
	for n := 1; n < store.MaxModuleFetches; n++ {
		if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, ErrSourceUnavailable) {
			t.Fatalf("fetch %d: %v", n, err)
		}
		if i := f.item(t, v.ID); i.Fetches != n || !i.Active() {
			t.Fatalf("after fetch %d: %+v", n, i)
		}
	}
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("last fetch: %v", err)
	}
	if i := f.item(t, v.ID); i.State != store.DeliveryFailed || i.Reason != store.ReasonFetchBudget || i.Fetches != 5 {
		t.Fatalf("item = %+v", i)
	}
	if f.met.source["unavailable"] != 5 || f.count("module_delivery_failed") != 1 {
		t.Fatalf("metrics %v", f.met.source)
	}
	// A fetched item that used its budget is refused before the source.
	f2 := newFix(t)
	_, a2 := f2.modHost(t, "gw-1", true)
	v2 := f2.create(t, did(1), "gw-1")
	for n := 0; n < store.MaxModuleFetches; n++ {
		if _, err := f2.svc.Fetch(ctx, a2, v2.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f2.svc.Fetch(ctx, a2, v2.ID); !errors.Is(err, repo.ErrNotFound) || f2.met.refused[RefusedFetchLimit] != 1 {
		t.Fatalf("sixth fetch: %v %v", err, f2.met.refused)
	}
}

// TestFetchSourceRefuses (T007): NotFound from the source (expired,
// unknown, mismatched join or render budget) fails the item at once
// (source_refused) and the agent gets NotFound; the fetch is counted.
func TestFetchSourceRefuses(t *testing.T) {
	for name, src := range map[string]*fakeSource{
		"on call":   {callErr: notFound("no such join")},
		"on header": {streamEr: notFound("expired")},
	} {
		f := newFix(t)
		_, a := f.modHost(t, "gw-1", true)
		v := f.create(t, did(1), "gw-1")
		src.module = "sms-gw"
		if src.streamEr != nil {
			src.mutate = func([]*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk { return nil }
		}
		f.svc.src = src
		if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) {
			t.Fatalf("%s: %v", name, err)
		}
		if i := f.item(t, v.ID); i.State != store.DeliveryFailed || i.Reason != store.ReasonSourceRefused || i.Fetches != 1 {
			t.Fatalf("%s: item = %+v", name, i)
		}
		if f.met.source["refused"] != 1 {
			t.Fatalf("%s: metrics %v", name, f.met.source)
		}
	}
}

// TestFetchRejectsMalformedStreams (T007): a bundle over 12 MiB, a digest
// or size mismatch, a gap, an oversized chunk, a second header, the wrong
// module or a broken stream is never relayed (ErrSourceUnavailable, the
// fetch counted).
func TestFetchRejectsMalformedStreams(t *testing.T) {
	big := make([]byte, MaxChunkBytes+1)
	cases := map[string]func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk{
		"no header": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk { return m[1:] },
		"wrong module": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk {
			m[0].GetHeader().Module = "mail"
			return m
		},
		"empty": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk {
			m[0].GetHeader().Size = 0
			return m[:1]
		},
		"over 12 MiB": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk {
			m[0].GetHeader().Size = store.MaxModuleBundleBytes + 1
			return m
		},
		"bad digest syntax": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk {
			m[0].GetHeader().Sha256 = strings.ToUpper(m[0].GetHeader().Sha256)
			return m
		},
		"digest mismatch": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk {
			m[1].GetChunk().Data[0] ^= 0xff
			return m
		},
		"short": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk { return m[:len(m)-1] },
		"gap": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk {
			return append(m[:1], m[2:]...)
		},
		"overflow": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk {
			return append(m, chunk(int(m[0].GetHeader().Size), []byte("x")))
		},
		"chunk over 1 MiB": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk {
			m[0].GetHeader().Size = int64(len(big))
			return []*invv1.ModuleBundleChunk{m[0], chunk(0, big)}
		},
		"second header": func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk { return append(m, m[0]) },
	}
	for name, mutate := range cases {
		f := newFix(t)
		_, a := f.modHost(t, "gw-1", true)
		v := f.create(t, did(1), "gw-1")
		f.src.mutate = mutate
		if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, ErrSourceUnavailable) {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if i := f.item(t, v.ID); i.Fetches != 1 || i.State != store.DeliveryDelivered {
			t.Errorf("%s: item = %+v", name, i)
		}
		if f.met.source["bad_bundle"] != 1 {
			t.Errorf("%s: metrics %v", name, f.met.source)
		}
	}
	// A stream that breaks after the header: retryable.
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	f.src.streamEr = status.Error(codes.Unavailable, "reset")
	f.src.mutate = func(m []*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk { return m[:2] }
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("broken stream: %v", err)
	}
	// ... and one that breaks before the header.
	f.src.mutate = func([]*invv1.ModuleBundleChunk) []*invv1.ModuleBundleChunk { return nil }
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, ErrSourceUnavailable) {
		t.Fatalf("no message: %v", err)
	}
}

// TestFetchAmbiguousAgent (SR-001): a second agent claiming the host since
// the item was created makes it unsupported; nothing is rendered.
func TestFetchAmbiguousAgent(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	f.agentFor(t, "gw-1", "linux", "4.8.0", false, store.CapModuleV1)
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("fetch: %v", err)
	}
	if i := f.item(t, v.ID); i.State != store.DeliveryUnsupported || i.Reason != store.ReasonAmbiguousAgent {
		t.Fatalf("item = %+v", i)
	}
	if len(f.src.reqs) != 0 || f.count("module_delivery_unsupported") != 1 {
		t.Fatal("rendered for an ambiguous host")
	}
}

// TestFetchRaces: the item ends while the source renders (superseded):
// the bundle is dropped and the agent gets NotFound.
func TestFetchRaces(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	f.src.during = func() { f.now = f.now.Add(time.Second); _, _ = f.svc.Create(ctx, f.req(did(2), a.HostID)) }
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("superseded during render: %v", err)
	}
	if f.item(t, v.ID).State != store.DeliverySuperseded {
		t.Fatal("state")
	}
	// The source refuses after the item ended: still NotFound, no change.
	f.src.during = nil
	v2 := f.item(t, f.reg.delivered[len(f.reg.delivered)-1].Module.ItemID)
	f.src.callErr = notFound("gone")
	f.src.during = func() {
		_, _ = f.svc.Report(ctx, a, Report{ItemID: v2.ID, State: store.DeliveryFailed, Reason: store.ReasonBusy, HookExitCode: -1})
	}
	if _, err := f.svc.Fetch(ctx, a, v2.ID); !errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("refused after end: %v", err)
	}
	if i := f.item(t, v2.ID); i.Reason != store.ReasonBusy {
		t.Fatalf("item = %+v", i)
	}
}

// TestFetchStoreErrors: storage failures surface unchanged.
func TestFetchStoreErrors(t *testing.T) {
	for _, method := range []string{"GetModuleItem", "ListAgents", "UpdateModuleItem"} {
		f := newFix(t)
		_, a := f.modHost(t, "gw-1", true)
		v := f.create(t, did(1), "gw-1")
		f.mem.FailNext(method)
		if _, err := f.svc.Fetch(ctx, a, v.ID); err == nil || errors.Is(err, repo.ErrNotFound) {
			t.Errorf("%s: %v", method, err)
		}
	}
	// The update recording the fetch fails: the bundle is dropped.
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	f.src.during = func() { f.mem.FailNext("UpdateModuleItem") }
	if b, err := f.svc.Fetch(ctx, a, v.ID); err == nil || b != nil {
		t.Fatalf("mark fetched failure: %v", err)
	}
	// Failing the item fails.
	f.src.callErr = notFound("x")
	if _, err := f.svc.Fetch(ctx, a, v.ID); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("fail item failure: %v", err)
	}
	// The ambiguity failure cannot be stored.
	f2 := newFix(t)
	_, a2 := f2.modHost(t, "gw-1", true)
	v2 := f2.create(t, did(1), "gw-1")
	f2.agentFor(t, "gw-1", "linux", "4.8.0", false)
	f2.mem.FailNext("UpdateModuleItem")
	if _, err := f2.svc.Fetch(ctx, a2, v2.ID); err == nil || errors.Is(err, repo.ErrNotFound) {
		t.Fatalf("ambiguity update failure: %v", err)
	}
}

// staleGetRepo answers GetModuleItem with a copy read before the item ended.
type staleGetRepo struct {
	Repo
	item store.ModuleDeliveryItem
}

func (s staleGetRepo) GetModuleItem(context.Context, string, string) (store.ModuleDeliveryItem, error) {
	return s.item, nil
}

// TestFetchEndedAfterRead: the item ended between the ownership read and
// the counted fetch: NotFound, audited, nothing rendered.
func TestFetchEndedAfterRead(t *testing.T) {
	f := newFix(t)
	_, a := f.modHost(t, "gw-1", true)
	v := f.create(t, did(1), "gw-1")
	if _, err := f.svc.Report(ctx, a, Report{ItemID: v.ID, State: store.DeliveryFailed, HookExitCode: -1}); err != nil {
		t.Fatal(err)
	}
	f.svc.repo = staleGetRepo{Repo: f.svc.repo, item: v.ModuleDeliveryItem}
	if _, err := f.svc.Fetch(ctx, a, v.ID); !errors.Is(err, repo.ErrNotFound) || len(f.src.reqs) != 0 || f.met.refused[RefusedNotActive] != 1 {
		t.Fatalf("fetch: %v", err)
	}
}
