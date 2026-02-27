package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/go-tangra/go-tangra-inventory/cmd/collector/assets"
	"github.com/go-tangra/go-tangra-inventory/internal/config"
	"github.com/go-tangra/go-tangra-inventory/internal/server"
	"github.com/go-tangra/go-tangra-inventory/internal/store"
	"github.com/go-tangra/go-tangra-inventory/internal/winsvc"
)

var (
	version    = "dev"
	commitHash = "unknown"
	buildDate  = "unknown"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "inventory-collector",
	Short: "Inventory Collector - gRPC daemon that stores hardware inventory data",
	Long: `Inventory Collector receives SMBIOS hardware inventory data via gRPC
from go-tangra-inventory agents and stores it in a local SQLite database.

Run without a subcommand to start the daemon (equivalent to 'serve').`,
	RunE: runServe,
}

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the gRPC collector daemon",
	RunE:  runServe,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("inventory-collector %s (commit: %s, built: %s)\n", version, commitHash, buildDate)
	},
}

var purgeCmd = &cobra.Command{
	Use:   "purge",
	Short: "Purge inventory records older than the specified number of days",
	RunE:  runPurge,
}

var purgeDays int

const serviceName = "TangraInventoryCollector"

var serviceCmd = &cobra.Command{
	Use:   "service",
	Short: "Manage Windows service installation",
}

var serviceInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install as a Windows service",
	RunE:  runServiceInstall,
}

var serviceUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Uninstall the Windows service",
	RunE:  runServiceUninstall,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "config file (default: ./configs/collector.yaml)")
	rootCmd.PersistentFlags().String("listen", "", "gRPC listen address (default :9550)")
	rootCmd.PersistentFlags().String("http-listen", "", "HTTP listen address for Swagger UI (default :9551)")
	rootCmd.PersistentFlags().String("database", "", "SQLite database path (default inventory.db)")
	rootCmd.PersistentFlags().String("client-secret", "", "secret for gRPC inventory agents (empty = no auth)")
	rootCmd.PersistentFlags().String("api-secret", "", "secret for REST API clients (empty = no auth)")

	purgeCmd.Flags().IntVar(&purgeDays, "days", 90, "purge records older than this many days")

	serviceCmd.AddCommand(serviceInstallCmd)
	serviceCmd.AddCommand(serviceUninstallCmd)

	rootCmd.AddCommand(serveCmd)
	rootCmd.AddCommand(versionCmd)
	rootCmd.AddCommand(purgeCmd)
	rootCmd.AddCommand(serviceCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runServe(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// CLI flag overrides.
	if v, _ := cmd.Flags().GetString("listen"); v != "" {
		cfg.Listen = v
	}
	if v, _ := cmd.Flags().GetString("http-listen"); v != "" {
		cfg.HTTPListen = v
	}
	if v, _ := cmd.Flags().GetString("database"); v != "" {
		cfg.DatabasePath = v
	}
	if v, _ := cmd.Flags().GetString("client-secret"); v != "" {
		cfg.ClientSecret = v
	}
	if v, _ := cmd.Flags().GetString("api-secret"); v != "" {
		cfg.ApiSecret = v
	}

	// Windows service mode.
	if winsvc.IsWindowsService() {
		winsvc.SetupEventLog(serviceName)
		return winsvc.RunService(serviceName, func(ctx context.Context) error {
			return server.Run(ctx, cfg, assets.OpenApiData)
		})
	}

	// Interactive mode: shut down on SIGINT / SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	return server.Run(ctx, cfg, assets.OpenApiData)
}

func runServiceInstall(_ *cobra.Command, _ []string) error {
	exePath, err := winsvc.ExePath()
	if err != nil {
		return err
	}

	var svcArgs []string
	svcArgs = append(svcArgs, "serve")
	if cfgFile != "" {
		svcArgs = append(svcArgs, "--config", cfgFile)
	}

	if err := winsvc.Install(
		serviceName,
		"Tangra Inventory Collector",
		"Receives hardware inventory from agents via gRPC and stores it locally.",
		exePath,
		svcArgs,
	); err != nil {
		return err
	}

	log.Printf("Service %s installed successfully", serviceName)
	return nil
}

func runServiceUninstall(_ *cobra.Command, _ []string) error {
	if err := winsvc.Uninstall(serviceName); err != nil {
		return err
	}
	log.Printf("Service %s uninstalled successfully", serviceName)
	return nil
}

func runPurge(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if v, _ := cmd.Flags().GetString("database"); v != "" {
		cfg.DatabasePath = v
	}

	db, err := store.New(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	n, err := db.Purge(context.Background(), time.Duration(purgeDays)*24*time.Hour)
	if err != nil {
		return fmt.Errorf("purge: %w", err)
	}

	fmt.Printf("Purged %d records older than %d days\n", n, purgeDays)
	return nil
}
