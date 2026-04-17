package observability

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"

	"llm-proxy/internal/tokencount"
)

type Metrics struct {
	mu                sync.RWMutex
	requestsTotal     uint64
	responsesByStatus map[int]uint64
	promptTokens      map[string]uint64
	completionTokens  map[string]uint64
}

func NewMetrics() *Metrics {
	return &Metrics{
		responsesByStatus: make(map[int]uint64),
		promptTokens:      make(map[string]uint64),
		completionTokens:  make(map[string]uint64),
	}
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &metricsRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		m.mu.Lock()
		m.requestsTotal++
		m.responsesByStatus[recorder.status]++

		if tc := tokencount.FromContext(r.Context()); tc != nil && tc.Enabled {
			provider := tc.ProviderName
			m.promptTokens[provider] += uint64(tc.Counts.PromptTokens)
			m.completionTokens[provider] += uint64(tc.Counts.CompletionTokens)
		}
		m.mu.Unlock()
	})
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("format") == "prometheus" {
			w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
			_ = m.writePrometheus(w)
			return
		}
		payload := m.snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
}

func (m *Metrics) writePrometheus(w http.ResponseWriter) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	_, _ = fmt.Fprintf(w, "# HELP llm_proxy_requests_total Total number of proxy requests.\n")
	_, _ = fmt.Fprintf(w, "# TYPE llm_proxy_requests_total counter\n")
	_, _ = fmt.Fprintf(w, "llm_proxy_requests_total %d\n\n", m.requestsTotal)

	_, _ = fmt.Fprintf(w, "# HELP llm_proxy_responses_total Total number of responses by status code.\n")
	_, _ = fmt.Fprintf(w, "# TYPE llm_proxy_responses_total counter\n")

	statuses := make([]int, 0, len(m.responsesByStatus))
	for status := range m.responsesByStatus {
		statuses = append(statuses, status)
	}
	sort.Ints(statuses)
	for _, status := range statuses {
		_, _ = fmt.Fprintf(w, "llm_proxy_responses_total{status=\"%d\"} %d\n", status, m.responsesByStatus[status])
	}

	if len(m.promptTokens) > 0 || len(m.completionTokens) > 0 {
		_, _ = fmt.Fprintf(w, "\n# HELP llm_proxy_prompt_tokens_total Total prompt tokens by provider.\n")
		_, _ = fmt.Fprintf(w, "# TYPE llm_proxy_prompt_tokens_total counter\n")
		for _, provider := range sortedStringKeys(m.promptTokens) {
			_, _ = fmt.Fprintf(w, "llm_proxy_prompt_tokens_total{provider=\"%s\"} %d\n", provider, m.promptTokens[provider])
		}

		_, _ = fmt.Fprintf(w, "\n# HELP llm_proxy_completion_tokens_total Total completion tokens by provider.\n")
		_, _ = fmt.Fprintf(w, "# TYPE llm_proxy_completion_tokens_total counter\n")
		for _, provider := range sortedStringKeys(m.completionTokens) {
			_, _ = fmt.Fprintf(w, "llm_proxy_completion_tokens_total{provider=\"%s\"} %d\n", provider, m.completionTokens[provider])
		}
	}

	return nil
}

func (m *Metrics) snapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make(map[string]uint64, len(m.responsesByStatus))
	for status, count := range m.responsesByStatus {
		statuses[strconv.Itoa(status)] = count
	}

	result := map[string]any{
		"requests_total":      m.requestsTotal,
		"responses_by_status": statuses,
	}

	if len(m.promptTokens) > 0 || len(m.completionTokens) > 0 {
		tokenUsage := make(map[string]any)
		for provider := range m.promptTokens {
			tokenUsage[provider] = map[string]uint64{
				"prompt_tokens":     m.promptTokens[provider],
				"completion_tokens": m.completionTokens[provider],
			}
		}
		for provider := range m.completionTokens {
			if _, ok := tokenUsage[provider]; !ok {
				tokenUsage[provider] = map[string]uint64{
					"prompt_tokens":     0,
					"completion_tokens": m.completionTokens[provider],
				}
			}
		}
		result["token_usage_by_provider"] = tokenUsage
	}

	return result
}

func sortedStringKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

type metricsRecorder struct {
	http.ResponseWriter
	status int
}

func (r *metricsRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

func (r *metricsRecorder) Flush() {
	flusher, ok := r.ResponseWriter.(http.Flusher)
	if ok {
		flusher.Flush()
	}
}
