package observability

import (
	"log/slog"
	"net/http"
	"time"

	"llm-proxy/internal/tokencount"
)

// TokenContextMiddleware pre-installs an empty TokenContext into the request
// context so inner handlers (the forwarder) can populate it via pointer and
// outer middlewares (metrics, logging) can read it after next.ServeHTTP
// returns. Without this, each middleware has its own r and the forwarder's
// r.WithContext call is invisible outside its own stack frame.
//
// Mount this as the outermost middleware.
func TokenContextMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tc := &tokencount.TokenContext{}
		r = r.WithContext(tokencount.WithContext(r.Context(), tc))
		next.ServeHTTP(w, r)
	})
}

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
