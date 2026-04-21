package admin

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// sessionStore is an in-memory token → expiry map guarded by a mutex. Single
// process, no persistence; losing it on restart simply forces re-login.
type sessionStore struct {
	mu       sync.Mutex
	sessions map[string]time.Time
	ttl      time.Duration
}

func newSessionStore(ttl time.Duration) *sessionStore {
	if ttl <= 0 {
		ttl = 12 * time.Hour
	}
	return &sessionStore{
		sessions: map[string]time.Time{},
		ttl:      ttl,
	}
}

// Create issues a fresh 256-bit session token.
func (s *sessionStore) Create() string {
	token := randToken()
	s.mu.Lock()
	s.sessions[token] = time.Now().Add(s.ttl)
	s.mu.Unlock()
	return token
}

// Valid reports whether token names a non-expired session. Expired entries are
// pruned opportunistically on the failing lookup.
func (s *sessionStore) Valid(token string) bool {
	if token == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.sessions[token]
	if !ok {
		return false
	}
	if time.Now().After(exp) {
		delete(s.sessions, token)
		return false
	}
	return true
}

func (s *sessionStore) Delete(token string) {
	if token == "" {
		return
	}
	s.mu.Lock()
	delete(s.sessions, token)
	s.mu.Unlock()
}

func randToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
