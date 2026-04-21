// Package auth turns an inbound HTTP request's Bearer / x-api-key token into
// a resolved user + key identity backed by the store. The old per-process
// token map is gone — multi-tenant deployments need per-user attribution and
// revocation, which only a persistent store can give us.
package auth

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"llm-proxy/internal/store"
)

// ErrUnauthorized is the only error shape callers see; every failure (missing
// token, unknown token, revoked key, disabled user, DB error) collapses into
// this so nothing leaks about why the request was rejected.
var ErrUnauthorized = errors.New("unauthorized")

// Result carries the bits downstream handlers need to attribute usage to a
// specific user/key without a second DB round-trip.
type Result struct {
	UserID int64
	KeyID  int64
}

// lastUsedThrottle is how often we'll flush a key's last_used_at back to the
// DB per key. Without this the authenticator would write on every request —
// wasteful for a metric that only needs minute-level granularity.
const lastUsedThrottle = 60 * time.Second

// Authenticator performs per-request authorisation against the store. Safe
// for concurrent use; internal state (the last-used cache) is mutex-guarded.
type Authenticator struct {
	store  *store.Store
	logger *slog.Logger

	mu            sync.Mutex
	lastTouchedAt map[int64]time.Time
}

func New(s *store.Store, logger *slog.Logger) *Authenticator {
	if logger == nil {
		logger = slog.Default()
	}
	return &Authenticator{
		store:         s,
		logger:        logger,
		lastTouchedAt: map[int64]time.Time{},
	}
}

// Authorize extracts a Bearer / x-api-key header, looks it up in the store,
// and returns the resolved identity. On success it schedules a throttled
// last-used update so hot callers don't hammer the DB with UPDATE statements.
func (a *Authenticator) Authorize(r *http.Request) (Result, error) {
	token := extractToken(r)
	if token == "" {
		return Result{}, ErrUnauthorized
	}

	lookup, err := a.store.LookupKeyByToken(r.Context(), token)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			a.logger.Warn("auth lookup failed", "err", err)
		}
		return Result{}, ErrUnauthorized
	}
	if lookup.KeyRevoked || lookup.UserDisabled {
		return Result{}, ErrUnauthorized
	}

	a.maybeTouchLastUsed(lookup.KeyID)

	return Result{UserID: lookup.UserID, KeyID: lookup.KeyID}, nil
}

func (a *Authenticator) maybeTouchLastUsed(keyID int64) {
	a.mu.Lock()
	if t, ok := a.lastTouchedAt[keyID]; ok && time.Since(t) < lastUsedThrottle {
		a.mu.Unlock()
		return
	}
	a.lastTouchedAt[keyID] = time.Now()
	a.mu.Unlock()

	// Detach the DB write from the request context: the request may be
	// streaming for minutes and a timeout there would cancel the update.
	go func(id int64) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := a.store.TouchKeyLastUsed(ctx, id); err != nil {
			a.logger.Warn("touch last_used_at failed", "key_id", id, "err", err)
		}
	}(keyID)
}

func extractToken(r *http.Request) string {
	if header := strings.TrimSpace(r.Header.Get("Authorization")); header != "" {
		if strings.HasPrefix(strings.ToLower(header), "bearer ") {
			return strings.TrimSpace(header[7:])
		}
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}
