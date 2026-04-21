// Package store is the persistence layer for llm-proxy's multi-tenant state:
// users, API keys, and per-request usage records. It wraps a single SQLite
// database file opened via the pure-Go modernc.org/sqlite driver so the
// proxy keeps its single-binary, no-cgo deployment story.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

//go:embed migrations
var migrationsFS embed.FS

// Store is the database handle. It is safe for concurrent use — all methods
// take a context and forward to database/sql's connection pool.
type Store struct {
	db *sql.DB
}

// Open connects to the SQLite database at path (creating the file if needed),
// applies PRAGMAs tuned for a write-heavy mixed workload, and runs any
// pending migrations.
func Open(ctx context.Context, dbPath string) (*Store, error) {
	// Query params tell the driver to open in rwc mode and enable foreign
	// keys at the connection level; the additional PRAGMAs below tune WAL
	// durability trade-offs for a mostly-local, single-writer workload.
	dsn := dbPath + "?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// SQLite handles multiple readers with WAL but only one writer at a
	// time. Capping open conns avoids lock contention surfacing as busy
	// retries under bursty admin-API write traffic.
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(2)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}

	s := &Store{db: db}
	if err := s.migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

// DB exposes the underlying *sql.DB for code that needs ad-hoc queries (tests
// or future analytics endpoints). Keep uses narrow: this is an escape hatch,
// not an architectural boundary.
func (s *Store) DB() *sql.DB { return s.db }

// Close releases the database handle. Safe to call with nil receiver so
// cleanup paths don't need to guard.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return err
	}

	// Migrations are named NNN_description.sql. Sort by leading integer so
	// out-of-order filenames (e.g. 10 vs 2) apply correctly.
	sort.Slice(entries, func(i, j int) bool {
		return migrationVersion(entries[i].Name()) < migrationVersion(entries[j].Name())
	})

	// Ensure the bookkeeping table exists before we ask it anything. Safe to
	// run on every open because CREATE TABLE IF NOT EXISTS is a no-op once it
	// succeeds.
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    INTEGER PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return err
	}

	applied := map[int]bool{}
	rows, err := s.db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()

	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		version := migrationVersion(e.Name())
		if version < 0 || applied[version] {
			continue
		}
		sqlBytes, err := fs.ReadFile(migrationsFS, path.Join("migrations", e.Name()))
		if err != nil {
			return err
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply %s: %w", e.Name(), err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version) VALUES (?)`, version); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record %s: %w", e.Name(), err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func migrationVersion(name string) int {
	// "001_init.sql" → 1; unparseable names return -1 so they're skipped.
	underscore := strings.IndexByte(name, '_')
	if underscore <= 0 {
		return -1
	}
	v, err := strconv.Atoi(name[:underscore])
	if err != nil {
		return -1
	}
	return v
}

// ErrNotFound is returned by lookups that find no matching row. Callers
// translate this to 404 for HTTP clients.
var ErrNotFound = errors.New("not found")
