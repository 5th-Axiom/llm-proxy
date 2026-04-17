package observability

import (
	"log/slog"
	"net/http"
	"time"

	"llm-proxy/internal/tokencount"
)

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Flush() {
	flusher, ok := r.ResponseWriter.(http.Flusher)
	if ok {
		flusher.Flush()
	}
}

func LoggingMiddleware(logger *slog.Logger, next http.Handler) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		start := time.Now()

		next.ServeHTTP(recorder, r)

		attrs := []any{
			"method", r.Method,
			"path", r.URL.Path,
			"status", recorder.status,
			"duration_ms", time.Since(start).Milliseconds(),
		}

		if tc := tokencount.FromContext(r.Context()); tc != nil && tc.Enabled {
			attrs = append(attrs,
				"provider", tc.ProviderName,
				"model", tc.Model,
				"prompt_tokens", tc.Counts.PromptTokens,
				"completion_tokens", tc.Counts.CompletionTokens,
				"total_tokens", tc.Counts.TotalTokens,
			)
		}

		logger.Info("http request", attrs...)
	})
}
