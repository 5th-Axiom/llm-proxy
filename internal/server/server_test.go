package server

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"llm-proxy/internal/config"
)

func TestHandlerProxiesOpenAIRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("upstream path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-openai" {
			t.Fatalf("Authorization = %q, want upstream bearer token", got)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("io.ReadAll() error = %v", err)
		}
		if string(body) != `{"model":"gpt-4.1"}` {
			t.Fatalf("body = %q, want forwarded request body", string(body))
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1"}`))
	}))
	defer upstream.Close()

	handler := testHandler(t, upstream.URL)

	req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-4.1"}`))
	req.Header.Set("Authorization", "Bearer proxy-token")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	if body := rec.Body.String(); body != `{"id":"chatcmpl-1"}` {
		t.Fatalf("body = %q, want upstream response body", body)
	}
}

func TestHandlerProxiesAnthropicRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("upstream path = %q, want /v1/messages", r.URL.Path)
		}
		if got := r.Header.Get("x-api-key"); got != "upstream-anthropic" {
			t.Fatalf("x-api-key = %q, want upstream anthropic key", got)
		}
		if got := r.Header.Get("anthropic-version"); got != "2023-06-01" {
			t.Fatalf("anthropic-version = %q, want preserved header", got)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"msg_123"}`))
	}))
	defer upstream.Close()

	handler := testHandler(t, upstream.URL)

	req := httptest.NewRequest(http.MethodPost, "/anthropic/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4-5"}`))
	req.Header.Set("x-api-key", "proxy-token")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != `{"id":"msg_123"}` {
		t.Fatalf("body = %q, want upstream response body", body)
	}
}

func TestHandlerProxiesModelsRequests(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Fatalf("upstream path = %q, want /v1/models", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-4.1"}]}`))
	}))
	defer upstream.Close()

	handler := testHandler(t, upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer proxy-token")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != `{"data":[{"id":"gpt-4.1"}]}` {
		t.Fatalf("body = %q, want upstream response body", body)
	}
}

func TestHandlerSupportsMultipleOpenAICompatibleProviders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("upstream path = %q, want /v1/chat/completions", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer upstream-glm" {
			t.Fatalf("Authorization = %q, want glm upstream bearer token", got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"glm-1"}`))
	}))
	defer upstream.Close()

	handler := testHandlerWithProviders(t, []config.ProviderConfig{
		{
			Name:            "glm-prod",
			Type:            config.ProviderTypeOpenAI,
			BasePath:        "/glm",
			UpstreamBaseURL: upstream.URL,
			UpstreamAPIKey:  "upstream-glm",
		},
	})

	req := httptest.NewRequest(http.MethodPost, "/glm/v1/chat/completions", strings.NewReader(`{"model":"glm-4-flash"}`))
	req.Header.Set("Authorization", "Bearer proxy-token")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if body := rec.Body.String(); body != `{"id":"glm-1"}` {
		t.Fatalf("body = %q, want upstream response body", body)
	}
}

func TestHandlerStreamsWithoutWaitingForFullResponse(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("upstream ResponseWriter does not implement http.Flusher")
		}

		_, _ = w.Write([]byte("data: first\n\n"))
		flusher.Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = w.Write([]byte("data: second\n\n"))
		flusher.Flush()
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(testHandler(t, upstream.URL))
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/openai/v1/chat/completions", strings.NewReader(`{"stream":true}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer proxy-token")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	defer resp.Body.Close()

	reader := bufio.NewReader(resp.Body)
	firstChunk := make(chan string, 1)
	go func() {
		line, _ := reader.ReadString('\n')
		firstChunk <- line
	}()

	select {
	case line := <-firstChunk:
		if line != "data: first\n" {
			t.Fatalf("first stream line = %q, want first event", line)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("did not receive first stream chunk before upstream delay elapsed")
	}
}

func TestHandlerExposesBasicMetrics(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()

	proxy := httptest.NewServer(testHandler(t, upstream.URL))
	defer proxy.Close()

	req, err := http.NewRequest(http.MethodPost, proxy.URL+"/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-4.1"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer proxy-token")
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	resp.Body.Close()

	metricsResp, err := client.Get(proxy.URL + "/metrics")
	if err != nil {
		t.Fatalf("client.Get() error = %v", err)
	}
	defer metricsResp.Body.Close()

	var metrics map[string]any
	if err := json.NewDecoder(metricsResp.Body).Decode(&metrics); err != nil {
		t.Fatalf("json decode error = %v", err)
	}

	if metrics["requests_total"] != float64(1) {
		t.Fatalf("requests_total = %#v, want 1", metrics["requests_total"])
	}

	statuses, ok := metrics["responses_by_status"].(map[string]any)
	if !ok {
		t.Fatalf("responses_by_status = %#v, want object", metrics["responses_by_status"])
	}
	if statuses["201"] != float64(1) {
		t.Fatalf("responses_by_status[201] = %#v, want 1", statuses["201"])
	}
}

func testHandler(t *testing.T, upstreamBaseURL string) http.Handler {
	t.Helper()

	return testHandlerWithProviders(t, []config.ProviderConfig{
		{
			Name:            "openai-main",
			Type:            config.ProviderTypeOpenAI,
			BasePath:        "/openai",
			UpstreamBaseURL: upstreamBaseURL,
			UpstreamAPIKey:  "upstream-openai",
		},
		{
			Name:            "claude-main",
			Type:            config.ProviderTypeAnthropic,
			BasePath:        "/anthropic",
			UpstreamBaseURL: upstreamBaseURL,
			UpstreamAPIKey:  "upstream-anthropic",
		},
	})
}

func testHandlerWithProviders(t *testing.T, providers []config.ProviderConfig) http.Handler {
	t.Helper()

	cfg := config.Config{
		Server: config.ServerConfig{
			Listen: ":8080",
			Tokens: []string{"proxy-token"},
		},
		Providers: providers,
	}

	handler, err := NewHandler(context.Background(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	return handler
}
