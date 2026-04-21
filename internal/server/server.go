package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"llm-proxy/internal/auth"
	"llm-proxy/internal/config"
	"llm-proxy/internal/observability"
	"llm-proxy/internal/providers"
	"llm-proxy/internal/proxy"
	"llm-proxy/internal/router"
	"llm-proxy/internal/tokencount"
)

// Handlers bundles the two HTTP entry points the proxy exposes. Public serves
// the proxy routes (and /healthz) on the user-facing listener; Admin serves
// /metrics on a separate — typically loopback — listener to avoid exposing
// provider names and token usage to the public network.
type Handlers struct {
	Public http.Handler
	Admin  http.Handler
}

func BuildHandlers(_ context.Context, cfg config.Config, logger *slog.Logger) (Handlers, error) {
	if logger == nil {
		logger = slog.Default()
	}

	registry, err := providers.NewRegistry(cfg.Providers)
	if err != nil {
		return Handlers{}, err
	}

	authenticator := auth.New(cfg.Server.Tokens)
	client := proxy.NewHTTPClient(cfg.Transport)

	tokenCountingEnabled := cfg.TokenCounting.Enabled
	if !tokenCountingEnabled {
		for _, p := range cfg.Providers {
			if p.IsTokenCountingEnabled(cfg.TokenCounting) {
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

	forwarder := proxy.NewForwarder(client, cfg.TokenCounting)
	metrics := observability.NewMetrics()
	proxyHandler := router.New(registry, authenticator, forwarder, logger)
	proxyHandler = metrics.Middleware(proxyHandler)
	proxyHandler = observability.LoggingMiddleware(logger, proxyHandler)
	proxyHandler = observability.TokenContextMiddleware(proxyHandler)

	publicMux := http.NewServeMux()
	publicMux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	publicMux.Handle("/", proxyHandler)

	adminMux := http.NewServeMux()
	adminMux.Handle("/metrics", metrics.Handler())

	return Handlers{Public: publicMux, Admin: adminMux}, nil
}

// Service pairs the public-facing proxy server with the loopback-only admin
// server that hosts /metrics.
type Service struct {
	Public *http.Server
	Admin  *http.Server
	logger *slog.Logger
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Service, error) {
	if logger == nil {
		logger = slog.Default()
	}

	handlers, err := BuildHandlers(ctx, cfg, logger)
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
	// Trigger shutdown on the peer so the process exits instead of the
	// healthy server soldiering on alone.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.Public.Shutdown(shutdownCtx)
	_ = s.Admin.Shutdown(shutdownCtx)
	<-errs
	return first
}
