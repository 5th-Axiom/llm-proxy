package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

// KeyTokenPrefix is the fixed brand segment that every issued token starts
// with. Clients can grep logs / history for `llmp_` to find one quickly.
const KeyTokenPrefix = "llmp_"

// visiblePrefixHexLen is the number of hex characters shown after `llmp_` in
// list views and used to uniquely identify a key in the URL. Combined with
// KeyTokenPrefix it forms the token_prefix stored in DB.
const visiblePrefixHexLen = 16

// secretHexLen is the random-body length in hex characters appended after the
// visible prefix. Together that's 16+32 = 48 hex chars = 192 bits of entropy
// in the secret portion alone — plenty for an API key.
const secretHexLen = 32

// APIKey is a row in the api_keys table. The raw token is only resurfaced
// through GetKeyPlaintext; list queries expose only metadata plus a boolean
// flag (PlaintextPresent) so UIs can hide the "view" button for rows that
// predate plaintext storage.
type APIKey struct {
	ID               int64
	UserID           int64
	Name             string
	TokenPrefix      string // "llmp_<16hex>"
	CreatedAt        time.Time
	LastUsedAt       *time.Time
	RevokedAt        *time.Time
	PlaintextPresent bool
}

// IssuedKey wraps a newly-minted key with its plaintext token. The caller
// must hand the token to the user immediately; it cannot be recovered later.
type IssuedKey struct {
	APIKey
	Token string // "llmp_<16hex>_<32hex>" — show once, never log
}

// IssueKey generates a fresh random token, stores its SHA-256 hash + prefix
// under userID, and returns the plaintext in IssuedKey.Token. The caller is
// responsible for showing it to the user exactly once.
func (s *Store) IssueKey(ctx context.Context, userID int64, name string) (*IssuedKey, error) {
	prefixBytes := make([]byte, visiblePrefixHexLen/2)
	if _, err := rand.Read(prefixBytes); err != nil {
		return nil, fmt.Errorf("generate prefix: %w", err)
	}
	secretBytes := make([]byte, secretHexLen/2)
	if _, err := rand.Read(secretBytes); err != nil {
		return nil, fmt.Errorf("generate secret: %w", err)
	}

	visiblePrefix := KeyTokenPrefix + hex.EncodeToString(prefixBytes)
	token := visiblePrefix + "_" + hex.EncodeToString(secretBytes)
	hash := sha256.Sum256([]byte(token))

	// token_plaintext is stored so admins can re-copy a key from the UI
	// after the initial "show once" dialog is dismissed. See migration
	// 002_api_key_plaintext.sql for the security trade-off.
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO api_keys (user_id, name, token_hash, token_prefix, token_plaintext)
		VALUES (?, ?, ?, ?, ?)`,
		userID, name, hash[:], visiblePrefix, token)
	if err != nil {
		if isUniqueViolation(err) {
			// Prefix collision is astronomically unlikely (64 bits) but
			// retry once before giving up so we never surface a
			// cryptographically-rare error to the user.
			return s.IssueKey(ctx, userID, name)
		}
		return nil, err
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	key, err := s.getKey(ctx, id)
	if err != nil {
		return nil, err
	}
	return &IssuedKey{APIKey: *key, Token: token}, nil
}

// ListKeysForUser returns every key (active + revoked) issued to the user,
// most-recently-created first. Revoked keys show up so admins can see the
// audit trail; they cannot be reused for auth.
func (s *Store) ListKeysForUser(ctx context.Context, userID int64) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, user_id, name, token_prefix, created_at, last_used_at, revoked_at,
		       token_plaintext IS NOT NULL AND token_plaintext <> ''
		FROM api_keys
		WHERE user_id = ?
		ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []APIKey
	for rows.Next() {
		var k APIKey
		var lastUsed, revoked sql.NullTime
		if err := rows.Scan(&k.ID, &k.UserID, &k.Name, &k.TokenPrefix,
			&k.CreatedAt, &lastUsed, &revoked, &k.PlaintextPresent); err != nil {
			return nil, err
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			k.LastUsedAt = &t
		}
		if revoked.Valid {
			t := revoked.Time
			k.RevokedAt = &t
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// ErrKeyPlaintextUnavailable is returned when a key row exists but its
// plaintext was never stored — specifically, rows that survived from before
// migration 002 introduced the token_plaintext column.
var ErrKeyPlaintextUnavailable = errors.New("key plaintext unavailable")

// GetKeyPlaintext looks up the stored plaintext for a key identified by
// its visible prefix. Returns ErrNotFound for unknown prefixes and
// ErrKeyPlaintextUnavailable when the row predates plaintext storage.
func (s *Store) GetKeyPlaintext(ctx context.Context, prefix string) (string, error) {
	if !strings.HasPrefix(prefix, KeyTokenPrefix) {
		return "", ErrNotFound
	}
	var plaintext sql.NullString
	row := s.db.QueryRowContext(ctx,
		`SELECT token_plaintext FROM api_keys WHERE token_prefix = ?`, prefix)
	if err := row.Scan(&plaintext); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", err
	}
	if !plaintext.Valid || plaintext.String == "" {
		return "", ErrKeyPlaintextUnavailable
	}
	return plaintext.String, nil
}

// ErrKeyNotRevoked is returned when DeleteKey is called on a key that is
// still active. We require revoke-then-delete as a small safety rail: a
// mis-click in the UI should still leave one confirm step between an
// operational key and oblivion.
var ErrKeyNotRevoked = errors.New("key must be revoked before it can be deleted")

// DeleteKey hard-deletes an already-revoked key row. Historical usage
// records reference the key via ON DELETE SET NULL, so per-user roll-ups
// stay intact after deletion; we only lose the ability to attribute a row
// back to the specific key that produced it.
func (s *Store) DeleteKey(ctx context.Context, prefix string) error {
	if !strings.HasPrefix(prefix, KeyTokenPrefix) {
		return ErrNotFound
	}
	var revokedAt sql.NullTime
	row := s.db.QueryRowContext(ctx,
		`SELECT revoked_at FROM api_keys WHERE token_prefix = ?`, prefix)
	if err := row.Scan(&revokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if !revokedAt.Valid {
		return ErrKeyNotRevoked
	}
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM api_keys WHERE token_prefix = ?`, prefix); err != nil {
		return err
	}
	return nil
}

