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

type Config struct {
	Server        ServerConfig        `yaml:"server"`
	Transport     TransportConfig     `yaml:"transport"`
	Providers     []ProviderConfig    `yaml:"providers"`
	TokenCounting TokenCountingConfig `yaml:"token_counting"`
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
	UpstreamHeaders map[string]string `yaml:"upstream_headers"`
	// TokenCounting is a pointer so nil means "inherit global", allowing a
	// provider to opt out of token counting even when the global default is on.
	TokenCounting *bool `yaml:"token_counting"`
}

func (p ProviderConfig) IsTokenCountingEnabled(global TokenCountingConfig) bool {
	if p.TokenCounting != nil {
		return *p.TokenCounting
	}
	return global.Enabled
}

func Load(path string) (Config, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	expanded := os.ExpandEnv(string(contents))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return Config{}, fmt.Errorf("parse config: %w", err)
	}

	cfg.applyDefaults()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	return cfg, nil
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
		if strings.TrimSpace(provider.Name) == "" {
			return errors.New("provider name is required")
		}
		if _, exists := names[provider.Name]; exists {
			return fmt.Errorf("duplicate provider name: %s", provider.Name)
		}
		names[provider.Name] = struct{}{}

		switch provider.Type {
		case ProviderTypeOpenAI, ProviderTypeAnthropic:
		default:
			return fmt.Errorf("unsupported provider type: %s", provider.Type)
		}

		if provider.BasePath == "" || provider.BasePath == "/" {
			return fmt.Errorf("provider %s base_path must not be empty or root", provider.Name)
		}
		if _, exists := basePaths[provider.BasePath]; exists {
			return fmt.Errorf("duplicate provider base_path: %s", provider.BasePath)
		}
		basePaths[provider.BasePath] = struct{}{}

		if strings.TrimSpace(provider.UpstreamBaseURL) == "" {
			return fmt.Errorf("provider %s upstream_base_url is required", provider.Name)
		}
		if strings.TrimSpace(provider.UpstreamAPIKey) == "" {
			return fmt.Errorf("provider %s upstream_api_key is required", provider.Name)
		}
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
