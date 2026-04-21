// Package appstate holds the runtime-swappable view of the proxy's
// configuration. The handlers read the current state through Container.Load()
// on every request so a config mutation through the admin API takes effect on
// the next call without restarting the process or dropping in-flight
// connections.
package appstate

import (
	"log/slog"
	"net/http"
	"sync/atomic"

	"llm-proxy/internal/auth"
	"llm-proxy/internal/config"
	"llm-proxy/internal/providers"
	"llm-proxy/internal/proxy"
	"llm-proxy/internal/store"
)

// AppState is the immutable snapshot of everything derived from a Config plus
// the (mutable, long-lived) store. A new AppState is built for every Reload
// and installed via atomic pointer swap; existing goroutines that already
// captured a pointer keep using the old snapshot until they finish.
type AppState struct {
	Registry      *providers.Registry
	Authenticator *auth.Authenticator
	Forwarder     *proxy.Forwarder
	TokenCounting config.TokenCountingConfig
	// Raw is the config as stored on disk (with ${ENV} placeholders
	// intact). The admin API reads and rewrites this form.
	Raw config.Config
	// Store is shared across reloads; a config mutation never re-opens
	// the DB, so connection pools, WAL, and prepared statements survive.
	Store *store.Store
}

// Container holds the current AppState behind an atomic pointer.
type Container struct {
	ptr    atomic.Pointer[AppState]
	client *http.Client
	store  *store.Store
	logger *slog.Logger
}

// NewContainer constructs a Container that will build AppStates using the
// supplied http.Client for upstream calls and store for identity/usage. Both
// are shared across reloads so TLS sessions, idle connections, and DB
// handles survive config changes.
func NewContainer(client *http.Client, s *store.Store, logger *slog.Logger) *Container {
	if logger == nil {
		logger = slog.Default()
	}
	return &Container{client: client, store: s, logger: logger}
}

// Load returns the currently-installed AppState. Safe for concurrent use.
func (c *Container) Load() *AppState {
	return c.ptr.Load()
}

// Store returns the persistent handle so admin handlers can reach users/keys
// tables without routing everything through AppState.
func (c *Container) Store() *store.Store { return c.store }

// Install builds a fresh AppState from raw and atomically swaps it in. The
// caller is responsible for having validated raw first; Install still runs
// Validate defensively so bad states can't reach the swap.
func (c *Container) Install(raw config.Config) error {
	if err := raw.Validate(); err != nil {
		return err
	}
	resolved := raw.Resolved()

	registry, err := providers.NewRegistry(resolved.Providers)
	if err != nil {
		return err
	}

	state := &AppState{
		Registry:      registry,
		Authenticator: auth.New(c.store, c.logger),
		Forwarder:     proxy.NewForwarder(c.client, resolved.TokenCounting),
		TokenCounting: resolved.TokenCounting,
		Raw:           raw,
		Store:         c.store,
	}
	c.ptr.Store(state)
	return nil
}
