package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
)

type fakeAPI struct {
	mu      sync.Mutex
	enrolls map[string]bool
	submits int
}

func (f *fakeAPI) Enroll(_ context.Context, token string, ident store.Identity, _ string) (string, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if token == "bad" || f.enrolls[token] {
		return "", "", errors.New("token invalid")
	}
	f.enrolls[token] = true
	return "agent-" + ident.Hostname, "cred", nil
}

func (f *fakeAPI) Submit(context.Context, string, string, store.Inventory) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits++
	return "snap", nil
}

func TestRun(t *testing.T) {
	api := &fakeAPI{enrolls: map[string]bool{}}
	toks := []string{"a", "b", "bad", "c"}
	res := run(context.Background(), api, toks, 3, 0.5, 2, 1)
	if res.submitted != 9 || res.errors != 1 || api.submits != 9 {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.summary(), "submitted=9 errors=1") {
		t.Fatal(res.summary())
	}
}

func TestSynthesizeStableAndValid(t *testing.T) {
	a, b := Synthesize(7, 0, 1, 0), Synthesize(7, 0, 1, 0)
	if a.Identity != b.Identity || a.PrimaryIPv4 != b.PrimaryIPv4 || len(a.Networks) != 3 || len(a.Programs) != 200 {
		t.Fatalf("synthesize = %+v", a.Identity)
	}
	if Synthesize(8, 0, 1, 0).Identity == a.Identity {
		t.Fatal("identities collide")
	}
	moved := 0
	for i := 0; i < 1000; i++ {
		if Synthesize(i, 1, 1, 0.1).PrimaryIPv4 != Synthesize(i, 0, 1, 0.1).PrimaryIPv4 {
			moved++
		}
	}
	if moved < 50 || moved > 150 {
		t.Fatalf("moved %d of 1000 at change=0.1", moved)
	}
	if len(Synthesize(10, 0, 1, 0).HypervisorGuests) != 5 {
		t.Fatal("hypervisor hosts carry guests")
	}
}

func TestReadTokens(t *testing.T) {
	p := filepath.Join(t.TempDir(), "t")
	if err := os.WriteFile(p, []byte("# minted\nA\n\n B \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readTokens(p)
	if err != nil || fmt.Sprint(got) != "[A B]" {
		t.Fatalf("tokens = %v %v", got, err)
	}
	if _, err := readTokens(p + "x"); err == nil {
		t.Fatal("missing file")
	}
}
