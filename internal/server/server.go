package server

import (
	"context"
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

func NewHandler(_ context.Context, cfg config.Config, logger *slog.Logger) (http.Handler, error) {
	if logger == nil {
		logger = slog.Default()
	}

	registry, err := providers.NewRegistry(cfg.Providers)
	if err != nil {
		return nil, err
	}

	authenticator := auth.New(cfg.Server.Tokens)
	client := proxy.NewHTTPClient(cfg.Transport)

	tokenCountingEnabled := cfg.TokenCounting.Enabled
	if !tokenCountingEnabled {
		for _, p := range cfg.Providers {
			if p.TokenCounting {
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

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/metrics", metrics.Handler())
	mux.Handle("/", proxyHandler)

	return mux, nil
}

func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*http.Server, error) {
	handler, err := NewHandler(ctx, cfg, logger)
	if err != nil {
		return nil, err
	}

	return &http.Server{
		Addr:              cfg.Server.Listen,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
	}, nil
}
