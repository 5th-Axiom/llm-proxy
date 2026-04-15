package proxy

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"llm-proxy/internal/config"
	anthropicprovider "llm-proxy/internal/providers/anthropic"
	openaiprovider "llm-proxy/internal/providers/openai"
)

type Forwarder struct {
	client *http.Client
}

func NewForwarder(client *http.Client) *Forwarder {
	return &Forwarder{client: client}
}

func (f *Forwarder) Forward(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig, upstreamPath string) error {
	targetURL, err := buildUpstreamURL(provider.UpstreamBaseURL, upstreamPath, r.URL.RawQuery)
	if err != nil {
		return err
	}

	upstreamReq, err := buildUpstreamRequest(r, targetURL, provider)
	if err != nil {
		return err
	}

	resp, err := f.client.Do(upstreamReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	return writeUpstreamResponse(w, r, resp)
}

func buildUpstreamURL(baseURL, path, rawQuery string) (*url.URL, error) {
	target, err := url.Parse(strings.TrimRight(baseURL, "/") + path)
	if err != nil {
		return nil, fmt.Errorf("build upstream url: %w", err)
	}
	target.RawQuery = rawQuery
	return target, nil
}

func buildUpstreamRequest(r *http.Request, target *url.URL, provider config.ProviderConfig) (*http.Request, error) {
	var body io.ReadCloser
	if r.Body != nil {
		body = r.Body
	}

	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("create upstream request: %w", err)
	}

	copyRequestHeaders(upstreamReq.Header, r.Header)
	upstreamReq.ContentLength = r.ContentLength

	switch provider.Type {
	case config.ProviderTypeOpenAI:
		openaiprovider.ApplyHeaders(upstreamReq.Header, provider)
	case config.ProviderTypeAnthropic:
		anthropicprovider.ApplyHeaders(upstreamReq.Header, provider)
	default:
		return nil, fmt.Errorf("unsupported provider type: %s", provider.Type)
	}

	return upstreamReq, nil
}

func copyRequestHeaders(dst, src http.Header) {
	for key, values := range src {
		if isHopByHopHeader(key) {
			continue
		}
		if strings.EqualFold(key, "Authorization") || strings.EqualFold(key, "x-api-key") {
			continue
		}
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func isHopByHopHeader(key string) bool {
	switch strings.ToLower(key) {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	default:
		return false
	}
}
