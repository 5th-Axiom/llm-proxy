package config

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"gopkg.in/yaml.v3"
)

type ProviderType string

const (
	ProviderTypeOpenAI    ProviderType = "openai"
	ProviderTypeAnthropic ProviderType = "anthropic"
)

// Config is the raw configuration as stored on disk. String fields may contain
// ${ENV_VAR} placeholders; call Resolved() to obtain a copy with environment
// variables expanded for use at request time. Keeping the raw form in memory
// lets the admin API round-trip the file without losing env-var references.
type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Admin         AdminConfig         `yaml:"admin,omitempty"`
	Transport     TransportConfig     `yaml:"transport"`
	Providers     []ProviderConfig    `yaml:"providers"`
	TokenCounting TokenCountingConfig `yaml:"token_counting"`
}

// AdminConfig controls authentication for the admin listener. When
// PasswordHash is empty, the admin UI and API are open — appropriate when the
// listener is loopback-only and trusted to its OS boundary. Set PasswordHash
// (generate with `llm-proxy -hash-password`) to require login via cookie
// session.
//
// MetricsBearerToken, if set, lets a scraper (Prometheus) pull /metrics with
// `Authorization: Bearer <token>` without needing a session cookie. This is
// the preferred way to expose metrics when admin login is enabled; keep the
// token separate from server.tokens so scraping credentials can be rotated
// independently of client proxy tokens.
type AdminConfig struct {
	PasswordHash       string `yaml:"password_hash,omitempty"`
	SessionTTLMin      int    `yaml:"session_ttl_min,omitempty"`
	MetricsBearerToken string `yaml:"metrics_bearer_token,omitempty"`
}

type TokenCountingConfig struct {
	Enabled bool `yaml:"enabled"`
}

type ServerConfig struct {
	Listen        string   `yaml:"listen"`
	MetricsListen string   `yaml:"metrics_listen"`
	Tokens        []string `yaml:"tokens"`
}

type TransportConfig struct {
	MaxIdleConns        int `yaml:"max_idle_conns"`
	MaxIdleConnsPerHost int `yaml:"max_idle_conns_per_host"`
	MaxConnsPerHost     int `yaml:"max_conns_per_host"`
	IdleConnTimeoutSec  int `yaml:"idle_conn_timeout_sec"`
}

type ProviderConfig struct {
	Name            string            `yaml:"name"`
	Type            ProviderType      `yaml:"type"`
	BasePath        string            `yaml:"base_path"`
	UpstreamBaseURL string            `yaml:"upstream_base_url"`
	UpstreamAPIKey  string            `yaml:"upstream_api_key"`
	UpstreamHeaders map[string]string `yaml:"upstream_headers,omitempty"`
	// TokenCounting is a pointer so nil means "inherit global", allowing a
	// provider to opt out of token counting even when the global default is on.
	TokenCounting *bool `yaml:"token_counting,omitempty"`
}

func (p ProviderConfig) IsTokenCountingEnabled(global TokenCountingConfig) bool {
	if p.TokenCounting != nil {
		return *p.TokenCounting
	}
	return global.Enabled
}

// Load reads a YAML config from disk. String values are stored verbatim —
// ${ENV_VAR} references are preserved so Save can round-trip them. Consumers
// that need expanded values should call Resolved().
func Load(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(contents, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// Save writes the config back to disk as YAML. Env-var placeholders inside
// string fields are preserved because Config holds raw strings.
func Save(path string, cfg Config) error {
	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return err
	}

	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	// Atomic rename so a crashed write cannot produce a half-written config.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename config: %w", err)
	}
	return nil
}

// Resolved returns a deep copy of the config with ${ENV_VAR} placeholders in
// string fields expanded via os.ExpandEnv. The original Config is left
// untouched so subsequent Save writes back the raw form.
func (c Config) Resolved() Config {
	out := c
	out.Server.Tokens = expandAll(c.Server.Tokens)

	out.Providers = make([]ProviderConfig, len(c.Providers))
	for i, p := range c.Providers {
		p.UpstreamBaseURL = os.ExpandEnv(p.UpstreamBaseURL)
		p.UpstreamAPIKey = os.ExpandEnv(p.UpstreamAPIKey)
		if p.UpstreamHeaders != nil {
			headers := make(map[string]string, len(p.UpstreamHeaders))
			for k, v := range p.UpstreamHeaders {
				headers[k] = os.ExpandEnv(v)
			}
			p.UpstreamHeaders = headers
		}
		out.Providers[i] = p
	}
	return out
}

