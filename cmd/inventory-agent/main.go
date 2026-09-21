// Command inventory-agent is the Freya inventory endpoint agent. It collects a
// local hardware/software inventory and, depending on mode, prints or writes it
// as JSON and/or submits it to the off-mesh ingest edge. It can run one-shot,
// as a long-lived daemon, or install/uninstall itself as an OS service.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-freya/freya/services/inventory/internal/collector"
	"github.com/go-freya/freya/services/inventory/internal/config"
	"github.com/go-freya/freya/services/inventory/internal/daemon"
	"github.com/go-freya/freya/services/inventory/internal/winsvc"
)

// version is stamped via -ldflags "-X main.version=...".
var version = "dev"

const serviceName = "FreyaInventoryAgent"

func main() {
	var (
		configPath = flag.String("config", "", "path to agent config YAML")
		ingest     = flag.String("ingest", "", "ingest endpoint host:port (overrides config)")
		token      = flag.String("token", "", "path to enrollment token file (overrides config)")
		outputDir  = flag.String("o", "", "directory to write collected inventory JSON")
		daemonMode = flag.Bool("daemon", false, "run as a long-lived daemon (periodic submit + command stream)")
		service    = flag.String("service", "", "OS service action: install or uninstall")
		insecure   = flag.Bool("insecure", false, "use a plaintext connection to the ingest edge (development only)")
	)
	flag.Parse()

	collector.Version = version

	cfg, err := resolveConfig(*configPath, *ingest, *token, *insecure)
	if err != nil {
		fatalf("config: %v", err)
	}

	switch {
	case *service != "":
		if err := handleService(*service, *configPath, *ingest, *token, *insecure); err != nil {
			fatalf("service %s: %v", *service, err)
		}
	case *daemonMode:
		if err := runDaemon(cfg); err != nil {
			fatalf("daemon: %v", err)
		}
	default:
		if err := runOnce(cfg, *outputDir); err != nil {
			fatalf("%v", err)
		}
	}
}

// resolveConfig loads the optional config file and applies flag overrides.
func resolveConfig(configPath, ingest, token string, insecure bool) (config.AgentConfig, error) {
	cfg := config.DefaultAgent()
	if configPath != "" {
		loaded, err := config.LoadAgent(configPath)
		if err != nil {
			return cfg, err
		}
		cfg = loaded
	}
	if ingest != "" {
		cfg.IngestEndpoint = ingest
	}
	if token != "" {
		cfg.TokenFile = token
	}
	if insecure {
		cfg.Insecure = true
	}
	return cfg, nil
}

// runOnce collects a single inventory, optionally writes it as JSON, and
// optionally submits it when an ingest endpoint is configured.
func runOnce(cfg config.AgentConfig, outputDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	inv, err := collector.Collect(ctx)
	if err != nil {
		return fmt.Errorf("collect: %w", err)
	}

	if outputDir != "" {
		path, err := writeJSON(outputDir, inv.Identity.Hostname, inv)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "inventory written to %s\n", path)
	}

	if cfg.IngestEndpoint != "" {
		if err := cfg.Validate(); err != nil {
			return err
		}
		snapID, err := daemon.New(cfg, version).SubmitCollected(ctx, inv)
		if err != nil {
			return fmt.Errorf("submit: %w", err)
		}
		fmt.Fprintf(os.Stderr, "inventory submitted to %s (snapshot %s)\n", cfg.IngestEndpoint, snapID)
		return nil
	}

	// No output target at all: print to stdout so the run is useful.
	if outputDir == "" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(inv); err != nil {
			return fmt.Errorf("encode inventory: %w", err)
		}
	}
	return nil
}

// runDaemon validates config and runs the agent loop, under the SCM when the
// process is launched as a Windows service.
func runDaemon(cfg config.AgentConfig) error {
	if err := cfg.Validate(); err != nil {
		return err
	}
	d := daemon.New(cfg, version)

	if winsvc.IsWindowsService() {
		winsvc.SetupEventLog(serviceName)
		return winsvc.RunService(serviceName, d.Run)
	}

	ctx, stop := signalContext()
	defer stop()
	return d.Run(ctx)
}

// handleService installs or uninstalls the agent as an OS service. Install
// records the flags needed to run the daemon.
func handleService(action, configPath, ingest, token string, insecure bool) error {
	switch action {
	case "install":
		exe, err := winsvc.ExePath()
		if err != nil {
			return err
		}
		args := []string{"-daemon"}
		if configPath != "" {
			abs, _ := filepath.Abs(configPath)
			args = append(args, "-config", abs)
		}
		if ingest != "" {
			args = append(args, "-ingest", ingest)
		}
		if token != "" {
			abs, _ := filepath.Abs(token)
			args = append(args, "-token", abs)
		}
		if insecure {
			args = append(args, "-insecure")
		}
		if err := winsvc.Install(serviceName, "Freya Inventory Agent",
			"Collects endpoint inventory and submits it to the Freya inventory service.",
			exe, args); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "service %s installed\n", serviceName)
		return nil
	case "uninstall":
		if err := winsvc.Uninstall(serviceName); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "service %s uninstalled\n", serviceName)
		return nil
	default:
		return fmt.Errorf("unknown service action %q (use install or uninstall)", action)
	}
}

// writeJSON writes the inventory as an indented JSON file named
// <hostname>-<timestamp>.json in dir, returning the full path.
func writeJSON(dir, hostname string, v any) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	if hostname == "" {
		hostname = "unknown"
	}
	hostname = strings.ReplaceAll(hostname, string(os.PathSeparator), "_")
	name := fmt.Sprintf("%s-%s.json", hostname, time.Now().Format("20060102-150405"))
	path := filepath.Join(dir, name)

	f, err := os.Create(path) // #nosec G304 -- operator-supplied output directory
	if err != nil {
		return "", fmt.Errorf("create output file: %w", err)
	}
	defer f.Close()

	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return "", fmt.Errorf("encode inventory: %w", err)
	}
	return path, nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
