// Package admin hosts the management REST API and embedded web UI that the
// proxy exposes on its loopback-only admin listener. It owns the path of the
// on-disk config file, validates incoming mutations, writes them back, and
// swaps the live AppState through the container so changes take effect
// immediately.
package admin

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"llm-proxy/internal/appstate"
	"llm-proxy/internal/config"
)

//go:embed ui
var uiFS embed.FS

// Handler serves /api/* JSON endpoints plus the /ui static assets. When a
// password hash is configured it also gates access with a session cookie.
type Handler struct {
	container  *appstate.Container
	configPath string
	logger     *slog.Logger
	sessions   *sessionStore

	// mu serializes mutations so concurrent admin calls cannot race on the
	// read-modify-write cycle of the raw config.
	mu sync.Mutex
}

// passwordHash returns the currently-configured hash by reading the live
// container state. A goroutine that rotates the admin password via the
// Settings API sees the new value take effect immediately on its next call,
// without needing to rebuild the admin handler.
func (h *Handler) passwordHash() string {
	state := h.container.Load()
	if state == nil {
		return ""
	}
	return state.Raw.Admin.PasswordHash
}

func (h *Handler) metricsBearerToken() string {
	state := h.container.Load()
	if state == nil {
		return ""
	}
	return state.Raw.Admin.MetricsBearerToken
}

// sessionTTL resolves the cookie lifetime to use for freshly-issued
// sessions. Existing sessions keep whatever expiry they were minted with.
func (h *Handler) sessionTTL() time.Duration {
	state := h.container.Load()
	if state == nil {
		return 12 * time.Hour
	}
	if m := state.Raw.Admin.SessionTTLMin; m > 0 {
		return time.Duration(m) * time.Minute
	}
	return 12 * time.Hour
}

// Options configures the admin handler. Secret-holding fields
// (password_hash, metrics_bearer_token) are no longer duplicated here —
// they live in the raw config carried by the container so Settings-API
// edits take effect immediately. Only Metrics (a live http.Handler) has
// to be wired at construction.
type Options struct {
	Metrics http.Handler
}

func NewHandler(container *appstate.Container, configPath string, logger *slog.Logger, opts Options) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	h := &Handler{
		container:  container,
		configPath: configPath,
		logger:     logger,
		sessions:   newSessionStore(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/providers", h.handleProvidersCollection)
	mux.HandleFunc("/api/providers/", h.handleProviderItem)
	mux.HandleFunc("/api/users", h.handleUsersCollection)
	mux.HandleFunc("/api/users/", h.handleUserItem)
	mux.HandleFunc("/api/keys/", h.handleKeyItem)
	mux.HandleFunc("/api/config", h.handleConfigSummary)
	mux.HandleFunc("/api/usage/summary", h.handleUsageSummary)
	mux.HandleFunc("/api/usage/cleanup", h.handleUsageCleanupNow)
	mux.HandleFunc("/api/settings", h.handleSettings)
	mux.HandleFunc("/api/settings/password", h.handleChangePassword)
	mux.HandleFunc("/api/settings/metrics-token", h.handleMetricsToken)
	mux.HandleFunc("/api/login", h.handleLogin)
	mux.HandleFunc("/api/logout", h.handleLogout)
	mux.HandleFunc("/api/auth", h.handleAuthStatus)
	if opts.Metrics != nil {
		mux.Handle("/metrics", opts.Metrics)
	}

	sub, err := fs.Sub(uiFS, "ui")
	if err != nil {
		// Embedding paths are static, so this can only fail if the directory
		// was removed by a misconfigured build.
		panic(fmt.Sprintf("admin ui embed: %v", err))
	}
	mux.Handle("/ui/", http.StripPrefix("/ui/", http.FileServer(http.FS(sub))))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/ui/", http.StatusFound)
			return
		}
		http.NotFound(w, r)
	})

	return h.requireAuth(mux)
}

// providerView is the serialised shape of a provider over the API. Sensitive
// fields are never sent to the browser — the UI shows a placeholder and the
// user replaces the value only when they want to change it.
type providerView struct {
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	BasePath        string            `json:"base_path"`
	UpstreamBaseURL string            `json:"upstream_base_url"`
	APIKeyPreview   string            `json:"api_key_preview"`
	UpstreamHeaders map[string]string `json:"upstream_headers,omitempty"`
	TokenCounting   *bool             `json:"token_counting,omitempty"`
}

