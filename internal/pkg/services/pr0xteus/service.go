package pr0xteus

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/psyb0t/aichteeteapee/serbewr"
	"github.com/psyb0t/aichteeteapee/serbewr/middleware"
	"github.com/psyb0t/ctxerrors"
	"github.com/psyb0t/ctxscope"
)

// ServiceName is the canonical service name used by service-manager
// + log lines. Matches the directory name.
const ServiceName = "pr0xteus"

const (
	httpReadHeaderTimeout = 5 * time.Second
	httpShutdownTimeout   = 5 * time.Second
)

// Service is the standalone pr0xteus service. Boots the HTTP API
// (via aichteeteapee/serbewr) + the prometheus /metrics listener
// (separate port to keep the scrape surface internal) + the reaper.
// Lifecycle: New() → Run(ctx) blocks; Stop(ctx) drains.
type Service struct {
	cfg      Config
	apiToken []byte
	mgr      *Manager
	spawner  Spawner
	reaper   *Reaper

	apiSrv     *serbewr.Server
	metricsSrv *http.Server

	httpErr chan error
}

// New is the zero-arg constructor. All wiring happens at New() so a
// misconfigured service fails fast at boot instead of half-booting
// and dying mid-traffic.
func New() (*Service, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load pr0xteus config")
	}

	apiToken, err := ValidateAPIToken(cfg.APIToken)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "validate API token")
	}

	specs, known, err := LoadPoolSpecs(cfg.PoolsFile, cfg.BundleDir)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load pools.yaml")
	}

	router, err := LoadRouter(cfg.RoutingFile, cfg.DefaultPool, known)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "load egress-routing.yaml")
	}

	spawner, err := NewCellSpawner(cfg)
	if err != nil {
		return nil, ctxerrors.Wrap(err, "new spawner")
	}

	mgr := NewManager(cfg, specs, router, spawner)

	return &Service{
		cfg:      cfg,
		apiToken: apiToken,
		mgr:      mgr,
		spawner:  spawner,
		reaper:   NewReaper(cfg, mgr, spawner, nil),
	}, nil
}

// Name — servicepack Service interface (still used when pr0xteus
// is embedded as a servicepack familiar in tests).
func (s *Service) Name() string { return ServiceName }

// Run blocks until ctx is cancelled OR an HTTP server exits with a
// non-shutdown error.
func (s *Service) Run(ctx context.Context) error {
	logger := ctxscope.GetLogger(ctx)
	logger.Info(
		"starting pr0xteus",
		"service", ServiceName,
		"http_addr", s.cfg.HTTPAddr,
		"metrics_addr", s.cfg.MetricsAddr,
		"pools_file", s.cfg.PoolsFile,
		"bundle_dir", s.cfg.BundleDir,
		"default_pool", s.cfg.DefaultPool,
	)

	// Boot-time orphan reconcile: empty keep-set → everything
	// labelled pr0xteus.managed gets killed. In-memory
	// PoolState is rebuilt from cold so any previously-running
	// cells are unreachable to us anyway.
	if gs, ok := s.spawner.(*CellSpawner); ok {
		reaped, err := gs.ReapOrphans(ctx, nil)
		if err != nil {
			logger.Warn("boot orphan reap failed", "err", err)
		} else if reaped > 0 {
			logger.Info("boot orphan reap removed cells",
				"count", reaped)
		}
	}

	s.reaper.Start(ctx)

	s.httpErr = make(chan error, 2) //nolint:mnd // api + metrics

	if err := s.startAPI(ctx); err != nil {
		return ctxerrors.Wrap(err, "start api")
	}

	s.startMetrics(ctx)

	logger.Info("pr0xteus running, waiting for SIGTERM")

	select {
	case <-ctx.Done():
		logger.Info("pr0xteus context cancelled")

		return nil
	case err := <-s.httpErr:
		return ctxerrors.Wrap(err, "http server failed")
	}
}

