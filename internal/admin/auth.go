package admin

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const sessionCookieName = "llm_proxy_admin"

// requireAuth is a middleware that gates the admin mux when a password is
// configured. It lets a small whitelist of paths through (login endpoints and
// their static assets) so an unauthenticated visitor can reach the form.
func (h *Handler) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.authEnabled() {
			next.ServeHTTP(w, r)
			return
		}
		if isPublicAdminPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		// /metrics additionally accepts a static bearer token so scrapers
		// (Prometheus) can pull without a session cookie. The token is
		// optional; when unset the endpoint behaves like any other
		// authenticated path.
		if r.URL.Path == "/metrics" && h.metricsBearerAccepted(r) {
			next.ServeHTTP(w, r)
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil || !h.sessions.Valid(cookie.Value) {
			if isAPIPath(r.URL.Path) {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			http.Redirect(w, r, "/ui/login.html", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (h *Handler) metricsBearerAccepted(r *http.Request) bool {
	configured := h.metricsBearerToken()
	if configured == "" {
		return false
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return false
	}
	got := strings.TrimSpace(header[len(prefix):])
	return subtle.ConstantTimeCompare([]byte(got), []byte(configured)) == 1
}

func (h *Handler) authEnabled() bool {
	return h.passwordHash() != ""
}

// isAPIPath identifies requests that should get a 401 (rather than a login
// redirect) on missing/invalid credentials — API consumers and the UI's
// fetch code both expect JSON-style status codes.
func isAPIPath(p string) bool {
	return strings.HasPrefix(p, "/api/") || p == "/metrics"
}

func isPublicAdminPath(p string) bool {
	switch p {
	case "/api/login", "/api/logout", "/api/auth":
		return true
	case "/ui/login.html", "/ui/login.js", "/ui/styles.css":
		return true
	}
	// The login form itself is a Vue + ElementPlus app, so the vendored
	// framework bundles have to be reachable before login. The content
	// here is off-the-shelf library code (see internal/admin/ui/vendor/),
	// nothing sensitive lives in this tree.
	if strings.HasPrefix(p, "/ui/vendor/") {
		return true
	}
	return false
}

// handleLogin verifies the submitted password and, on success, issues a
// session cookie. On failure we sleep briefly to blunt timing oracles — the
// sleep runs unconditionally, so a caller cannot distinguish "unknown
// password" from "wrong password" by response time.
func (h *Handler) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if !h.authEnabled() {
		// Auth is off — no login needed. Return a noop success so the UI
		// flow stays uniform.
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if !VerifyPassword(h.passwordHash(), body.Password) {
		time.Sleep(150 * time.Millisecond)
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
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
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) handleLogout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if c, err := r.Cookie(sessionCookieName); err == nil {
		h.sessions.Delete(c.Value)
	}
	http.SetCookie(w, &http.Cookie{
		Name:   sessionCookieName,
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleAuthStatus lets the UI decide whether to show the login page before
// making any mutating request. Returned JSON: {"enabled":true,"authenticated":false}.
func (h *Handler) handleAuthStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	enabled := h.authEnabled()
	authed := !enabled
	if enabled {
		if c, err := r.Cookie(sessionCookieName); err == nil && h.sessions.Valid(c.Value) {
			authed = true
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{
		"enabled":       enabled,
		"authenticated": authed,
	})
}
