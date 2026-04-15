package providers

import (
	"testing"

	"llm-proxy/internal/config"
)

func TestRegistryMatchReturnsLongestBasePath(t *testing.T) {
	registry, err := NewRegistry([]config.ProviderConfig{
		{
			Name:            "openai-root",
			Type:            config.ProviderTypeOpenAI,
			BasePath:        "/openai",
			UpstreamBaseURL: "https://api.openai.com",
			UpstreamAPIKey:  "key-1",
		},
		{
			Name:            "openai-eu",
			Type:            config.ProviderTypeOpenAI,
			BasePath:        "/openai/eu",
			UpstreamBaseURL: "https://eu.api.openai.com",
			UpstreamAPIKey:  "key-2",
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	provider, upstreamPath, ok := registry.Match("/openai/eu/v1/chat/completions")
	if !ok {
		t.Fatal("Match() ok = false, want true")
	}

	if provider.Name != "openai-eu" {
		t.Fatalf("provider.Name = %q, want openai-eu", provider.Name)
	}

	if upstreamPath != "/v1/chat/completions" {
		t.Fatalf("upstreamPath = %q, want /v1/chat/completions", upstreamPath)
	}
}

func TestRegistryMatchRejectsUnknownPath(t *testing.T) {
	registry, err := NewRegistry([]config.ProviderConfig{
		{
			Name:            "openai-root",
			Type:            config.ProviderTypeOpenAI,
			BasePath:        "/openai",
			UpstreamBaseURL: "https://api.openai.com",
			UpstreamAPIKey:  "key-1",
		},
	})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}

	_, _, ok := registry.Match("/unknown/v1/models")
	if ok {
		t.Fatal("Match() ok = true, want false")
	}
}
