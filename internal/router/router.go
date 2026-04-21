package router

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"llm-proxy/internal/appstate"
	"llm-proxy/internal/tokencount"
)

// Handler is the proxy-facing request router. It pulls the current AppState
// from the container per request so admin-triggered reloads apply to the
// next call without restarting.
type Handler struct {
	container *appstate.Container
	logger    *slog.Logger
}

func New(container *appstate.Container, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{container: container, logger: logger}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	state := h.container.Load()
	if state == nil {
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}

	provider, upstreamPath, ok := state.Registry.Match(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}

	authResult, err := state.Authenticator.Authorize(r)
	if err != nil {
		http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
		return
	}

	// Stash the resolved identity on the shared per-request TokenContext so
	// metrics / logging / future usage-recording all see the same attribution
	// without a second auth lookup.
	if tc := tokencount.FromContext(r.Context()); tc != nil {
		tc.UserID = authResult.UserID
		tc.KeyID = authResult.KeyID
	}

	if err := state.Forwarder.Forward(w, r, provider, upstreamPath); err != nil {
		status := http.StatusBadGateway
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			status = 499
		}
		h.logger.Error("proxy request failed", "provider", provider.Name, "err", err)
		http.Error(w, http.StatusText(status), status)
	}
}
