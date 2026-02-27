package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/go-tangra/go-tangra-inventory/internal/collector"
	"github.com/go-tangra/go-tangra-inventory/internal/daemon"
	"github.com/go-tangra/go-tangra-inventory/internal/sender"
	"github.com/go-tangra/go-tangra-inventory/internal/winsvc"
)

// Set via ldflags.
var version = "dev"

const serviceName = "TangraInventoryAgent"

func main() {
	outputDir := flag.String("o", "", "directory path to save inventory JSON (filename: HOSTNAME-DATE-TIME.json)")
	collectorAddr := flag.String("collector", "", "inventory collector gRPC address (e.g. 192.168.1.10:9550)")
	collectorSecret := flag.String("secret", "", "client secret for collector authentication")
	daemonMode := flag.Bool("daemon", false, "run in daemon mode: stay connected and accept refresh commands")
	serviceAction := flag.String("service", "", "Windows service action: install or uninstall")
	flag.Parse()

	// Service install/uninstall actions.
	if *serviceAction != "" {
		if err := handleServiceAction(*serviceAction, *collectorAddr, *collectorSecret); err != nil {
			fmt.Fprintf(os.Stderr, "error: service %s: %v\n", *serviceAction, err)
			os.Exit(1)
		}
		return
	}

	// Daemon mode: requires -collector, stays connected via streaming.
	if *daemonMode {
		if *collectorAddr == "" {
			fmt.Fprintln(os.Stderr, "error: -collector is required in daemon mode")
			os.Exit(1)
		}

		hostname, _ := os.Hostname()
		daemonCfg := daemon.Config{
			CollectorAddr: *collectorAddr,
			ClientSecret:  *collectorSecret,
			ClientID:      hostname,
			Version:       version,
		}

		// Windows service mode.
		if winsvc.IsWindowsService() {
			winsvc.SetupEventLog(serviceName)
			if err := winsvc.RunService(serviceName, func(ctx context.Context) error {
				return daemon.Run(ctx, daemonCfg)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "error: service: %v\n", err)
				os.Exit(1)
			}
			return
		}

		// Interactive daemon mode.
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		if err := daemon.Run(ctx, daemonCfg); err != nil {
			fmt.Fprintf(os.Stderr, "error: daemon: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// One-shot mode (original behavior).
	inv, err := collector.Collect()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: %v\n", err)
	}

	// Send to collector if address is provided.
	if *collectorAddr != "" {
		id, err := sender.Send(context.Background(), *collectorAddr, *collectorSecret, inv)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: sending to collector: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "inventory submitted to %s (id: %d)\n", *collectorAddr, id)
	}

	// Write to file or stdout (skip if collector-only mode with no -o).
	if *collectorAddr != "" && *outputDir == "" {
		return
	}

	var w *os.File
	var outputPath string
	if *outputDir != "" {
		if err := os.MkdirAll(*outputDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot create output directory: %v\n", err)
			os.Exit(1)
		}

		hostname := inv.Hostname
		if hostname == "" {
			hostname = "unknown"
		}
		hostname = strings.ReplaceAll(hostname, string(os.PathSeparator), "_")
		timestamp := time.Now().Format("20060102-150405")
		filename := fmt.Sprintf("%s-%s.json", hostname, timestamp)
		user, err := collector.GetUserInfo()
		if err != nil {
			fmt.Printf("warning: cannot get user info: %v\n", err)
		} else {
			filename = fmt.Sprintf("%s-%s.json", user, timestamp)
		}
		outputPath = filepath.Join(*outputDir, filename)

		f, err := os.Create(outputPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: cannot create output file: %v\n", err)
			os.Exit(1)
		}
		defer f.Close()
		w = f
	} else {
		w = os.Stdout
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(inv); err != nil {
		fmt.Fprintf(os.Stderr, "error: encoding inventory: %v\n", err)
		os.Exit(1)
	}

	if outputPath != "" {
		fmt.Fprintf(os.Stderr, "inventory written to %s\n", outputPath)
	}
}

func handleServiceAction(action, collectorAddr, secret string) error {
	switch action {
	case "install":
		if collectorAddr == "" {
			return fmt.Errorf("-collector is required for service install")
		}
		exePath, err := winsvc.ExePath()
		if err != nil {
			return err
		}
		args := []string{"-collector", collectorAddr, "-secret", secret, "-daemon"}
		if err := winsvc.Install(
			serviceName,
			"Tangra Inventory Agent",
			"Collects hardware inventory and streams commands from the collector.",
			exePath,
			args,
		); err != nil {
			return err
		}
		log.Printf("Service %s installed successfully", serviceName)
		return nil

	case "uninstall":
		if err := winsvc.Uninstall(serviceName); err != nil {
			return err
		}
		log.Printf("Service %s uninstalled successfully", serviceName)
		return nil

	default:
		return fmt.Errorf("unknown service action %q (use install or uninstall)", action)
	}
}
