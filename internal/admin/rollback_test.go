package admin_test

import (
	"context"
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

// TestApplyAndPersistRollsBackOnSaveFailure guards against a regression in
// applyAndPersist: the previous implementation read `prev` from the
// container *after* Install, so a Save failure re-installed the
// just-installed new state and the live process silently diverged from
// disk. This test points the admin handler at a configPath whose parent
// doesn't exist (Save therefore fails) and asserts the in-memory state
// is still the pre-mutation one.
func TestApplyAndPersistRollsBackOnSaveFailure(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")

	// Write a valid initial config, load, then point admin at a path
	// whose directory doesn't exist so config.Save's atomic
	// tmp-file-rename fails deterministically.
	initial := `
server:
  listen: ":0"
  metrics_listen: "127.0.0.1:0"
providers:
  - name: "alpha"
    type: "openai"
    base_path: "/alpha"
    upstream_base_url: "http://127.0.0.1:1"
    upstream_api_key: "k"
`
	if err := os.WriteFile(configPath, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Storage.SQLitePath = filepath.Join(dir, "test.db")

	unreachable := filepath.Join(dir, "does", "not", "exist", "config.yaml")
	handlers, err := server.BuildHandlers(context.Background(), cfg, unreachable,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("BuildHandlers: %v", err)
	}

	adminTS := httptest.NewServer(handlers.Admin)
	defer adminTS.Close()

	before := listProviderNames(t, adminTS.URL)
	if len(before) != 1 || before[0] != "alpha" {
		t.Fatalf("pre-mutation providers = %v, want [alpha]", before)
	}

	// Attempt to create "beta". Install should succeed; Save will fail
	// because the configPath parent is missing. The handler should return
	// a 4xx and — the point of this test — roll the container back to
	// the state that held only "alpha".
	req, _ := http.NewRequest(http.MethodPost, adminTS.URL+"/api/providers",
		strings.NewReader(`{"name":"beta","type":"openai","base_path":"/beta",
		                    "upstream_base_url":"http://127.0.0.1:1","upstream_api_key":"k"}`))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode < 400 {
		t.Fatalf("create status = %d, want 4xx (save should have failed); body=%s", resp.StatusCode, body)
	}

	after := listProviderNames(t, adminTS.URL)
	if len(after) != 1 || after[0] != "alpha" {
		t.Fatalf("post-rollback providers = %v, want [alpha] (beta should not have stuck); body=%s", after, body)
	}
}

func listProviderNames(t *testing.T, base string) []string {
	t.Helper()
	resp, err := http.Get(base + "/api/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	names := []string{}
	s := string(data)
	for _, chunk := range strings.Split(s, `"name":`) {
		q := strings.Index(chunk, `"`)
		if q < 0 {
			continue
		}
		rest := chunk[q+1:]
		end := strings.Index(rest, `"`)
		if end < 0 {
			continue
		}
		names = append(names, rest[:end])
	}
	return names
}
