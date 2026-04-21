package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"llm-proxy/internal/admin"
	"llm-proxy/internal/appstate"
	"llm-proxy/internal/config"
	"llm-proxy/internal/observability"
	"llm-proxy/internal/proxy"
	"llm-proxy/internal/router"
	"llm-proxy/internal/tokencount"
)

// Handlers bundles the two HTTP entry points the proxy exposes. Public serves
// the proxy routes (and /healthz) on the user-facing listener; Admin serves
// /metrics, the management REST API, and the embedded UI on a separate —
// typically loopback — listener.
type Handlers struct {
	Public    http.Handler
	Admin     http.Handler
	Container *appstate.Container
}

// BuildHandlers constructs the proxy and admin handlers plus the shared
// AppState container. The container is populated with the initial AppState
// derived from cfg; admin mutations will swap in new AppStates in place.
//
// configPath may be empty, in which case the admin UI and API are still
// served but mutations that try to persist will fail — useful for tests that
// don't care about on-disk state.
func BuildHandlers(_ context.Context, cfg config.Config, configPath string, logger *slog.Logger) (Handlers, error) {
	if logger == nil {
		logger = slog.Default()
	}

	resolved := cfg.Resolved()

	// tiktoken state is a package-level cache so it's safe to initialise
	// once here; it's keyed by model name, not config state, and Reload
	// does not need to re-run it.
	tokenCountingEnabled := resolved.TokenCounting.Enabled
	if !tokenCountingEnabled {
		for _, p := range resolved.Providers {
			if p.IsTokenCountingEnabled(resolved.TokenCounting) {
				tokenCountingEnabled = true
				break
			}
		}
	}
	if tokenCountingEnabled {
		if err := tokencount.Init(); err != nil {
			logger.Warn("tiktoken init failed, falling back to estimation", "error", err)
		}
	}

	client := proxy.NewHTTPClient(resolved.Transport)
	container := appstate.NewContainer(client)
	if err := container.Install(cfg); err != nil {
		return Handlers{}, err
	}

	metrics := observability.NewMetrics()
	proxyHandler := router.New(container, logger)
	proxyHandler = metrics.Middleware(proxyHandler)
	proxyHandler = observability.LoggingMiddleware(logger, proxyHandler)
	proxyHandler = observability.TokenContextMiddleware(proxyHandler)

	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	publicMux.Handle("/", proxyHandler)

	adminHandler := admin.NewHandler(container, configPath, logger, admin.Options{
		PasswordHash:       cfg.Admin.PasswordHash,
		SessionTTLMin:      cfg.Admin.SessionTTLMin,
		MetricsBearerToken: cfg.Admin.MetricsBearerToken,
		Metrics:            metrics.Handler(),
	})

	return Handlers{Public: publicMux, Admin: adminHandler, Container: container}, nil
}

// Service pairs the public-facing proxy server with the loopback-only admin
// server that hosts /metrics and the management UI.
type Service struct {
	Public *http.Server
	Admin  *http.Server
	logger *slog.Logger
}

func New(ctx context.Context, cfg config.Config, configPath string, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}

	handlers, err := BuildHandlers(ctx, cfg, configPath, logger)
	if err != nil {
		return nil, err
	}

	return &Service{
		Public: &http.Server{
			Addr:              cfg.Server.Listen,
			Handler:           handlers.Public,
			ReadHeaderTimeout: 5 * time.Second,
		},
		Admin: &http.Server{
			Addr:              cfg.Server.MetricsListen,
			Handler:           handlers.Admin,
			ReadHeaderTimeout: 5 * time.Second,
		},
		logger: logger,
	}, nil
}

// ListenAndServe starts both servers. It returns when either one exits; the
// other is shut down so the process can terminate cleanly.
func (s *Service) ListenAndServe() error {
	errs := make(chan error, 2)

	go func() {
		s.logger.Info("admin server listening", "addr", s.Admin.Addr)
		err := s.Admin.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	go func() {
		s.logger.Info("proxy server listening", "addr", s.Public.Addr)
		err := s.Public.ListenAndServe()
		if !errors.Is(err, http.ErrServerClosed) {
			errs <- err
			return
		}
		errs <- nil
	}()

	first := <-errs
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Public.Shutdown(shutdownCtx)
	_ = s.Admin.Shutdown(shutdownCtx)
	<-errs
	return first
}
