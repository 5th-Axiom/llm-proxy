package anthropic

import (
	"net/http"

	"llm-proxy/internal/config"
)

func ApplyHeaders(headers http.Header, provider config.ProviderConfig) {
	headers.Del("Authorization")
	headers.Set("x-api-key", provider.UpstreamAPIKey)
	applyStaticHeaders(headers, provider.UpstreamHeaders)
}

func applyStaticHeaders(headers http.Header, values map[string]string) {
	for key, value := range values {
		headers.Set(key, value)
	}
}
