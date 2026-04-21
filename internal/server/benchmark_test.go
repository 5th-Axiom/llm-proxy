package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"llm-proxy/internal/config"
)

func BenchmarkOpenAIProxyRoundTrip(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"chatcmpl-bench","object":"chat.completion"}`))
	}))
	defer upstream.Close()

	handler := benchmarkHandler(b, upstream.URL)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodPost, "/openai/v1/chat/completions", strings.NewReader(`{"model":"gpt-4.1-mini"}`))
		req.Header.Set("Authorization", "Bearer proxy-token")
		req.Header.Set("Content-Type", "application/json")

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			b.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
	}
}

func benchmarkHandler(b *testing.B, upstreamBaseURL string) http.Handler {
	b.Helper()

	cfg := config.Config{
		Server: config.ServerConfig{
			Listen:        ":8080",
			MetricsListen: "127.0.0.1:0",
			Tokens:        []string{"proxy-token"},
		},
		Providers: []config.ProviderConfig{
			{
				Name:            "openai-main",
				Type:            config.ProviderTypeOpenAI,
				BasePath:        "/openai",
				UpstreamBaseURL: upstreamBaseURL,
				UpstreamAPIKey:  "upstream-openai",
			},
		},
	}

	handlers, err := BuildHandlers(b.Context(), cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		b.Fatalf("BuildHandlers() error = %v", err)
	}
	return handlers.Public
}
