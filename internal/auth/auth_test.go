package auth_test

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"testing"

	"llm-proxy/internal/auth"
	"llm-proxy/internal/store"
)

func TestAuthorizeAcceptsValidKey(t *testing.T) {
	s := openTestStore(t)
	user, err := s.CreateUser(context.Background(), "alice", "alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	issued, err := s.IssueKey(context.Background(), user.ID, "laptop")
	if err != nil {
		t.Fatal(err)
	}

	authn := auth.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req, _ := http.NewRequest(http.MethodGet, "http://proxy/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)

	res, err := authn.Authorize(req)
	if err != nil {
		t.Fatalf("Authorize() = %v, want nil", err)
	}
	if res.UserID != user.ID {
		t.Errorf("UserID = %d, want %d", res.UserID, user.ID)
	}
	if res.KeyID == 0 {
		t.Error("KeyID = 0, want non-zero")
	}
}

func TestAuthorizeRejectsRevokedKey(t *testing.T) {
	s := openTestStore(t)
	user, _ := s.CreateUser(context.Background(), "bob", "")
	issued, _ := s.IssueKey(context.Background(), user.ID, "old")
	if err := s.RevokeKey(context.Background(), issued.TokenPrefix); err != nil {
		t.Fatal(err)
	}

	authn := auth.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req, _ := http.NewRequest(http.MethodGet, "http://proxy/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+issued.Token)

	if _, err := authn.Authorize(req); err == nil {
		t.Fatal("Authorize() = nil, want unauthorized for revoked key")
	}
}

func TestAuthorizeRejectsDisabledUser(t *testing.T) {
	s := openTestStore(t)
	user, _ := s.CreateUser(context.Background(), "carol", "")
	issued, _ := s.IssueKey(context.Background(), user.ID, "k")
	if err := s.SetUserDisabled(context.Background(), user.ID, true); err != nil {
		t.Fatal(err)
	}

	authn := auth.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))
	req, _ := http.NewRequest(http.MethodGet, "http://proxy/openai/v1/models", nil)
	req.Header.Set("x-api-key", issued.Token)

	if _, err := authn.Authorize(req); err == nil {
		t.Fatal("Authorize() = nil, want unauthorized for disabled user")
	}
}

func TestAuthorizeRejectsUnknownToken(t *testing.T) {
	s := openTestStore(t)
	authn := auth.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req, _ := http.NewRequest(http.MethodGet, "http://proxy/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer llmp_ffffffffffffffff_deadbeefdeadbeefdeadbeefdeadbeef")

	if _, err := authn.Authorize(req); err == nil {
		t.Fatal("Authorize() = nil, want unauthorized for unknown key")
	}
}

func TestAuthorizeRejectsEmpty(t *testing.T) {
	s := openTestStore(t)
	authn := auth.New(s, slog.New(slog.NewTextHandler(io.Discard, nil)))

	req, _ := http.NewRequest(http.MethodGet, "http://proxy/openai/v1/models", nil)
	if _, err := authn.Authorize(req); err == nil {
		t.Fatal("Authorize() = nil, want unauthorized for missing token")
	}
}

// openTestStore spins up a fresh SQLite DB in a temp dir. Using a real DB
// (not a mock) ensures the auth layer exercises actual queries and indexes.
func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	s, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}