// Stop drains the HTTP servers and reaper, then removes this controller's
// tracked cells without touching any other controller's scope.
func (s *Service) Stop(ctx context.Context) error {
	logger := ctxscope.GetLogger(ctx)
	logger.Info("stopping pr0xteus")

	if s.apiSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(
			ctx, httpShutdownTimeout,
		)

		if err := s.apiSrv.Stop(shutdownCtx); err != nil {
			logger.Warn("api server shutdown incomplete", "err", err)
		}

		cancel()
	}

	if s.metricsSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(
			ctx, httpShutdownTimeout,
		)

		if err := s.metricsSrv.Shutdown(shutdownCtx); err != nil {
			logger.Warn("metrics server shutdown incomplete", "err", err)
		}

		cancel()
	}

	if s.reaper != nil {
		s.reaper.Shutdown()
	}

	if s.mgr != nil {
		s.mgr.Close(ctx)
	}

	return nil
}

// startAPI builds the serbewr.Server with our middleware chain +
// route registrations, then launches it in a goroutine. Failures
// surface via s.httpErr so Run unblocks cleanly.
func (s *Service) startAPI(ctx context.Context) error {
	logger := ctxscope.GetLogger(ctx)
	api := NewAPIServer(s.mgr, s.apiToken)
	clear(s.apiToken)

	cfg := serbewr.Config{
		ListenAddress:     s.cfg.HTTPAddr,
		ReadHeaderTimeout: httpReadHeaderTimeout,
		ShutdownTimeout:   httpShutdownTimeout,
	}

	srv, err := serbewr.NewWithConfigAndLogger(cfg, logger)
	if err != nil {
		return ctxerrors.Wrap(err, "new serbewr server")
	}

	s.apiSrv = srv

	router := buildAPIRouter(api)

	go func() {
		if err := srv.Start(ctx, router); err != nil &&
			!errors.Is(err, context.Canceled) {
			logger.Error("api server stopped", "err", err)

			s.httpErr <- err
		}
	}()

	logger.Info("pr0xteus api listening", "addr", s.cfg.HTTPAddr)

	return nil
}

// buildAPIRouter wires the authenticated control-plane routes onto a serbewr
// router with the standard middleware chain.
func buildAPIRouter(api *APIServer) *serbewr.Router {
	return &serbewr.Router{
		GlobalMiddlewares: []middleware.Middleware{
			middleware.RequestID(),
			middleware.Logger(),
			middleware.Recovery(),
			middleware.SecurityHeaders(),
		},
		Groups: []serbewr.GroupConfig{{
			Path: "/",
			Routes: []serbewr.RouteConfig{
				{
					Method:  http.MethodPost,
					Path:    pathV1Proxies,
					Handler: api.authenticate(api.handleProxy),
				},
				{
					Method:  http.MethodGet,
					Path:    pathV1Pools,
					Handler: api.authenticate(api.handlePools),
				},
				{
					Method:  http.MethodGet,
					Path:    pathV1Cells,
					Handler: api.authenticate(api.handleCells),
				},
				{
					Method:  http.MethodGet,
					Path:    pathV1CellByID,
					Handler: api.authenticate(api.handleCell),
				},
				{
					Method:  http.MethodDelete,
					Path:    pathV1CellByID,
					Handler: api.authenticate(api.handleDeleteCell),
				},
			},
		}},
	}
}

// startMetrics stands up the prometheus /metrics + /healthz endpoint
// on a separate port so the scrape surface stays internal to the
// tailnet even if the API listener is ever made externally reachable.
func (s *Service) startMetrics(ctx context.Context) {
	logger := ctxscope.GetLogger(ctx)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	s.metricsSrv = &http.Server{
		Addr:              s.cfg.MetricsAddr,
		Handler:           mux,
		ReadHeaderTimeout: httpReadHeaderTimeout,
	}

	go func() {
		err := s.metricsSrv.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("metrics server stopped", "err", err)

			s.httpErr <- err
		}
	}()

	logger.Info("pr0xteus metrics listening", "addr", s.cfg.MetricsAddr)
}
