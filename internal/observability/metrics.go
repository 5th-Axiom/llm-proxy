package observability

import (
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
)

type Metrics struct {
	mu                sync.RWMutex
	requestsTotal     uint64
	responsesByStatus map[int]uint64
}

func NewMetrics() *Metrics {
	return &Metrics{
		responsesByStatus: make(map[int]uint64),
	}
}

func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &metricsRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		m.mu.Lock()
		m.requestsTotal++
		m.responsesByStatus[recorder.status]++
		m.mu.Unlock()
	})
}

func (m *Metrics) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		payload := m.snapshot()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	})
}

func (m *Metrics) snapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	statuses := make(map[string]uint64, len(m.responsesByStatus))
	for status, count := range m.responsesByStatus {
		statuses[strconv.Itoa(status)] = count
	}

	return map[string]any{
		"requests_total":      m.requestsTotal,
		"responses_by_status": statuses,
	}
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
