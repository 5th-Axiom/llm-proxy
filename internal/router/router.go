package router

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"llm-proxy/internal/auth"
	"llm-proxy/internal/providers"
	"llm-proxy/internal/proxy"
)

type Handler struct {
	registry      *providers.Registry
	authenticator *auth.Authenticator
	forwarder     *proxy.Forwarder
	logger        *slog.Logger
}

func New(registry *providers.Registry, authenticator *auth.Authenticator, forwarder *proxy.Forwarder, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		registry:      registry,
		authenticator: authenticator,
		forwarder:     forwarder,
		logger:        logger,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	provider, upstreamPath, ok := h.registry.Match(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	if err := h.authenticator.Authorize(r, provider.Name); err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	if err := h.forwarder.Forward(w, r, provider, upstreamPath); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = 499
		}
		h.logger.Error("proxy request failed", "provider", provider.Name, "err", err)
		http.Error(w, http.StatusText(status), status)
	}
}
