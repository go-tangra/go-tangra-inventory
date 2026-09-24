// Package app wires the inventory service: configuration -> Freya runtime ->
// store/sealing/enroll/registry -> the mesh HTTP+gRPC surfaces (via the gateway)
// and the SEPARATE off-mesh ingest edge that authenticated endpoint agents post
// snapshots to, plus gateway registration and maintenance workers.
package app

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"strings"
	"time"

	"github.com/go-tangra/go-tangra-lcm/sdk/v4/pkg/lcmidentity"
	"github.com/go-tangra/go-tangra/v4"
	"github.com/valkey-io/valkey-go"
	"google.golang.org/grpc"

	authv1 "github.com/go-tangra/go-tangra-auth/sdk/v4/api/proto/auth/v1"
	"github.com/go-tangra/go-tangra-auth/sdk/v4/pkg/authclient"
	"github.com/go-tangra/go-tangra-portal/sdk/v4/pkg/gatewayclient"

	"github.com/go-tangra/go-tangra-inventory/v4/internal/backup"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/config"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/enroll"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/events"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/grpcapi"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/hosts"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/httpapi"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/ingest"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/registry"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/repo/repodb"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/sealed"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/snapshots"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stats"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/store"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stream"
	"github.com/go-tangra/go-tangra-inventory/v4/internal/stream/valkeykv"
	"github.com/go-tangra/go-tangra-inventory/v4/pkg/inventorymanifest"
)

// Options override infrastructure (tests) and attach optional parts.
type Options struct {
	Logger   slog.Handler
	KEK      []byte
	Verifier httpapi.Verifier
	Freya    []freya.Option
	Migrate  bool
	Remote   fs.FS // built federated UI remote (nil serves no remote)
}

// App is the wired service.
type App struct {
	Cfg      config.Config
	Log      *slog.Logger
	Freya    *freya.App
	Store    *store.Store
	Repo     repo.Store
	Env      *sealed.Envelope
	Verifier httpapi.Verifier
	HTTP     *httpapi.Server
	Hub      *stream.Hub
	Registry registry.Registry
	Enroll   *enroll.Service

	closers []func()
	workers []func(context.Context)
}

