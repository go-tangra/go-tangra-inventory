//go:build linux

package agentteleport

import (
	"context"
	"testing"
	"time"
)

func TestSleepCtx(t *testing.T) {
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sleepCtx(ctx, time.Hour); err == nil {
		t.Fatal("cancelled")
	}
	if err := (execSystemctl{path: "/bin/true"}).Run(context.Background(), "is-active"); err != nil {
		t.Fatal(err)
	}
	if err := (execSystemctl{path: "/bin/false"}).Run(context.Background()); err == nil {
		t.Fatal("non-zero exit")
	}
	if in, err := NewOS(time.Minute); err == nil && (in.root != "/" || in.base != Origin) {
		t.Fatalf("os installer = %+v", in)
	}
}
