package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestResolvedExpandsInfrastructureFields guards against a regression where
// only provider + token fields got ${ENV_VAR} expansion and listen
// addresses / storage paths stayed raw — net.Listen would then fail with
// an opaque parse error on the unexpanded "${VAR}" literal.
func TestResolvedExpandsInfrastructureFields(t *testing.T) {
	t.Setenv("LLMP_PROXY_LISTEN", ":18080")
	t.Setenv("LLMP_METRICS_LISTEN", "127.0.0.1:18081")
	t.Setenv("LLMP_DB_PATH", "/var/lib/llm-proxy/db")
	t.Setenv("LLMP_METRICS_TOKEN", "scrape-bearer")

	cfg := Config{
		Server: ServerConfig{
			Listen:        "${LLMP_PROXY_LISTEN}",
			MetricsListen: "${LLMP_METRICS_LISTEN}",
			Tokens:        []string{"t"},
		},
		Admin: AdminConfig{
			MetricsBearerToken: "${LLMP_METRICS_TOKEN}",
		},
		Storage: StorageConfig{
			SQLitePath: "${LLMP_DB_PATH}/main.db",
		},
		Providers: []ProviderConfig{{
			Name:            "p",
			Type:            ProviderTypeOpenAI,
			BasePath:        "/p",
			UpstreamBaseURL: "http://x",
			UpstreamAPIKey:  "k",
		}},
	}

	got := cfg.Resolved()
	if got.Server.Listen != ":18080" {
		t.Errorf("Server.Listen = %q, want :18080", got.Server.Listen)
	}
	if got.Server.MetricsListen != "127.0.0.1:18081" {
		t.Errorf("Server.MetricsListen = %q, want 127.0.0.1:18081", got.Server.MetricsListen)
	}
	if got.Storage.SQLitePath != "/var/lib/llm-proxy/db/main.db" {
		t.Errorf("Storage.SQLitePath = %q, want expanded path", got.Storage.SQLitePath)
	}
	if got.Admin.MetricsBearerToken != "scrape-bearer" {
		t.Errorf("Admin.MetricsBearerToken = %q, want expanded", got.Admin.MetricsBearerToken)
	}

	// Source config must remain raw so a subsequent Save round-trips the
	// placeholders back to disk.
	if cfg.Server.Listen != "${LLMP_PROXY_LISTEN}" {
		t.Errorf("Resolved() mutated cfg.Server.Listen: %q", cfg.Server.Listen)
	}
	if cfg.Storage.SQLitePath != "${LLMP_DB_PATH}/main.db" {
		t.Errorf("Resolved() mutated cfg.Storage.SQLitePath: %q", cfg.Storage.SQLitePath)
	}
}

func TestLoadPreservesEnvPlaceholdersAndResolvedExpands(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "upstream-openai-key")

	configPath := writeTempConfig(t, `
server:
  listen: ":8080"
  tokens:
    - "proxy-token"

providers:
  - name: "openai-main"
    type: "openai"
    base_path: "/openai"
    upstream_base_url: "https://api.openai.com"
    upstream_api_key: "${OPENAI_API_KEY}"
`)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	raw := cfg.Providers[0].UpstreamAPIKey
	if raw != "${OPENAI_API_KEY}" {
		t.Fatalf("raw UpstreamAPIKey = %q, want env placeholder preserved", raw)
	}

	resolved := cfg.Resolved()
	if got := resolved.Providers[0].UpstreamAPIKey; got != "upstream-openai-key" {
		t.Fatalf("resolved UpstreamAPIKey = %q, want expanded env value", got)
	}

	// Resolved must not mutate the original raw Config.
	if cfg.Providers[0].UpstreamAPIKey != "${OPENAI_API_KEY}" {
		t.Fatalf("Resolved() mutated the source Config")
	}
}

func TestSaveRoundTripsEnvPlaceholders(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "should-not-appear-in-file")

	configPath := writeTempConfig(t, `
server:
  listen: ":8080"
  metrics_listen: "127.0.0.1:8081"
  tokens:
    - "proxy-token"

providers:
  - name: "openai-main"
    type: "openai"
    base_path: "/openai"
    upstream_base_url: "https://api.openai.com"
    upstream_api_key: "${OPENAI_API_KEY}"
`)

	cfg, err := Load(configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	cfg.Providers = append(cfg.Providers, ProviderConfig{
		Name:            "anthropic-main",
		Type:            ProviderTypeAnthropic,
		BasePath:        "/anthropic",
		UpstreamBaseURL: "https://api.anthropic.com",
		UpstreamAPIKey:  "${ANTHROPIC_API_KEY}",
	})

	if err := Save(configPath, cfg); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	contents := string(data)
	if !strings.Contains(contents, "${OPENAI_API_KEY}") {
		t.Errorf("saved config lost ${OPENAI_API_KEY} placeholder:\n%s", contents)
	}
	if !strings.Contains(contents, "${ANTHROPIC_API_KEY}") {
		t.Errorf("saved config missing new ${ANTHROPIC_API_KEY} placeholder:\n%s", contents)
	}
	if strings.Contains(contents, "should-not-appear-in-file") {
		t.Errorf("saved config leaked expanded env value:\n%s", contents)
	}

	reloaded, err := Load(configPath)
	if err != nil {
		t.Fatalf("reload after Save() error = %v", err)
	}
	if len(reloaded.Providers) != 2 {
		t.Fatalf("reloaded providers len = %d, want 2", len(reloaded.Providers))
	}
}

func TestLoadRejectsDuplicateBasePath(t *testing.T) {
	configPath := writeTempConfig(t, `
server:
  listen: ":8080"
  tokens:
    - "proxy-token"

providers:
  - name: "openai-main"
    type: "openai"
    base_path: "/shared"
    upstream_base_url: "https://api.openai.com"
    upstream_api_key: "key-1"
  - name: "glm-main"
    type: "openai"
    base_path: "/shared"
    upstream_base_url: "https://open.bigmodel.cn/api/paas"
    upstream_api_key: "key-2"
`)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load() error = nil, want duplicate base_path validation error")
	}
}

func TestLoadRejectsUnknownProviderType(t *testing.T) {
	configPath := writeTempConfig(t, `
server:
  listen: ":8080"
  tokens:
    - "proxy-token"

providers:
  - name: "unsupported"
    type: "custom"
    base_path: "/custom"
    upstream_base_url: "https://example.com"
    upstream_api_key: "key"
`)

	_, err := Load(configPath)
	if err == nil {
		t.Fatal("Load() error = nil, want unknown provider type validation error")
	}
}

func writeTempConfig(t *testing.T, contents string) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	return path
}
