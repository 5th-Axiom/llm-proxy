package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadParsesProvidersAndExpandsEnv(t *testing.T) {
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

	if cfg.Server.Listen != ":8080" {
		t.Fatalf("Listen = %q, want %q", cfg.Server.Listen, ":8080")
	}

	if len(cfg.Server.Tokens) != 1 || cfg.Server.Tokens[0] != "proxy-token" {
		t.Fatalf("Tokens = %#v, want single proxy token", cfg.Server.Tokens)
	}

	if len(cfg.Providers) != 1 {
		t.Fatalf("Providers len = %d, want 1", len(cfg.Providers))
	}

	provider := cfg.Providers[0]
	if provider.Name != "openai-main" {
		t.Fatalf("provider.Name = %q, want openai-main", provider.Name)
	}
	if provider.Type != ProviderTypeOpenAI {
		t.Fatalf("provider.Type = %q, want %q", provider.Type, ProviderTypeOpenAI)
	}
	if provider.BasePath != "/openai" {
		t.Fatalf("provider.BasePath = %q, want /openai", provider.BasePath)
	}
	if provider.UpstreamAPIKey != "upstream-openai-key" {
		t.Fatalf("provider.UpstreamAPIKey = %q, want expanded env value", provider.UpstreamAPIKey)
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
