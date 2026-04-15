package auth

import (
	"errors"
	"net/http"
	"strings"
)

var ErrUnauthorized = errors.New("unauthorized")

type Authenticator struct {
	tokens map[string]struct{}
}

func New(tokens []string) *Authenticator {
	allowed := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		token = strings.TrimSpace(token)
		if token == "" {
			continue
		}
		allowed[token] = struct{}{}
	}
	return &Authenticator{tokens: allowed}
}

func (a *Authenticator) Authorize(r *http.Request, _ string) error {
	token := extractToken(r)
	if token == "" {
		return ErrUnauthorized
	}
	if _, ok := a.tokens[token]; !ok {
		return ErrUnauthorized
	}
	return nil
}

func extractToken(r *http.Request) string {
	if header := strings.TrimSpace(r.Header.Get("Authorization")); header != "" {
		if strings.HasPrefix(strings.ToLower(header), "bearer ") {
			return strings.TrimSpace(header[7:])
		}
	}
	return strings.TrimSpace(r.Header.Get("x-api-key"))
}