// RevokeKey marks the key identified by its visible prefix as revoked. Idempotent —
// re-revoking a revoked key is a no-op that does not error.
func (s *Store) RevokeKey(ctx context.Context, prefix string) error {
	if !strings.HasPrefix(prefix, KeyTokenPrefix) {
		return ErrNotFound
	}
	res, err := s.db.ExecContext(ctx, `
		UPDATE api_keys SET revoked_at = CURRENT_TIMESTAMP
		WHERE token_prefix = ? AND revoked_at IS NULL`, prefix)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		// No active key with that prefix. Distinguish "never existed" from
		// "already revoked" so the API can return 404 only for the former.
		var exists int
		row := s.db.QueryRowContext(ctx, `SELECT 1 FROM api_keys WHERE token_prefix = ?`, prefix)
		if err := row.Scan(&exists); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		// key exists, already revoked → treat as success (idempotent)
	}
	return nil
}

// KeyLookup is the minimal payload the authenticator needs to authorise a
// request. It avoids shipping around full APIKey structs in the hot path.
type KeyLookup struct {
	UserID       int64
	KeyID        int64
	UserDisabled bool
	KeyRevoked   bool
}

// LookupKeyByToken hashes the plaintext token and looks it up. Returns
// ErrNotFound if there is no row; the caller must translate any error as 401
// to avoid leaking whether a token exists-but-was-revoked vs never-existed.
func (s *Store) LookupKeyByToken(ctx context.Context, token string) (*KeyLookup, error) {
	hash := sha256.Sum256([]byte(token))
	row := s.db.QueryRowContext(ctx, `
		SELECT k.id, k.user_id, k.revoked_at IS NOT NULL, u.disabled_at IS NOT NULL
		FROM api_keys k JOIN users u ON u.id = k.user_id
		WHERE k.token_hash = ?`, hash[:])
	var l KeyLookup
	err := row.Scan(&l.KeyID, &l.UserID, &l.KeyRevoked, &l.UserDisabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// TouchKeyLastUsed updates last_used_at to now. Callers should throttle — see
// auth.Authenticator.Authorize — so this isn't hammered on every request.
func (s *Store) TouchKeyLastUsed(ctx context.Context, keyID int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET last_used_at = CURRENT_TIMESTAMP WHERE id = ?`, keyID)
	return err
}

func (s *Store) getKey(ctx context.Context, id int64) (*APIKey, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, name, token_prefix, created_at, last_used_at, revoked_at,
		       token_plaintext IS NOT NULL AND token_plaintext <> ''
		FROM api_keys WHERE id = ?`, id)
	var k APIKey
	var lastUsed, revoked sql.NullTime
	err := row.Scan(&k.ID, &k.UserID, &k.Name, &k.TokenPrefix,
		&k.CreatedAt, &lastUsed, &revoked, &k.PlaintextPresent)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if lastUsed.Valid {
		t := lastUsed.Time
		k.LastUsedAt = &t
	}
	if revoked.Valid {
		t := revoked.Time
		k.RevokedAt = &t
	}
	return &k, nil
}
