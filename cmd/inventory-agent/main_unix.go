//go:build !windows

package main

import (
	"context"
	"os/signal"
	"syscall"
)

// signalContext returns a context canceled on SIGINT or SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
}
