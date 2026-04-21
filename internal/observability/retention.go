package observability

import (
	"context"
	"log/slog"
	"time"

	"llm-proxy/internal/store"
)

// StartUsageRetention starts a background goroutine that prunes usage_records
// older than retentionDays. It runs once on start (so a freshly-restarted
// proxy catches up) and then every 24 hours. The goroutine exits when ctx is
// cancelled — callers can cancel on process shutdown to trigger a clean stop.
//
// A retention of zero (or negative) disables the cron so tests and
// special-purpose deployments can keep everything forever.
func StartUsageRetention(ctx context.Context, s *store.Store, retentionDays int, logger *slog.Logger) {
	if retentionDays <= 0 {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}

	go func() {
		// Run immediately, then on a daily cadence. A fixed interval is
		// fine — we don't need second-precision retention, and aligning
		// to midnight adds complexity (DST, clock skew) for no real
		// benefit.
		runOnce(ctx, s, retentionDays, logger)
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runOnce(ctx, s, retentionDays, logger)
			}
		}
	}()
}

func runOnce(ctx context.Context, s *store.Store, retentionDays int, logger *slog.Logger) {
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	// Scope the delete to a short timeout so a sudden large deletion can't
	// hold the DB busy indefinitely under an unrelated pause.
	dbCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	deleted, err := s.PurgeUsageOlderThan(dbCtx, cutoff)
	if err != nil {
		logger.Warn("usage retention delete failed", "err", err, "cutoff", cutoff)
		return
	}
	if deleted > 0 {
		logger.Info("usage retention pruned rows", "deleted", deleted, "cutoff", cutoff)
	}
}
