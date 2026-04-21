package store_test

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"llm-proxy/internal/store"
)

func TestUsageInsertAndSummary(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	alice, _ := s.CreateUser(ctx, "alice", "")
	bob, _ := s.CreateUser(ctx, "bob", "")
	aliceKey, _ := s.IssueKey(ctx, alice.ID, "k")
	bobKey, _ := s.IssueKey(ctx, bob.ID, "k")

	rows := []store.UsageRecord{
		{UserID: alice.ID, KeyID: aliceKey.ID, Provider: "openai", Model: "gpt-4.1", Status: 200, PromptTokens: 100, CompletionTokens: 200, DurationMs: 500},
		{UserID: alice.ID, KeyID: aliceKey.ID, Provider: "anthropic", Model: "claude", Status: 200, PromptTokens: 50, CompletionTokens: 80, DurationMs: 300},
		{UserID: bob.ID, KeyID: bobKey.ID, Provider: "openai", Model: "gpt-4.1", Status: 200, PromptTokens: 10, CompletionTokens: 20, DurationMs: 150},
	}
	for _, r := range rows {
		if err := s.InsertUsage(ctx, r); err != nil {
			t.Fatalf("InsertUsage: %v", err)
		}
	}

	summary, err := s.GetUsageSummary(ctx, time.Time{})
	if err != nil {
		t.Fatalf("GetUsageSummary: %v", err)
	}
	if len(summary.Users) != 2 {
		t.Fatalf("users len = %d, want 2", len(summary.Users))
	}
	// alice should be first (more tokens: 430 vs 30).
	if summary.Users[0].UserName != "alice" {
		t.Errorf("top user = %q, want alice", summary.Users[0].UserName)
	}
	if summary.Users[0].PromptTokens != 150 || summary.Users[0].CompletionTokens != 280 {
		t.Errorf("alice counts = %+v, want prompt=150 completion=280", summary.Users[0])
	}

	// provider roll-up: openai has 3 rows (110+20 prompt), anthropic has 1.
	providerByName := map[string]store.ProviderUsage{}
	for _, p := range summary.Providers {
		providerByName[p.Provider] = p
	}
	if providerByName["openai"].Requests != 2 || providerByName["openai"].PromptTokens != 110 {
		t.Errorf("openai usage = %+v", providerByName["openai"])
	}
	if providerByName["anthropic"].Requests != 1 {
		t.Errorf("anthropic usage = %+v", providerByName["anthropic"])
	}
}

func TestUsagePurgeOlderThan(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "u", "")
	k, _ := s.IssueKey(ctx, u.ID, "k")

	now := time.Now()
	old := store.UsageRecord{
		UserID: u.ID, KeyID: k.ID, Provider: "p", Status: 200,
		RecordedAt: now.Add(-120 * 24 * time.Hour),
	}
	recent := store.UsageRecord{
		UserID: u.ID, KeyID: k.ID, Provider: "p", Status: 200,
		RecordedAt: now.Add(-10 * 24 * time.Hour),
	}
	if err := s.InsertUsage(ctx, old); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertUsage(ctx, recent); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.PurgeUsageOlderThan(ctx, now.Add(-90*24*time.Hour))
	if err != nil {
		t.Fatalf("PurgeUsageOlderThan: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (the 120d-old row)", deleted)
	}

	summary, _ := s.GetUsageSummary(ctx, time.Time{})
	if len(summary.Users) != 1 || summary.Users[0].Requests != 1 {
		t.Errorf("post-purge requests = %+v, want single remaining row", summary.Users)
	}
}

// TestUsageRecordedAtHonoured verifies that callers can override the default
// DB-supplied timestamp for tests / imports of historical data.
func TestUsageRecordedAtHonoured(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	u, _ := s.CreateUser(ctx, "u", "")
	k, _ := s.IssueKey(ctx, u.ID, "k")

	at := time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC)
	err := s.InsertUsage(ctx, store.UsageRecord{
		UserID: u.ID, KeyID: k.ID, Provider: "p", Status: 200,
		RecordedAt: at,
	})
	if err != nil {
		t.Fatalf("InsertUsage: %v", err)
	}

	summary, _ := s.GetUsageSummary(ctx, at.Add(-time.Hour))
	if len(summary.Users) != 1 || summary.Users[0].Requests != 1 {
		t.Fatalf("summary since at-1h missed the row: %+v", summary.Users)
	}
	summary, _ = s.GetUsageSummary(ctx, at.Add(time.Hour))
	if len(summary.Users) != 0 {
		t.Fatalf("summary since at+1h should exclude the row, got %+v", summary.Users)
	}
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	p := filepath.Join(t.TempDir(), "store.db")
	s, err := store.Open(context.Background(), p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