func expandAll(in []string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, v := range in {
		out[i] = os.ExpandEnv(v)
	}
	return out
}

func (c *Config) applyDefaults() {
	if c.Server.Listen == "" {
		c.Server.Listen = ":8080"
	}
	if c.Server.MetricsListen == "" {
		c.Server.MetricsListen = "127.0.0.1:8081"
	}
	if c.Transport.MaxIdleConns == 0 {
		c.Transport.MaxIdleConns = 512
	}
	if c.Transport.MaxIdleConnsPerHost == 0 {
		c.Transport.MaxIdleConnsPerHost = 128
	}
	if c.Transport.IdleConnTimeoutSec == 0 {
		c.Transport.IdleConnTimeoutSec = 90
	}
	for i := range c.Providers {
		c.Providers[i].BasePath = normalizeBasePath(c.Providers[i].BasePath)
		c.Providers[i].UpstreamBaseURL = strings.TrimRight(c.Providers[i].UpstreamBaseURL, "/")
	}
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.Server.Listen) == "" {
		return errors.New("server.listen is required")
	}
	if strings.TrimSpace(c.Server.MetricsListen) == "" {
		return errors.New("server.metrics_listen is required")
	}
	if c.Server.MetricsListen == c.Server.Listen {
		return errors.New("server.metrics_listen must differ from server.listen")
	}
	if len(c.Server.Tokens) == 0 {
		return errors.New("server.tokens must contain at least one token")
	}
	if len(c.Providers) == 0 {
		return errors.New("providers must contain at least one provider")
	}

	names := map[string]struct{}{}
	basePaths := map[string]struct{}{}

	for _, provider := range c.Providers {
		if err := ValidateProvider(provider); err != nil {
			return err
		}
		if _, exists := names[provider.Name]; exists {
			return fmt.Errorf("duplicate provider name: %s", provider.Name)
		}
		names[provider.Name] = struct{}{}
		if _, exists := basePaths[provider.BasePath]; exists {
			return fmt.Errorf("duplicate provider base_path: %s", provider.BasePath)
		}
		basePaths[provider.BasePath] = struct{}{}
	}

	return nil
}

// ValidateProvider checks a single provider's required fields. Exposed so the
// admin API can validate an incoming payload before merging it.
func ValidateProvider(provider ProviderConfig) error {
	if strings.TrimSpace(provider.Name) == "" {
		return errors.New("provider name is required")
	}
	switch provider.Type {
	case ProviderTypeOpenAI, ProviderTypeAnthropic:
	default:
		return fmt.Errorf("unsupported provider type: %s", provider.Type)
	}
	normalized := normalizeBasePath(provider.BasePath)
	if normalized == "" || normalized == "/" {
		return fmt.Errorf("provider %s base_path must not be empty or root", provider.Name)
	}
	if strings.TrimSpace(provider.UpstreamBaseURL) == "" {
		return fmt.Errorf("provider %s upstream_base_url is required", provider.Name)
	}
	if strings.TrimSpace(provider.UpstreamAPIKey) == "" {
		return fmt.Errorf("provider %s upstream_api_key is required", provider.Name)
	}
	return nil
}

func normalizeBasePath(p string) string {
	if p == "" {
		return ""
	}
	cleaned := path.Clean(p)
	if !strings.HasPrefix(cleaned, "/") {
		cleaned = "/" + cleaned
	}
	if cleaned != "/" {
		cleaned = strings.TrimRight(cleaned, "/")
	}
	return cleaned
}

// NormalizeBasePath is the exported form for admin validation.
func NormalizeBasePath(p string) string {
	return normalizeBasePath(p)
}
