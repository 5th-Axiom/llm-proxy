package observability

import (
	"context"
	"log/slog"
	"time"

	"llm-proxy/internal/appstate"
)

// StartUsageRetention starts a background goroutine that prunes usage_records
// older than the configured retention. It runs once on start (so a
// freshly-restarted proxy catches up) and then every 24 hours. The goroutine
// exits when ctx is cancelled.
//
// The retention value is re-read from the container on every tick instead of
// being captured at construction, so a Settings-API edit flows through at the
// next cycle without a restart.
//
// A retention of zero (or negative) skips that iteration's cleanup without
// stopping the loop, so re-enabling retention later picks back up.
func StartUsageRetention(ctx context.Context, container *appstate.Container, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}

	go func() {
		runOnce(ctx, container, logger)
		t := time.NewTicker(24 * time.Hour)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				runOnce(ctx, container, logger)
			}
		}
	}()
}

func runOnce(ctx context.Context, container *appstate.Container, logger *slog.Logger) {
	state := container.Load()
	if state == nil {
		return
	}
	days := state.Raw.Storage.UsageRetentionDays
	if days <= 0 {
		return
	}

	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour)
	dbCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	deleted, err := state.Store.PurgeUsageOlderThan(dbCtx, cutoff)
	if err != nil {
		logger.Warn("usage retention delete failed", "err", err, "cutoff", cutoff)
		return
	}
	if deleted > 0 {
		logger.Info("usage retention pruned rows", "deleted", deleted, "cutoff", cutoff, "days", days)
	}
}