// Build wires the service.
func Build(ctx context.Context, cfg config.Config, o Options) (a *App, err error) {
	a = &App{Cfg: cfg}
	handler := o.Logger
	if handler == nil {
		handler = slog.NewJSONHandler(os.Stderr, nil)
	}
	a.Log = slog.New(handler)

	// Mesh identity: enroll for the module's own SPIFFE SVID over lcm.
	fopts := append([]freya.Option{freya.WithLogger(handler)}, o.Freya...)
	if cfg.MeshEnroll.Enabled {
		raw, rerr := os.ReadFile(cfg.MeshEnroll.TokenFile)
		if rerr != nil {
			return nil, fmt.Errorf("inventory: mesh enroll token: %w", rerr)
		}
		prov, perr := lcmidentity.NewNet(ctx, lcmidentity.NetConfig{
			EnrollURL: cfg.MeshEnroll.EnrollURL, LCMGRPCTarget: cfg.MeshEnroll.LCMGRPCTarget,
			TenantID: cfg.MeshEnroll.TenantID, TrustDomain: cfg.Config.TrustDomain, ServiceName: cfg.Config.ServiceName,
			EnrollmentToken: strings.TrimSpace(string(raw)), Insecure: cfg.MeshEnroll.Insecure, StateFile: cfg.MeshEnroll.StateFile,
		})
		if perr != nil {
			return nil, fmt.Errorf("inventory: mesh enroll: %w", perr)
		}
		a.closers = append(a.closers, func() { _ = prov.Close() })
		fopts = append(fopts, freya.WithIdentityProvider(prov))
	}
	if a.Freya, err = freya.New(cfg.Config, fopts...); err != nil {
		return nil, err
	}
	a.closers = append(a.closers, a.Freya.Close)

	// KEK + envelope.
	kek := o.KEK
	if len(kek) == 0 {
		if kek, err = sealed.LoadKEK(cfg.KEK.Source, cfg.KEK.Path, cfg.KEK.Env); err != nil {
			return nil, fmt.Errorf("kek: %w", err)
		}
	}
	if a.Env, err = sealed.NewEnvelope(kek); err != nil {
		return nil, err
	}

	// Store (migrate then open the app pool).
	if o.Migrate {
		mdsn := cfg.DB.MigrateDSN
		if mdsn == "" {
			mdsn = cfg.DB.DSN
		}
		if err = store.Migrate(ctx, mdsn); err != nil {
			return nil, err
		}
	}
	if a.Store, err = store.Open(ctx, cfg.DB.DSN, cfg.DB.MaxConns); err != nil {
		return nil, err
	}
	a.closers = append(a.closers, a.Store.Close)
	a.Repo = repodb.New(a.Store)

	// Verifier (platform token) from auth.
	a.Verifier = o.Verifier
	if a.Verifier == nil {
		conn, cerr := a.Freya.Client(ctx, "auth")
		if cerr != nil {
			return nil, fmt.Errorf("auth client: %w", cerr)
		}
		a.Verifier = authclient.New(authclient.Config{Issuer: cfg.Gateway.Issuer},
			authclient.GRPCKeys{Client: authv1.NewKeysClient(conn)},
			authclient.GRPCRevocations{Client: authv1.NewSessionsClient(conn)})
	}

	// Event bus (Valkey Streams) for realtime + the shared agent registry.
	sc := valkeykv.Config{Addresses: cfg.Valkey.Addresses, Username: cfg.Valkey.Username, Password: cfg.Valkey.Password, AllowPlaintext: cfg.Valkey.AllowPlaintext}
	if cfg.Valkey.CAFile != "" {
		if sc.CAPEM, err = os.ReadFile(cfg.Valkey.CAFile); err != nil {
			return nil, fmt.Errorf("valkey ca: %w", err)
		}
	}
	streamClient, serr := valkeykv.New(sc)
	if serr != nil {
		return nil, fmt.Errorf("event bus: %w", serr)
	}
	a.Hub = stream.NewHub(streamClient, stream.Config{}, a.Log)
	a.closers = append(a.closers, a.Hub.Close)

	// Shared agent connection registry: Valkey-backed for cross-instance refresh
	// when Valkey is configured; a single-instance in-memory registry otherwise.
	instanceID := cfg.Registry.InstanceID
	if instanceID == "" {
		instanceID, _ = os.Hostname()
	}
	if len(cfg.Valkey.Addresses) > 0 {
		vc, verr := valkey.NewClient(valkey.ClientOption{
			InitAddress: cfg.Valkey.Addresses, Username: cfg.Valkey.Username, Password: cfg.Valkey.Password, DisableCache: true,
		})
		if verr != nil {
			return nil, fmt.Errorf("registry valkey: %w", verr)
		}
		a.closers = append(a.closers, vc.Close)
		a.Registry = registry.NewValkey(vc, instanceID)
	} else {
		a.Registry = registry.NewMemory()
	}

	// Services.
	pub := events.HubPublisher{Hub: a.Hub}
	hostsSvc := hosts.New(a.Repo)
	snapsSvc := snapshots.New(a.Repo, hostsSvc, pub)
	statsSvc := stats.New(a.Repo)
	statsSvc.SetStaleAfter(cfg.StaleAfter())
	backupSvc := backup.New(a.Repo)
	a.Enroll = enroll.New(a.Repo, a.Env)

	// Mesh HTTP surface (reached only through the gateway).
	hopts := []httpapi.Option{httpapi.WithVerifier(a.Verifier)}
	if o.Remote != nil {
		hopts = append(hopts, httpapi.WithRemote(o.Remote))
	}
	if a.HTTP, err = httpapi.NewHandler(a.Freya, hopts...); err != nil {
		return nil, err
	}
	a.HTTP.Register(httpapi.Deps{
		Hosts: hostsSvc, Snapshots: snapsSvc, Stats: statsSvc, Backup: backupSvc,
		Enroll: a.Enroll, Registry: a.Registry, Hub: a.Hub,
	})
	a.Freya.HTTP().HandlePrefix("/", a.HTTP.Handler())

	// Service-to-service gRPC surface (inventory.v1), SPIFFE mTLS, not gateway-proxied.
	grpcapi.Register(a.Freya.GRPC(), grpcapi.Deps{
		Hosts: hostsSvc, Snapshots: snapsSvc, Stats: statsSvc, Backup: backupSvc,
		Enroll: a.Enroll, Registry: a.Registry,
	})

	// Off-mesh INGEST EDGE: a separate, network-isolated gRPC listener that
	// authenticates untrusted endpoint agents by their per-agent credential.
	// It serves TLS from ingest.tls_cert_file/tls_key_file (hot-reloaded); only
	// the development opt-out ingest.insecure serves plaintext. A missing or
	// unloadable certificate refuses start.
	ingestSrv := ingest.New(a.Enroll, snapsSvc, a.Registry, a.Repo, cfg.Limits.MaxSnapshotBytes, instanceID)
	var ingestTLS *ingest.CertLoader
	if !cfg.Ingest.Insecure {
		if ingestTLS, err = ingest.NewCertLoader(cfg.Ingest.TLSCertFile, cfg.Ingest.TLSKeyFile); err != nil {
			return nil, err
		}
	}
	a.workers = append(a.workers, func(c context.Context) { a.serveIngest(c, ingestSrv, ingestTLS) })

	// Maintenance: mark stale hosts + purge old snapshots on an interval.
	a.workers = append(a.workers, a.maintenance)
	return a, nil
}

