package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"time"

	kratoshttp "github.com/go-kratos/kratos/v2/transport/http"
	swaggerUI "github.com/tx7do/kratos-swagger-ui"

	collectorv1 "github.com/go-tangra/go-tangra-inventory/gen/go/inventory/collector/v1"
	"github.com/go-tangra/go-tangra-inventory/internal/config"
	"github.com/go-tangra/go-tangra-inventory/internal/store"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

// Run starts the gRPC and HTTP servers and blocks until the context is cancelled.
func Run(ctx context.Context, cfg *config.Config, openApiData []byte) error {
	db, err := store.New(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()

	cmdReg := NewCommandRegistry()
	handler := NewHandler(db, cmdReg)

	// gRPC server with client-secret auth interceptors (unary + stream).
	grpcSrv := grpc.NewServer(
		grpc.ChainUnaryInterceptor(ClientSecretInterceptor(cfg.ClientSecret)),
		grpc.ChainStreamInterceptor(ClientSecretStreamInterceptor(cfg.ClientSecret)),
	)
	collectorv1.RegisterInventoryCollectorServiceServer(grpcSrv, handler)
	reflection.Register(grpcSrv)

	lis, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen gRPC on %s: %w", cfg.Listen, err)
	}

	// Graceful shutdown when the caller cancels the context.
	go func() {
		<-ctx.Done()
		log.Println("Shutting down...")
		grpcSrv.GracefulStop()
	}()

	// Optional retention purge goroutine.
	if cfg.RetentionDays > 0 {
		go runPurgeLoop(ctx, db, cfg.RetentionDays, cfg.PurgeInterval)
	}

	// HTTP server with API-secret middleware and service routes.
	httpSrv := kratoshttp.NewServer(
		kratoshttp.Address(cfg.HTTPListen),
		kratoshttp.Middleware(ApiSecretMiddleware(cfg.ApiSecret)),
	)
	collectorv1.RegisterInventoryCollectorServiceHTTPServer(httpSrv, handler)

	// Swagger UI (registered via HandlePrefix — bypasses middleware chain).
	if cfg.EnableSwagger && len(openApiData) > 0 {
		swaggerUI.RegisterSwaggerUIServerWithOption(
			httpSrv,
			swaggerUI.WithTitle("Inventory Collector"),
			swaggerUI.WithMemoryData(openApiData, "yaml"),
		)
		log.Printf("Swagger UI available at http://%s/docs/", cfg.HTTPListen)
	}

	go func() {
		if err := httpSrv.Start(ctx); err != nil {
			log.Printf("HTTP server error: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		_ = httpSrv.Stop(context.Background())
	}()

	log.Printf("Inventory Collector gRPC listening on %s (db: %s)", cfg.Listen, cfg.DatabasePath)
	if cfg.RetentionDays > 0 {
		log.Printf("Retention: %d days, purge interval: %s", cfg.RetentionDays, cfg.PurgeInterval)
	}

	return grpcSrv.Serve(lis)
}

func runPurgeLoop(ctx context.Context, db *store.Store, retentionDays int, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			olderThan := time.Duration(retentionDays) * 24 * time.Hour
			n, err := db.Purge(ctx, olderThan)
			if err != nil {
				log.Printf("Purge error: %v", err)
			} else if n > 0 {
				log.Printf("Purged %d records older than %d days", n, retentionDays)
			}
		}
	}
}
