package proxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm-proxy/internal/config"
)

// TestForwardNonStreamingAppliesTimeout verifies that a slow upstream is cut
// off by the non-streaming timeout rather than hanging the client forever.
func TestForwardNonStreamingAppliesTimeout(t *testing.T) {
	prev := nonStreamingUpstreamTimeout
	nonStreamingUpstreamTimeout = 50 * time.Millisecond
	t.Cleanup(func() { nonStreamingUpstreamTimeout = prev })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer upstream.Close()

	forwarder := NewForwarder(&http.Client{}, config.TokenCountingConfig{})
	provider := config.ProviderConfig{
		Name:            "openai-main",
		Type:            config.ProviderTypeOpenAI,
		BasePath:        "/openai",
		UpstreamBaseURL: upstream.URL,
		UpstreamAPIKey:  "upstream-openai",
	}

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-4.1"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	start := time.Now()
	err := forwarder.Forward(rec, req, provider, "/v1/chat/completions")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Forward() error = nil, want timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Forward() error = %v, want context.DeadlineExceeded", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("Forward elapsed = %v, want ~50ms (timeout)", elapsed)
	}
}

// TestForwardStreamingSkipsTimeout verifies that a client signalling SSE via
// the Accept header is allowed to outlast the non-streaming timeout.
func TestForwardStreamingSkipsTimeout(t *testing.T) {
	prev := nonStreamingUpstreamTimeout
	nonStreamingUpstreamTimeout = 20 * time.Millisecond
	t.Cleanup(func() { nonStreamingUpstreamTimeout = prev })

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(80 * time.Millisecond) // > timeout, but we're streaming
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: hi\n\n"))
	}))
	defer upstream.Close()

	forwarder := NewForwarder(&http.Client{}, config.TokenCountingConfig{})
	provider := config.ProviderConfig{
		Name:            "openai-main",
		Type:            config.ProviderTypeOpenAI,
		BasePath:        "/openai",
		UpstreamBaseURL: upstream.URL,
		UpstreamAPIKey:  "upstream-openai",
	}

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-4.1","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()

	if err := forwarder.Forward(rec, req, provider, "/v1/chat/completions"); err != nil {
		t.Fatalf("Forward() error = %v, want nil (streaming bypasses timeout)", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
