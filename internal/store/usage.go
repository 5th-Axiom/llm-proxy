package store

import (
	"context"
	"database/sql"
	"time"
)

// UsageRecord is a single row in usage_records. One is written after every
// authenticated proxy request completes (streaming or not). Unauthenticated
// requests are left to Prometheus counters — storing 401-attempts here would
// pollute the table with unattributable rows.
type UsageRecord struct {
	UserID           int64
	KeyID            int64
	Provider         string
	Model            string
	Status           int
	PromptTokens     int
	CompletionTokens int
	DurationMs       int64
	RecordedAt       time.Time
}

// InsertUsage appends one row. Time is set by the DB (CURRENT_TIMESTAMP) so
// concurrent writers get monotonic ordering even if their Go clocks disagree
// slightly. Callers can override RecordedAt in tests by setting it non-zero.
func (s *Store) InsertUsage(ctx context.Context, r UsageRecord) error {
	if r.RecordedAt.IsZero() {
		_, err := s.db.ExecContext(ctx, `
			INSERT INTO usage_records
				(user_id, key_id, provider, model, status,
				 prompt_tokens, completion_tokens, duration_ms)
			VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?)`,
			nullableInt64(r.UserID), nullableInt64(r.KeyID),
			r.Provider, r.Model, r.Status,
			r.PromptTokens, r.CompletionTokens, r.DurationMs)
		return err
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO usage_records
			(user_id, key_id, provider, model, status,
			 prompt_tokens, completion_tokens, duration_ms, recorded_at)
		VALUES (?, ?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`,
		nullableInt64(r.UserID), nullableInt64(r.KeyID),
		r.Provider, r.Model, r.Status,
		r.PromptTokens, r.CompletionTokens, r.DurationMs,
		r.RecordedAt.UTC())
	return err
}

// PurgeUsageOlderThan deletes rows older than cutoff and returns the row
// count. Called from a daily cron so usage_records stays bounded by the
// configured retention window.
func (s *Store) PurgeUsageOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM usage_records WHERE recorded_at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// UserUsage rolls up a user's traffic over a time window.
type UserUsage struct {
	UserID           int64
	UserName         string
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
}

// ProviderUsage rolls up per-provider traffic over the same window.
type ProviderUsage struct {
	Provider         string
	Requests         int64
	PromptTokens     int64
	CompletionTokens int64
}

// UsageSummary returns both roll-ups for requests recorded at or after
// `since`. Passing the zero time yields an all-time summary — cheap because
// the retention cleanup bounds the table size anyway.
type UsageSummary struct {
	Since     time.Time
	Users     []UserUsage
	Providers []ProviderUsage
}

// GetUsageSummary runs two aggregation queries (by user, by provider) and
// stitches them together. Each query is cheap thanks to the recorded_at
// index; we deliberately don't try to share state between them so the two
// result sets can be consumed independently.
func (s *Store) GetUsageSummary(ctx context.Context, since time.Time) (*UsageSummary, error) {
	out := &UsageSummary{Since: since.UTC()}

	userRows, err := s.db.QueryContext(ctx, `
		SELECT COALESCE(u.id, 0), COALESCE(u.name, '(deleted)'),
		       COUNT(*), COALESCE(SUM(r.prompt_tokens),0), COALESCE(SUM(r.completion_tokens),0)
		FROM usage_records r
		LEFT JOIN users u ON u.id = r.user_id
		WHERE r.recorded_at >= ?
		GROUP BY r.user_id
		ORDER BY SUM(r.prompt_tokens + r.completion_tokens) DESC`,
		out.Since)
	if err != nil {
		return nil, err
	}
	for userRows.Next() {
		var u UserUsage
		if err := userRows.Scan(&u.UserID, &u.UserName, &u.Requests, &u.PromptTokens, &u.CompletionTokens); err != nil {
			userRows.Close()
			return nil, err
		}
		out.Users = append(out.Users, u)
	}
	userRows.Close()
	if err := userRows.Err(); err != nil {
		return nil, err
	}

	provRows, err := s.db.QueryContext(ctx, `
		SELECT provider, COUNT(*),
		       COALESCE(SUM(prompt_tokens),0), COALESCE(SUM(completion_tokens),0)
		FROM usage_records
		WHERE recorded_at >= ?
		GROUP BY provider
		ORDER BY SUM(prompt_tokens + completion_tokens) DESC`,
		out.Since)
	if err != nil {
		return nil, err
	}
	defer provRows.Close()
	for provRows.Next() {
		var p ProviderUsage
		if err := provRows.Scan(&p.Provider, &p.Requests, &p.PromptTokens, &p.CompletionTokens); err != nil {
			return nil, err
		}
		out.Providers = append(out.Providers, p)
	}
	return out, provRows.Err()
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

// Avoid an "imported and not used" if database/sql ever gets fully proxied
// through another helper.
var _ sql.NullInt64
