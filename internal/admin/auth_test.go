package admin_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-proxy/internal/admin"
	"llm-proxy/internal/config"
	"llm-proxy/internal/server"
)

func TestPasswordHashVerifyRoundTrip(t *testing.T) {
	hash, err := admin.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if !admin.VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("correct password failed to verify")
	}
	if admin.VerifyPassword(hash, "wrong password") {
		t.Fatal("wrong password was accepted")
	}
}

func TestAdminAuthDisabledAllowsAll(t *testing.T) {
	handlers := bootstrapWithHash(t, "")
	ts := httptest.NewServer(handlers.Admin)
	defer ts.Close()

	r, err := http.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("/api/providers unauthenticated = %d, want 200 (auth disabled)", r.StatusCode)
	}
}

func TestAdminAuthEnabledBlocksAPIAndRedirectsUI(t *testing.T) {
	hash, err := admin.HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	handlers := bootstrapWithHash(t, hash)
	ts := httptest.NewServer(handlers.Admin)
	defer ts.Close()

	// API call without cookie → 401
	r, err := http.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/providers without cookie = %d, want 401", r.StatusCode)
	}

	// UI fetch without cookie → 302 to /ui/login.html
	noRedirect := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	r, err = noRedirect.Get(ts.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusFound || !strings.Contains(r.Header.Get("Location"), "login") {
		t.Fatalf("/ui/ without cookie = %d %q, want 302 to login", r.StatusCode, r.Header.Get("Location"))
	}

	// Login with wrong password → 401
	r, err = http.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"password":"nope"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-password login = %d, want 401", r.StatusCode)
	}

	// Login with correct password → 204 + Set-Cookie
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	r, err = client.Post(ts.URL+"/api/login", "application/json", strings.NewReader(`{"password":"secret"}`))
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("login = %d, want 204", r.StatusCode)
	}

	// API call with cookie → 200
	r, err = client.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("/api/providers authenticated = %d, want 200", r.StatusCode)
	}

	// /metrics is also gated: unauthenticated fails, authenticated works.
	r2, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/metrics unauthenticated = %d, want 401", r2.StatusCode)
	}
	r2, err = client.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	r2.Body.Close()
	if r2.StatusCode != http.StatusOK {
		t.Fatalf("/metrics authenticated = %d, want 200", r2.StatusCode)
	}

	// Logout clears the session.
	r, err = client.Post(ts.URL+"/api/logout", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d, want 204", r.StatusCode)
	}
	r, err = client.Get(ts.URL + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/providers after logout = %d, want 401", r.StatusCode)
	}
}

func TestMetricsBearerTokenBypassesSessionAuth(t *testing.T) {
	hash, err := admin.HashPassword("secret")
	if err != nil {
		t.Fatal(err)
	}
	handlers := bootstrapWithAdmin(t, hash, "scrape-token-xyz")
	ts := httptest.NewServer(handlers.Admin)
	defer ts.Close()

	// No credentials → 401 (auth enabled).
	r, err := http.Get(ts.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no-auth /metrics = %d, want 401", r.StatusCode)
	}

	// Wrong bearer → 401.
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer wrong-token")
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-bearer /metrics = %d, want 401", r.StatusCode)
	}

	// Correct bearer → 200, scraper gets metrics without session cookie.
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/metrics?format=prometheus", nil)
	req.Header.Set("Authorization", "Bearer scrape-token-xyz")
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != http.StatusOK {
		t.Fatalf("scrape /metrics = %d, want 200", r.StatusCode)
	}
	if !strings.Contains(string(body), "llm_proxy_requests_total") {
		t.Fatalf("scrape body missing expected metric:\n%s", body)
	}

	// Bearer must NOT unlock the rest of the admin surface.
	req, _ = http.NewRequest(http.MethodGet, ts.URL+"/api/providers", nil)
	req.Header.Set("Authorization", "Bearer scrape-token-xyz")
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("/api/providers with metrics bearer = %d, want 401", r.StatusCode)
	}
}

func bootstrapWithHash(t *testing.T, hash string) server.Handlers {
	return bootstrapWithAdmin(t, hash, "")
}

func bootstrapWithAdmin(t *testing.T, hash, metricsBearer string) server.Handlers {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initial := `
server:
  listen: ":0"
  metrics_listen: "127.0.0.1:0"
providers:
  - name: "placeholder"
    type: "openai"
    base_path: "/placeholder"
    upstream_base_url: "http://127.0.0.1:1"
    upstream_api_key: "k"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg.Storage.SQLitePath = filepath.Join(dir, "test.db")
	cfg.Admin.PasswordHash = hash
	cfg.Admin.MetricsBearerToken = metricsBearer
	handlers, err := server.BuildHandlers(context.Background(), cfg, configPath,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("BuildHandlers: %v", err)
	}
	return handlers
}
