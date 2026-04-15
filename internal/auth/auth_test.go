package auth

import (
	"net/http"
	"testing"
)

func TestAuthenticatorAcceptsBearerToken(t *testing.T) {
	authenticator := New([]string{"proxy-token"})

	req, err := http.NewRequest(http.MethodGet, "http://proxy.local/openai/v1/models", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer proxy-token")

	if err := authenticator.Authorize(req, "openai-main"); err != nil {
		t.Fatalf("Authorize() error = %v, want nil", err)
	}
}

func TestAuthenticatorAcceptsXAPIKey(t *testing.T) {
	authenticator := New([]string{"proxy-token"})

	req, err := http.NewRequest(http.MethodGet, "http://proxy.local/anthropic/v1/messages", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("x-api-key", "proxy-token")

	if err := authenticator.Authorize(req, "claude-main"); err != nil {
		t.Fatalf("Authorize() error = %v, want nil", err)
	}
}

func TestAuthenticatorRejectsMissingToken(t *testing.T) {
	authenticator := New([]string{"proxy-token"})

	req, err := http.NewRequest(http.MethodGet, "http://proxy.local/openai/v1/models", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	if err := authenticator.Authorize(req, "openai-main"); err == nil {
		t.Fatal("Authorize() error = nil, want unauthorized error")
	}
}

func TestAuthenticatorRejectsUnknownToken(t *testing.T) {
	authenticator := New([]string{"proxy-token"})

	req, err := http.NewRequest(http.MethodGet, "http://proxy.local/openai/v1/models", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer wrong-token")

	if err := authenticator.Authorize(req, "openai-main"); err == nil {
		t.Fatal("Authorize() error = nil, want unauthorized error")
	}
}
