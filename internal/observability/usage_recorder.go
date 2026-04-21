package observability

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"llm-proxy/internal/store"
	"llm-proxy/internal/tokencount"
)

// usageQueueDepth bounds the in-flight backlog. SQLite inserts on a warm WAL
// take well under a millisecond each, so 512 slots is ~hundreds of ms of
// burst capacity — more than enough to soak up spikes without blocking the
// request path or leaking memory if the DB ever stalls.
const usageQueueDepth = 512

// UsageRecorder is a middleware + background writer pair that persists one
// row per authenticated request into usage_records. Writes are fire-and-
// forget from the request's perspective: the middleware drops a struct onto
// a buffered channel, and a single goroutine pulls and inserts. Losing a
// handful of records on process kill is acceptable for this workload
// (metrics, not billing of record).
type UsageRecorder struct {
	store  *store.Store
	logger *slog.Logger

	ch        chan store.UsageRecord
	shutdown  chan struct{}
	drained   chan struct{}
	closeOnce sync.Once
}

func NewUsageRecorder(s *store.Store, logger *slog.Logger) *UsageRecorder {
	if logger == nil {
		logger = slog.Default()
	}
	r := &UsageRecorder{
		store:    s,
		logger:   logger,
		ch:       make(chan store.UsageRecord, usageQueueDepth),
		shutdown: make(chan struct{}),
		drained:  make(chan struct{}),
	}
	go r.run()
	return r
}

// Middleware wraps the proxy handler. Mount it alongside the Prometheus
// metrics middleware — both read from the same TokenContext after
// next.ServeHTTP returns.
func (r *UsageRecorder) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, req)

		tc := tokencount.FromContext(req.Context())
		if tc == nil || tc.UserID == 0 {
			// Unauthenticated or pre-auth failure — Prometheus already
			// captured the 401 counter; writing an unattributable row
			// would just bloat the table.
			return
		}

		record := store.UsageRecord{
			UserID:           tc.UserID,
			KeyID:            tc.KeyID,
			Provider:         tc.ProviderName,
			Model:            tc.Model,
			Status:           rec.status,
			PromptTokens:     tc.Counts.PromptTokens,
			CompletionTokens: tc.Counts.CompletionTokens,
			DurationMs:       time.Since(start).Milliseconds(),
		}

		select {
		case r.ch <- record:
		default:
			// Dropping is better than stalling the client. This only
			// happens when the DB writer can't keep up — logging at warn
			// level surfaces the pressure without spamming.
			r.logger.Warn("usage record channel full, dropping",
				"user_id", record.UserID, "provider", record.Provider)
		}
	})
}

func (r *UsageRecorder) run() {
	defer close(r.drained)
	for {
		select {
		case rec, ok := <-r.ch:
			if !ok {
				return
			}
			r.writeOne(rec)
		case <-r.shutdown:
			// Drain the buffer so pending rows still land before exit.
			for {
				select {
				case rec := <-r.ch:
					r.writeOne(rec)
				default:
					return
				}
			}
		}
	}
}

func (r *UsageRecorder) writeOne(rec store.UsageRecord) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.store.InsertUsage(ctx, rec); err != nil {
		r.logger.Warn("insert usage record", "err", err,
			"user_id", rec.UserID, "provider", rec.Provider)
	}
}

// Close signals the writer to drain and exit. Safe to call multiple times;
// only the first call triggers shutdown.
func (r *UsageRecorder) Close() {
	r.closeOnce.Do(func() { close(r.shutdown) })
	<-r.drained
}
