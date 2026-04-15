package providers

import (
	"sort"
	"strings"

	"llm-proxy/internal/config"
)

type Registry struct {
	providers []config.ProviderConfig
}

func NewRegistry(providers []config.ProviderConfig) (*Registry, error) {
	ordered := make([]config.ProviderConfig, len(providers))
	copy(ordered, providers)

	sort.SliceStable(ordered, func(i, j int) bool {
		return len(ordered[i].BasePath) > len(ordered[j].BasePath)
	})

	return &Registry{providers: ordered}, nil
}

func (r *Registry) Match(path string) (config.ProviderConfig, string, bool) {
	for _, provider := range r.providers {
		if !matchesBasePath(path, provider.BasePath) {
			continue
		}

		upstreamPath := strings.TrimPrefix(path, provider.BasePath)
		if upstreamPath == "" {
			upstreamPath = "/"
		}
		if !strings.HasPrefix(upstreamPath, "/") {
			upstreamPath = "/" + upstreamPath
		}
		return provider, upstreamPath, true
	}

	return config.ProviderConfig{}, "", false
}

func matchesBasePath(path, basePath string) bool {
	if path == basePath {
		return true
	}
	return strings.HasPrefix(path, basePath+"/")
}
