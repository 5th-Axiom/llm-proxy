package admin

import (
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

func (h *Handler) authEnabled() bool {
	return h.passwordHash != ""
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
	if !VerifyPassword(h.passwordHash, body.Password) {
		time.Sleep(150 * time.Millisecond)
		http.Error(w, "invalid password", http.StatusUnauthorized)
		return
	}
	token := h.sessions.Create()
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(h.sessions.ttl.Seconds()),
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
