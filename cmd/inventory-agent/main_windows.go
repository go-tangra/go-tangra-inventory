//go:build windows

package main

import (
	"context"
	"os"
	"os/signal"
)

// signalContext returns a context canceled on an interactive interrupt. When the
// agent runs under the SCM the service handler manages the lifecycle instead.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt)
}
