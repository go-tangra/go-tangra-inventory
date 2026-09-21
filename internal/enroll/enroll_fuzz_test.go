package enroll

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/memstore"
	"github.com/go-freya/freya/services/inventory/internal/sealed"
	"github.com/go-freya/freya/services/inventory/internal/store"
)

// FuzzEnrollToken feeds arbitrary secrets to Enroll and arbitrary credentials to
// Verify, asserting the service never panics and that anything other than the
// exact minted secret / issued credential is refused.
func FuzzEnrollToken(f *testing.F) {
	f.Add("")
	f.Add("not-a-token")
	f.Add(strings.Repeat("x", 4096))
	f.Add("\x00\x01\x02 mixed \xff bytes")
	f.Add("YQ.bB-cC_dD")

	ctx := context.Background()
	mem := memstore.New()
	env, err := sealed.NewEnvelope(make([]byte, 32))
	if err != nil {
		f.Fatalf("NewEnvelope: %v", err)
	}
	svc := New(mem, env)

	// Establish one real minted secret and one real enrolled agent so the fuzz
	// input has a (vanishingly unlikely) chance to collide with a valid value.
	realSecret, _, err := svc.MintToken(ctx, "tenant-1", "admin@example.com", "lab", time.Hour)
	if err != nil {
		f.Fatalf("MintToken: %v", err)
	}
	otherSecret, _, err := svc.MintToken(ctx, "tenant-1", "admin@example.com", "lab", time.Hour)
	if err != nil {
		f.Fatalf("MintToken: %v", err)
	}
	agentID, realCred, err := svc.Enroll(ctx, otherSecret, store.Identity{Hostname: "h"}, "1.0.0")
	if err != nil {
		f.Fatalf("seed Enroll: %v", err)
	}

	f.Fuzz(func(t *testing.T, secret string) {
		// Enroll must never panic. Only the exact un-consumed real secret can
		// succeed; everything else must be refused with ErrTokenInvalid.
		_, _, err := svc.Enroll(ctx, secret, store.Identity{Hostname: "fuzz"}, "1.0.0")
		if err != nil && !errors.Is(err, ErrTokenInvalid) {
			t.Fatalf("Enroll returned unexpected error type: %v", err)
		}
		if err == nil && secret != realSecret {
			t.Fatalf("Enroll accepted a secret that was not the minted one: %q", secret)
		}

		// Verify must never panic; only the exact issued credential authenticates.
		ag, verr := svc.Verify(ctx, agentID, secret)
		if verr != nil && !errors.Is(verr, ErrUnauthenticated) {
			t.Fatalf("Verify returned unexpected error type: %v", verr)
		}
		if verr == nil {
			if secret != realCred {
				t.Fatalf("Verify accepted a credential that was not the issued one: %q", secret)
			}
			if ag.CredentialSealed != nil {
				t.Fatalf("Verify leaked sealed credential bytes")
			}
		}
		// Verify against an unknown agent id must always be refused.
		if _, verr := svc.Verify(ctx, "no-such-agent", secret); !errors.Is(verr, ErrUnauthenticated) {
			t.Fatalf("Verify with unknown agent id not refused: %v", verr)
		}
	})
}
