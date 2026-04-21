package admin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"llm-proxy/internal/config"
)

// settingsView is the read shape for the Settings page. Secret-holding fields
// are not echoed back — the UI renders them as "configured / not configured"
// and offers regenerate/clear flows instead.
type settingsView struct {
	TokenCountingEnabled bool `json:"token_counting_enabled"`
	UsageRetentionDays   int  `json:"usage_retention_days"`
	SessionTTLMin        int  `json:"session_ttl_min"`
	PasswordConfigured   bool `json:"password_configured"`
	MetricsTokenSet      bool `json:"metrics_bearer_token_set"`
}

// settingsInput is the PATCH shape. Pointers distinguish "not provided"
// from "set to zero/false": only keys the client explicitly sent are
// applied, the rest are left alone.
type settingsInput struct {
	TokenCountingEnabled *bool `json:"token_counting_enabled,omitempty"`
	UsageRetentionDays   *int  `json:"usage_retention_days,omitempty"`
	SessionTTLMin        *int  `json:"session_ttl_min,omitempty"`
}

func (h *Handler) handleSettings(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.getSettings(w, r)
	case http.MethodPatch:
		h.patchSettings(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) getSettings(w http.ResponseWriter, _ *http.Request) {
	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, settingsView{
		TokenCountingEnabled: state.Raw.TokenCounting.Enabled,
		UsageRetentionDays:   state.Raw.Storage.UsageRetentionDays,
		SessionTTLMin:        state.Raw.Admin.SessionTTLMin,
		PasswordConfigured:   state.Raw.Admin.PasswordHash != "",
		MetricsTokenSet:      state.Raw.Admin.MetricsBearerToken != "",
	})
}

func (h *Handler) patchSettings(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var in settingsInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	raw := cloneConfig(state.Raw)

	if in.TokenCountingEnabled != nil {
		raw.TokenCounting.Enabled = *in.TokenCountingEnabled
	}
	if in.UsageRetentionDays != nil {
		if *in.UsageRetentionDays < 0 {
			http.Error(w, "usage_retention_days must be >= 0", http.StatusBadRequest)
			return
		}
		raw.Storage.UsageRetentionDays = *in.UsageRetentionDays
	}
	if in.SessionTTLMin != nil {
		if *in.SessionTTLMin < 0 {
			http.Error(w, "session_ttl_min must be >= 0", http.StatusBadRequest)
			return
		}
		raw.Admin.SessionTTLMin = *in.SessionTTLMin
	}

	if err := h.applyAndPersist(raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.logger.Info("admin: settings updated")
	h.getSettings(w, r)
}

// passwordChangeInput requires the caller to re-present the current password.
// A hijacked session alone shouldn't be enough to lock the legitimate
// operator out — whoever can't supply the current password can't rotate it.
type passwordChangeInput struct {
	CurrentPassword string `json:"current_password"`
	NewPassword     string `json:"new_password"`
}

func (h *Handler) handleChangePassword(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in passwordChangeInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if in.NewPassword == "" {
		http.Error(w, "new_password is required", http.StatusBadRequest)
		return
	}
	if len(in.NewPassword) < 8 {
		http.Error(w, "new_password must be at least 8 characters", http.StatusBadRequest)
		return
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}

	// If a password is already set, require the current one. When no password
	// is configured yet, the admin UI is wide open on loopback anyway — let
	// the first operator bootstrap without a second secret.
	currentHash := state.Raw.Admin.PasswordHash
	if currentHash != "" {
		if !VerifyPassword(currentHash, in.CurrentPassword) {
			// Sleep to blunt timing oracle; uniform path with handleLogin.
			time.Sleep(150 * time.Millisecond)
			http.Error(w, "current password does not match", http.StatusUnauthorized)
			return
		}
	}

	newHash, err := HashPassword(in.NewPassword)
	if err != nil {
		http.Error(w, "hash password: "+err.Error(), http.StatusInternalServerError)
		return
	}

	raw := cloneConfig(state.Raw)
	raw.Admin.PasswordHash = newHash
	if err := h.applyAndPersist(raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Rotate sessions so previously-issued cookies (from before the password
	// change) stop working immediately — then re-issue a cookie for the
	// current caller so they're not logged out of their own flow.
	h.sessions.DeleteAll()
	ttl := h.sessionTTL()
	token := h.sessions.Create(ttl)
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(ttl.Seconds()),
	})
	h.logger.Info("admin: password rotated")
	w.WriteHeader(http.StatusNoContent)
}

// handleMetricsToken manages the scraper bearer token. POST regenerates (and
// returns the plaintext once); DELETE clears it so /metrics falls back to
// session-only auth.
func (h *Handler) handleMetricsToken(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.regenerateMetricsToken(w, r)
	case http.MethodDelete:
		h.clearMetricsToken(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) regenerateMetricsToken(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}

	token, err := newRandomHex(32)
	if err != nil {
		http.Error(w, "generate token: "+err.Error(), http.StatusInternalServerError)
		return
	}

	raw := cloneConfig(state.Raw)
	raw.Admin.MetricsBearerToken = token
	if err := h.applyAndPersist(raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logger.Info("admin: metrics bearer token rotated")
	writeJSON(w, http.StatusCreated, map[string]string{"token": token})
}

func (h *Handler) clearMetricsToken(w http.ResponseWriter, _ *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	raw := cloneConfig(state.Raw)
	raw.Admin.MetricsBearerToken = ""
	if err := h.applyAndPersist(raw); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h.logger.Info("admin: metrics bearer token cleared")
	w.WriteHeader(http.StatusNoContent)
}

// handleUsageCleanupNow runs the retention sweep on demand rather than
// waiting for the next daily tick. Useful right after an operator shortens
// the retention window and wants the delete to happen now.
func (h *Handler) handleUsageCleanupNow(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	days := state.Raw.Storage.UsageRetentionDays
	if days <= 0 {
		writeJSON(w, http.StatusOK, map[string]any{"deleted": 0, "skipped": "retention disabled"})
		return
	}
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	deleted, err := state.Store.PurgeUsageOlderThan(ctx, cutoff)
	if err != nil {
		http.Error(w, "purge: "+err.Error(), http.StatusInternalServerError)
		return
	}
	h.logger.Info("admin: on-demand usage cleanup", "deleted", deleted, "cutoff", cutoff)
	writeJSON(w, http.StatusOK, map[string]any{"deleted": deleted, "cutoff": cutoff})
}

func newRandomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// silence unused imports if every path ever stops needing them directly
var (
	_ = errors.New
	_ = config.Config{}
)
