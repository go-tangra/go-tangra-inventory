package daemon

import (
	"context"
	"fmt"
	"log"
	"math"
	"time"

	collectorv1 "github.com/go-tangra/go-tangra-inventory/gen/go/inventory/collector/v1"
	"github.com/go-tangra/go-tangra-inventory/internal/collector"
	"github.com/go-tangra/go-tangra-inventory/internal/sender"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// Config holds daemon-mode configuration.
type Config struct {
	CollectorAddr string
	ClientSecret  string
	ClientID      string
	Version       string
}

const (
	baseBackoff = 1 * time.Second
	maxBackoff  = 2 * time.Minute
)

// Run performs an initial collect-and-send, then enters a reconnect loop
// that streams commands from the collector.
func Run(ctx context.Context, cfg Config) error {
	// Initial collect + send.
	if err := collectAndSend(ctx, cfg); err != nil {
		return fmt.Errorf("initial inventory submit: %w", err)
	}
	log.Println("Initial inventory submitted; entering daemon mode")

	reconnectLoop(ctx, cfg)
	return nil
}

func reconnectLoop(ctx context.Context, cfg Config) {
	attempt := 0
	for {
		select {
		case <-ctx.Done():
			log.Println("Daemon shutting down")
			return
		default:
		}

		err := streamLoop(ctx, cfg)
		if ctx.Err() != nil {
			return
		}

		attempt++
		backoff := calcBackoff(attempt)
		log.Printf("Stream disconnected (attempt %d): %v; reconnecting in %s", attempt, err, backoff)

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
	}
}

func streamLoop(ctx context.Context, cfg Config) error {
	conn, err := grpc.NewClient(cfg.CollectorAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return fmt.Errorf("dial collector: %w", err)
	}
	defer conn.Close()

	client := collectorv1.NewInventoryCollectorServiceClient(conn)

	streamCtx := ctx
	if cfg.ClientSecret != "" {
		streamCtx = metadata.AppendToOutgoingContext(ctx, "x-client-secret", cfg.ClientSecret)
	}

	stream, err := client.StreamCommands(streamCtx, &collectorv1.StreamCommandsRequest{
		ClientId:      cfg.ClientID,
		ClientVersion: cfg.Version,
	})
	if err != nil {
		return fmt.Errorf("open stream: %w", err)
	}

	log.Printf("Connected to collector at %s; waiting for commands", cfg.CollectorAddr)

	for {
		cmd, err := stream.Recv()
		if err != nil {
			return fmt.Errorf("recv: %w", err)
		}

		switch cmd.CommandType {
		case collectorv1.InventoryCommandType_INVENTORY_COMMAND_TYPE_REFRESH:
			log.Printf("Received refresh command %s", cmd.CommandId)
			handleRefresh(ctx, cfg)
		default:
			log.Printf("Unknown command type %d (id: %s), ignoring", cmd.CommandType, cmd.CommandId)
		}
	}
}

func handleRefresh(ctx context.Context, cfg Config) {
	if err := collectAndSend(ctx, cfg); err != nil {
		log.Printf("Refresh failed: %v", err)
	} else {
		log.Println("Refresh complete; inventory re-submitted")
	}
}

func collectAndSend(ctx context.Context, cfg Config) error {
	inv, err := collector.Collect()
	if err != nil {
		log.Printf("warning: collect: %v", err)
	}

	_, err = sender.Send(ctx, cfg.CollectorAddr, cfg.ClientSecret, inv)
	return err
}

func calcBackoff(attempt int) time.Duration {
	d := baseBackoff * time.Duration(math.Pow(2, float64(attempt-1)))
	if d > maxBackoff {
		d = maxBackoff
	}
	return d
}
