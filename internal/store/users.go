package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

// User is a persisted end-user of the proxy. "End-user" meaning someone an
// admin has issued one or more API keys to — not an admin-UI login account
// (that's governed by config.admin.password_hash instead).
type User struct {
	ID         int64
	Name       string
	Email      string
	CreatedAt  time.Time
	DisabledAt *time.Time
	KeyCount   int // populated by listing queries, 0 elsewhere
}

// CreateUser inserts a new user. Name must be unique (enforced by the DB);
// email is optional. Returns ErrAlreadyExists if the name collides.
func (s *Store) CreateUser(ctx context.Context, name, email string) (*User, error) {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO users (name, email) VALUES (?, NULLIF(?, ''))`,
		name, email)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.GetUser(ctx, id)
}

// GetUser fetches by ID. Returns ErrNotFound if missing.
func (s *Store) GetUser(ctx context.Context, id int64) (*User, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, COALESCE(email, ''), created_at, disabled_at FROM users WHERE id = ?`, id)
	return scanUser(row)
}

// ListUsers returns all users with their non-revoked key counts, ordered
// alphabetically by name.
func (s *Store) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			u.id, u.name, COALESCE(u.email, ''), u.created_at, u.disabled_at,
			(SELECT COUNT(*) FROM api_keys k WHERE k.user_id = u.id AND k.revoked_at IS NULL)
		FROM users u
		ORDER BY u.name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []User
	for rows.Next() {
		var u User
		var disabled sql.NullTime
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt, &disabled, &u.KeyCount); err != nil {
			return nil, err
		}
		if disabled.Valid {
			t := disabled.Time
			u.DisabledAt = &t
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// UpdateUser overwrites the editable fields. Disabling is a separate concern
// handled by SetUserDisabled so a PATCH that just changes the name can't
// accidentally clear disabled_at.
func (s *Store) UpdateUser(ctx context.Context, id int64, name, email string) (*User, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET name = ?, email = NULLIF(?, '') WHERE id = ?`,
		name, email, id)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrAlreadyExists
		}
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, ErrNotFound
	}
	return s.GetUser(ctx, id)
}

// SetUserDisabled toggles the disabled_at flag. Disabling leaves the user's
// API keys intact but the authenticator refuses them (LookupKeyByHash returns
// a "disabled" signal).
func (s *Store) SetUserDisabled(ctx context.Context, id int64, disabled bool) error {
	var q string
	if disabled {
		q = `UPDATE users SET disabled_at = CURRENT_TIMESTAMP WHERE id = ? AND disabled_at IS NULL`
	} else {
		q = `UPDATE users SET disabled_at = NULL WHERE id = ?`
	}
	res, err := s.db.ExecContext(ctx, q, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 && disabled {
		// Verify the user exists (idempotency for already-disabled).
		if _, err := s.GetUser(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func scanUser(row *sql.Row) (*User, error) {
	var u User
	var disabled sql.NullTime
	err := row.Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if disabled.Valid {
		t := disabled.Time
		u.DisabledAt = &t
	}
	return &u, nil
}

// ErrAlreadyExists distinguishes "a conflicting row exists" from generic
// errors so admin handlers can return 409 instead of 500.
var ErrAlreadyExists = errors.New("already exists")

func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	// modernc.org/sqlite wraps the extended error code inside the error
	// message; checking the substring is portable across driver versions
	// without pulling in a driver-specific error type.
	msg := err.Error()
	return strings.Contains(msg, "UNIQUE constraint failed") ||
		strings.Contains(msg, "constraint failed: UNIQUE")
}
