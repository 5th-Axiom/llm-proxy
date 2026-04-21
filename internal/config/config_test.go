package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