// serveIngest runs the off-mesh ingest gRPC listener until ctx is cancelled:
// TLS with the hot-reloaded certificate when tlsCert is set, plaintext only for
// the development opt-out (nil).
func (a *App) serveIngest(ctx context.Context, s *ingest.Server, tlsCert *ingest.CertLoader) {
	addr := a.Cfg.IngestAddr()
	if addr == "" {
		a.Log.Error("ingest edge: no ingest.addr configured; ingest disabled")
		return
	}
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		a.Log.Error("ingest edge: listen", "addr", addr, "err", err)
		return
	}
	var opts []grpc.ServerOption
	mode := "plaintext (ingest.insecure, development only)"
	if tlsCert != nil {
		opts = append(opts, tlsCert.TransportOption())
		tlsCert.Watch(ctx, a.Cfg.IngestTLSReload(), func(err error) {
			a.Log.Error("ingest edge: certificate reload failed; keeping the current certificate", "err", err)
		})
		mode = "tls"
	}
	gs := ingest.NewGRPCServer(s, opts...)
	go func() { <-ctx.Done(); gs.GracefulStop() }()
	a.Log.Info("ingest edge listening", "addr", addr, "transport", mode)
	if err := gs.Serve(lis); err != nil && ctx.Err() == nil {
		a.Log.Error("ingest edge: serve", "err", err)
	}
}

// maintenance marks hosts stale and purges snapshots beyond the retention window.
func (a *App) maintenance(ctx context.Context) {
	t := time.NewTicker(a.Cfg.PurgeInterval())
	defer t.Stop()
	run := func() {
		now := time.Now()
		if _, err := a.Repo.MarkStaleHosts(ctx, now.Add(-a.Cfg.StaleAfter())); err != nil {
			a.Log.Warn("mark stale hosts", "err", err)
		}
		if a.Cfg.Retention.Days > 0 {
			if n, err := a.Repo.PurgeSnapshots(ctx, now.Add(-a.Cfg.RetentionWindow())); err != nil {
				a.Log.Warn("purge snapshots", "err", err)
			} else if n > 0 {
				a.Log.Info("purged old snapshots", "count", n)
			}
		}
	}
	run()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}

// Run starts the verifier, gateway registration, workers, and the Freya runtime.
func (a *App) Run(ctx context.Context) error {
	wctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if v, ok := a.Verifier.(*authclient.Verifier); ok {
		go func() {
			for wctx.Err() == nil {
				if err := v.Start(wctx, func(err error) { a.Log.Warn("verifier", "err", err) }); err == nil {
					return
				}
				select {
				case <-wctx.Done():
					return
				case <-time.After(2 * time.Second):
				}
			}
		}()
	}
	go a.register(wctx)
	for _, w := range a.workers {
		go w(wctx)
	}
	go func() {
		for wctx.Err() == nil && !a.Freya.Ready() {
			time.Sleep(100 * time.Millisecond)
		}
		a.seedLoop(wctx)
	}()
	return a.Freya.Run(ctx)
}

// Close releases resources.
func (a *App) Close() {
	for i := len(a.closers) - 1; i >= 0; i-- {
		a.closers[i]()
	}
	a.closers = nil
}

// register keeps the gateway lease for the manifest.
func (a *App) register(ctx context.Context) {
	for ctx.Err() == nil && !a.Freya.Ready() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
	man, err := inventorymanifest.Manifest()
	if err != nil {
		a.Log.Error("gateway manifest", "err", err)
		return
	}
	httpEP, err := a.Freya.HTTP().Endpoint()
	if err != nil {
		a.Log.Error("gateway registration: http endpoint", "err", err)
		return
	}
	grpcEP, err := a.Freya.GRPC().Endpoint()
	if err != nil {
		a.Log.Error("gateway registration: grpc endpoint", "err", err)
		return
	}
	var client *gatewayclient.Client
	for ctx.Err() == nil && client == nil {
		conn, cerr := a.Freya.Client(ctx, a.Cfg.Gateway.Service)
		if cerr != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(2 * time.Second):
			}
			continue
		}
		client, err = gatewayclient.New(conn, gatewayclient.Options{Manifest: man, HTTPURL: "https://" + httpEP.Host, GRPCTarget: grpcEP.Host, Logger: a.Log,
			OnState: func(s gatewayclient.State) {
				a.Log.Info("gateway lease", "registered", s.Registered, "lease", s.LeaseID, "err", s.Err)
			}})
		if err != nil {
			a.Log.Error("gateway client", "err", err)
			return
		}
	}
	if client != nil {
		if err := client.Run(ctx); err != nil {
			a.Log.Error("gateway registration", "err", err)
		}
	}
}
