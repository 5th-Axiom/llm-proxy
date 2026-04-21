package admin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"llm-proxy/internal/config"
	"llm-proxy/internal/server"
)

// TestAdminCRUDHotReload exercises the full admin flow: list, create, edit,
// use, delete. After each mutation we hit the public proxy and verify the
// new config is effective — no restart, no reload endpoint.
func TestAdminCRUDHotReload(t *testing.T) {
	upstreamA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"A","path":"` + r.URL.Path + `"}`))
	}))
	defer upstreamA.Close()
	upstreamB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"upstream":"B","path":"` + r.URL.Path + `"}`))
	}))
	defer upstreamB.Close()

	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initial := `
server:
  listen: ":0"
  metrics_listen: "127.0.0.1:0"
  tokens:
    - "proxy-token"
providers:
  - name: "alpha"
    type: "openai"
    base_path: "/alpha"
    upstream_base_url: "` + upstreamA.URL + `"
    upstream_api_key: "upstream-a"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	handlers, err := server.BuildHandlers(context.Background(), cfg, configPath,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("BuildHandlers: %v", err)
	}

	proxyTS := httptest.NewServer(handlers.Public)
	defer proxyTS.Close()
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	// 1. Initial provider works.
	if body := curl(t, proxyTS.URL+"/alpha/v1/anything", "proxy-token"); !strings.Contains(body, `"upstream":"A"`) {
		t.Fatalf("alpha before create: want upstream A, got %q", body)
	}

	// 2. Unknown provider 404s.
	if code := curlStatus(t, proxyTS.URL+"/beta/v1/anything", "proxy-token"); code != http.StatusNotFound {
		t.Fatalf("beta pre-create status = %d, want 404", code)
	}

	// 3. Create a second provider pointing at upstream B.
	createBody := `{"name":"beta","type":"openai","base_path":"/beta",
	               "upstream_base_url":"` + upstreamB.URL + `",
	               "upstream_api_key":"upstream-b"}`
	postJSON(t, adminTS.URL+"/api/providers", createBody, http.StatusCreated)

	// 4. Hot-reload: the proxy now routes /beta without restarting.
	if body := curl(t, proxyTS.URL+"/beta/v1/anything", "proxy-token"); !strings.Contains(body, `"upstream":"B"`) {
		t.Fatalf("beta after create: want upstream B, got %q", body)
	}

	// 5. Updating base_path retires the old one immediately.
	updateBody := `{"type":"openai","base_path":"/bravo",
	                "upstream_base_url":"` + upstreamB.URL + `",
	                "upstream_api_key":""}`
	putJSON(t, adminTS.URL+"/api/providers/beta", updateBody, http.StatusOK)

	if code := curlStatus(t, proxyTS.URL+"/beta/v1/anything", "proxy-token"); code != http.StatusNotFound {
		t.Fatalf("old /beta after rename status = %d, want 404", code)
	}
	if body := curl(t, proxyTS.URL+"/bravo/v1/anything", "proxy-token"); !strings.Contains(body, `"upstream":"B"`) {
		t.Fatalf("renamed /bravo: want upstream B, got %q", body)
	}

	// 6. API key preview must not leak the stored key.
	listResp := getJSON(t, adminTS.URL+"/api/providers")
	if strings.Contains(listResp, "upstream-a") || strings.Contains(listResp, "upstream-b") {
		t.Fatalf("provider list leaked raw API key:\n%s", listResp)
	}

	// 7. Deleting alpha removes it.
	deleteReq(t, adminTS.URL+"/api/providers/alpha")
	if code := curlStatus(t, proxyTS.URL+"/alpha/v1/anything", "proxy-token"); code != http.StatusNotFound {
		t.Fatalf("alpha after delete status = %d, want 404", code)
	}

	// 8. On-disk config reflects final state.
	disk, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	ds := string(disk)
	if strings.Contains(ds, "alpha") {
		t.Errorf("disk config still mentions alpha:\n%s", ds)
	}
	if !strings.Contains(ds, "bravo") {
		t.Errorf("disk config missing bravo:\n%s", ds)
	}
}

func TestAdminRejectsInvalidProvider(t *testing.T) {
	handlers, _ := bootstrapEmpty(t)
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	cases := []struct {
		name string
		body string
	}{
		{"missing name", `{"type":"openai","base_path":"/x","upstream_base_url":"http://x","upstream_api_key":"k"}`},
		{"bad type", `{"name":"n","type":"llama","base_path":"/x","upstream_base_url":"http://x","upstream_api_key":"k"}`},
		{"root base_path", `{"name":"n","type":"openai","base_path":"/","upstream_base_url":"http://x","upstream_api_key":"k"}`},
		{"missing key", `{"name":"n","type":"openai","base_path":"/x","upstream_base_url":"http://x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodPost, adminTS.URL+"/api/providers", strings.NewReader(c.body))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode < 400 || resp.StatusCode >= 500 {
				t.Fatalf("status = %d, want 4xx for invalid input", resp.StatusCode)
			}
		})
	}
}

// TestUIServedAtRoot checks that the static assets are embedded correctly and
// the root redirect lands on index.html.
func TestUIServedAtRoot(t *testing.T) {
	handlers, _ := bootstrapEmpty(t)
	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	c := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(adminTS.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("root status = %d, want 302", resp.StatusCode)
	}

	resp, err = http.Get(adminTS.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if !bytes.Contains(body, []byte("<h1>llm-proxy</h1>")) {
		t.Fatalf("ui body does not contain page heading:\n%s", body)
	}
}

// ---- helpers ----

func bootstrapEmpty(t *testing.T) (server.Handlers, string) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	initial := `
server:
  listen: ":0"
  metrics_listen: "127.0.0.1:0"
  tokens: ["proxy-token"]
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
	handlers, err := server.BuildHandlers(context.Background(), cfg, configPath,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("BuildHandlers: %v", err)
	}
	return handlers, configPath
}

func curl(t *testing.T, url, token string) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"model":"m"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}
func curlStatus(t *testing.T, url, token string) int {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func postJSON(t *testing.T, url, body string, wantStatus int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("POST %s status = %d, want %d; body=%s", url, resp.StatusCode, wantStatus, b)
	}
}
func putJSON(t *testing.T, url, body string, wantStatus int) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPut, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("PUT %s status = %d, want %d; body=%s", url, resp.StatusCode, wantStatus, b)
	}
}
func getJSON(t *testing.T, url string) string {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}
func deleteReq(t *testing.T, url string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodDelete, url, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE %s status = %d, want 204", url, resp.StatusCode)
	}
}

// silence unused import for test
var _ = json.Marshal
