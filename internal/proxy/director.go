package proxy

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"time"

	"llm-proxy/internal/config"
	anthropicprovider "llm-proxy/internal/providers/anthropic"
	openaiprovider "llm-proxy/internal/providers/openai"
	"llm-proxy/internal/tokencount"
)

// nonStreamingUpstreamTimeout bounds how long a non-streaming request may wait
// on the upstream. Streaming requests (Accept: text/event-stream or stream:true
// in the body) are exempt so long-running SSE generations don't get cut off.
// It is a var so tests can override it.
var nonStreamingUpstreamTimeout = 60 * time.Second

var versionSegmentPattern = regexp.MustCompile(`^v[0-9]+(?:[A-Za-z0-9._-]*)?$`)

type Forwarder struct {
	client        *http.Client
	tokenCountCfg config.TokenCountingConfig
}

func NewForwarder(client *http.Client, tokenCountCfg config.TokenCountingConfig) *Forwarder {
	return &Forwarder{client: client, tokenCountCfg: tokenCountCfg}
}

func (f *Forwarder) Forward(w http.ResponseWriter, r *http.Request, provider config.ProviderConfig, upstreamPath string) error {
	targetURL, err := buildUpstreamURL(provider.UpstreamBaseURL, upstreamPath, r.URL.RawQuery)
	if err != nil {
		return err
	}

	tokenCounting := provider.IsTokenCountingEnabled(f.tokenCountCfg)
	streaming := clientAcceptsStream(r)

	// tc is pre-installed in the request context by the outer middleware so
	// that metrics/logging can see what we populate here. If it isn't (e.g.
	// unit tests that call Forward directly), we still fill a local one so
	// the response path can consume it.
	tc := tokencount.FromContext(r.Context())
	if tc == nil {
		tc = &tokencount.TokenContext{}
	}

	if tokenCounting && r.Body != nil {
		bodyBytes, err := io.ReadAll(r.Body)
		if err != nil {
			return fmt.Errorf("read request body for token counting: %w", err)
		}
		r.Body.Close()

		rc := tokencount.ParseRequestContent(bodyBytes)
		promptTokens := tokencount.CountPromptTokens(string(provider.Type), bodyBytes)

		tc.ProviderName = provider.Name
		tc.ProviderType = string(provider.Type)
		tc.Model = rc.Model
		tc.Enabled = true
		tc.Counts.PromptTokens = promptTokens
		tc.Counts.PromptEstimated = true

		if rc.Stream {
			streaming = true
			tc.Parser = tokencount.NewStreamingUsageParser(string(provider.Type), rc.Model)
		}

		r.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		r.ContentLength = int64(len(bodyBytes))
	}

	if !streaming {
		ctx, cancel := context.WithTimeout(r.Context(), nonStreamingUpstreamTimeout)
		defer cancel()
		r = r.WithContext(ctx)
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

	if !tc.Enabled {
		tc = nil
	}
	return writeUpstreamResponse(w, r, resp, tc)
}

func buildUpstreamURL(baseURL, path, rawQuery string) (*url.URL, error) {
	base, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, fmt.Errorf("build upstream url: %w", err)
	}

	requestPath := normalizeUpstreamPath(path)
	if hasVersionedBasePath(base.Path) {
		requestPath = trimLeadingAPIVersion(requestPath)
	}

	base.Path = joinURLPath(base.Path, requestPath)
	base.RawPath = ""
	base.RawQuery = rawQuery
	return base, nil
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
	applyOriginalClientMetadata(upstreamReq, r)
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

func clientAcceptsStream(r *http.Request) bool {
	return strings.Contains(strings.ToLower(r.Header.Get("Accept")), "text/event-stream")
}

func applyOriginalClientMetadata(dstReq, srcReq *http.Request) {
	if userAgent := srcReq.Header.Get("User-Agent"); userAgent != "" {
		dstReq.Header.Set("User-Agent", userAgent)
	} else {
		// Prevent the Go client from injecting its own default User-Agent when the caller omitted one.
		dstReq.Header.Set("User-Agent", "")
	}
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

func normalizeUpstreamPath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		return "/" + p
	}
	return p
}

func hasVersionedBasePath(basePath string) bool {
	if basePath == "" || basePath == "/" {
		return false
	}
	segment := path.Base(strings.TrimRight(basePath, "/"))
	return versionSegmentPattern.MatchString(segment)
}

func trimLeadingAPIVersion(requestPath string) string {
	if requestPath == "/v1" {
		return "/"
	}
	if strings.HasPrefix(requestPath, "/v1/") {
		trimmed := strings.TrimPrefix(requestPath, "/v1")
		if trimmed == "" {
			return "/"
		}
		return trimmed
	}
	return requestPath
}

func joinURLPath(basePath, requestPath string) string {
	if basePath == "" || basePath == "/" {
		return normalizeUpstreamPath(requestPath)
	}
	return strings.TrimRight(basePath, "/") + normalizeUpstreamPath(requestPath)
}