// providerInput is the accepted shape on write. UpstreamAPIKey is optional on
// update: an empty string preserves the stored value.
type providerInput struct {
	Name            string            `json:"name"`
	Type            string            `json:"type"`
	BasePath        string            `json:"base_path"`
	UpstreamBaseURL string            `json:"upstream_base_url"`
	UpstreamAPIKey  string            `json:"upstream_api_key"`
	UpstreamHeaders map[string]string `json:"upstream_headers"`
	TokenCounting   *bool             `json:"token_counting"`
}

type summaryView struct {
	Listen        string `json:"listen"`
	MetricsListen string `json:"metrics_listen"`
	TokenCounting bool   `json:"token_counting_enabled"`
	ProviderCount int    `json:"provider_count"`
	UserCount     int    `json:"user_count"`
	ActiveKeys    int    `json:"active_keys"`
}

func (h *Handler) handleConfigSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}

	users, _ := h.container.Store().ListUsers(r.Context())
	activeKeys := 0
	for _, u := range users {
		activeKeys += u.KeyCount
	}

	writeJSON(w, http.StatusOK, summaryView{
		Listen:        state.Raw.Server.Listen,
		MetricsListen: state.Raw.Server.MetricsListen,
		TokenCounting: state.Raw.TokenCounting.Enabled,
		ProviderCount: len(state.Raw.Providers),
		UserCount:     len(users),
		ActiveKeys:    activeKeys,
	})
}

func (h *Handler) handleProvidersCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listProviders(w, r)
	case http.MethodPost:
		h.createProvider(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleProviderItem(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/api/providers/")
	if name == "" || strings.Contains(name, "/") {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.getProvider(w, r, name)
	case http.MethodPut:
		h.updateProvider(w, r, name)
	case http.MethodDelete:
		h.deleteProvider(w, r, name)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) listProviders(w http.ResponseWriter, _ *http.Request) {
	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	out := make([]providerView, 0, len(state.Raw.Providers))
	for _, p := range state.Raw.Providers {
		out = append(out, toView(p))
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getProvider(w http.ResponseWriter, _ *http.Request, name string) {
	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	for _, p := range state.Raw.Providers {
		if p.Name == name {
			writeJSON(w, http.StatusOK, toView(p))
			return
		}
	}
	http.NotFound(w, nil)
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var input providerInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(input.UpstreamAPIKey) == "" {
		http.Error(w, "upstream_api_key is required on create", http.StatusBadRequest)
		return
	}

	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	raw := cloneConfig(state.Raw)

	for _, p := range raw.Providers {
		if p.Name == input.Name {
			http.Error(w, "provider already exists", http.StatusConflict)
			return
		}
	}

	next := fromInput(input)
	raw.Providers = append(raw.Providers, next)

	if err := h.applyAndPersist(raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.logger.Info("admin: provider created", "name", input.Name)
	writeJSON(w, http.StatusCreated, toView(next))
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var input providerInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	raw := cloneConfig(state.Raw)

	idx := -1
	for i, p := range raw.Providers {
		if p.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.NotFound(w, nil)
		return
	}

	// Forbid renaming via PUT — simplifies caching and allows URL-as-ID.
	if input.Name != "" && input.Name != name {
		http.Error(w, "renaming providers is not supported; delete and recreate", http.StatusBadRequest)
		return
	}

	existing := raw.Providers[idx]
	merged := fromInput(input)
	merged.Name = name
	if strings.TrimSpace(input.UpstreamAPIKey) == "" {
		// Preserve the previously-stored key (with its ${ENV} placeholder
		// intact) when the client sends an empty string.
		merged.UpstreamAPIKey = existing.UpstreamAPIKey
	}
	raw.Providers[idx] = merged

	if err := h.applyAndPersist(raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.logger.Info("admin: provider updated", "name", name)
	writeJSON(w, http.StatusOK, toView(merged))
}

func (h *Handler) deleteProvider(w http.ResponseWriter, _ *http.Request, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()

	state := h.container.Load()
	if state == nil {
		http.Error(w, "state not ready", http.StatusServiceUnavailable)
		return
	}
	raw := cloneConfig(state.Raw)

	idx := -1
	for i, p := range raw.Providers {
		if p.Name == name {
			idx = i
			break
		}
	}
	if idx == -1 {
		http.NotFound(w, nil)
		return
	}
	raw.Providers = append(raw.Providers[:idx], raw.Providers[idx+1:]...)

	if err := h.applyAndPersist(raw); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.logger.Info("admin: provider deleted", "name", name)
	w.WriteHeader(http.StatusNoContent)
}

// applyAndPersist validates the candidate raw config, installs it into the
// container, and only then writes it to disk. Installing first ensures we
// never persist a config we couldn't actually run; if the caller was racing
// against another mutation the container mutex catches it too.
//
// If persistence fails, the in-memory state is rolled back to the snapshot
// captured *before* Install so the live process isn't silently divergent
// from what's on disk. A previous version of this function read prev from
// the container *after* Install — which returned the just-installed new
// state, turning the rollback into a no-op.
func (h *Handler) applyAndPersist(raw config.Config) error {
	if err := raw.Validate(); err != nil {
		return err
	}

	prevState := h.container.Load()
	if prevState == nil {
		// Admin API shouldn't be reachable before the first Install during
		// startup, but defend anyway: without a prev we can't roll back,
		// so refuse to swap the live state until the operator retries.
		return errors.New("initial state not ready")
	}
	prevRaw := prevState.Raw

	if err := h.container.Install(raw); err != nil {
		return err
	}
	if err := config.Save(h.configPath, raw); err != nil {
		// Best-effort rollback: restore the pre-Install snapshot. If the
		// restore itself fails (shouldn't — prevRaw validated on its way
		// in) we surface the original persist error rather than the
		// restore error so the operator sees the root cause.
		_ = h.container.Install(prevRaw)
		return fmt.Errorf("persist config: %w", err)
	}
	return nil
}

func fromInput(in providerInput) config.ProviderConfig {
	p := config.ProviderConfig{
		Name:            strings.TrimSpace(in.Name),
		Type:            config.ProviderType(strings.TrimSpace(in.Type)),
		BasePath:        config.NormalizeBasePath(in.BasePath),
		UpstreamBaseURL: strings.TrimRight(strings.TrimSpace(in.UpstreamBaseURL), "/"),
		UpstreamAPIKey:  in.UpstreamAPIKey,
		UpstreamHeaders: in.UpstreamHeaders,
		TokenCounting:   in.TokenCounting,
	}
	return p
}

func toView(p config.ProviderConfig) providerView {
	return providerView{
		Name:            p.Name,
		Type:            string(p.Type),
		BasePath:        p.BasePath,
		UpstreamBaseURL: p.UpstreamBaseURL,
		APIKeyPreview:   preview(p.UpstreamAPIKey),
		UpstreamHeaders: p.UpstreamHeaders,
		TokenCounting:   p.TokenCounting,
	}
}

// preview masks secrets for UI display. ${ENV_VAR} references are shown
// verbatim because they are not secrets themselves; literal keys show only a
// short tail so the user can still eyeball which one is stored.
func preview(v string) string {
	if v == "" {
		return ""
	}
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		return v
	}
	if len(v) <= 6 {
		return "••••"
	}
	return "••••" + v[len(v)-4:]
}

func cloneConfig(in config.Config) config.Config {
	out := in
	out.Server.Tokens = append([]string(nil), in.Server.Tokens...)
	out.Providers = append([]config.ProviderConfig(nil), in.Providers...)
	for i, p := range out.Providers {
		if p.UpstreamHeaders != nil {
			headers := make(map[string]string, len(p.UpstreamHeaders))
			for k, v := range p.UpstreamHeaders {
				headers[k] = v
			}
			out.Providers[i].UpstreamHeaders = headers
		}
		if p.TokenCounting != nil {
			v := *p.TokenCounting
			out.Providers[i].TokenCounting = &v
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Compile-time check that the config package still exports what we rely on.
var _ = errors.New
