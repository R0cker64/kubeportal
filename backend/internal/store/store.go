// Package store persists teams, the group → team mapping and BFF sessions in
// PostgreSQL. Every statement is parameterized (CLAUDE.md rule 13).
package store

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound is returned when a requested row does not exist.
var ErrNotFound = errors.New("not found")

// migrationLockID serializes migrations across processes.
const migrationLockID = 0x6b706f7274616c // "kportal"

// Store wraps a PostgreSQL connection pool. Callers pass contexts with a
// deadline; the store never waits indefinitely on its own.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL and checks the connection.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// The URL contains the password: never wrap the parser error.
		return nil, errors.New("store: invalid database URL")
	}
	cfg.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("store: create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases every connection.
func (s *Store) Close() { s.pool.Close() }

// Ping checks that the database answers.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies the embedded migrations that have not run yet, in order,
// inside a single transaction.
func (s *Store) Migrate(ctx context.Context) error {
	names, err := fs.Glob(migrations, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("store: begin migration: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", migrationLockID); err != nil {
		return fmt.Errorf("store: migration lock: %w", err)
	}
	if _, err := tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    integer PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("store: create schema_migrations: %w", err)
	}

	for _, name := range names {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		var applied bool
		if err := tx.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version,
		).Scan(&applied); err != nil {
			return fmt.Errorf("store: read schema_migrations: %w", err)
		}
		if applied {
			continue
		}
		sql, err := migrations.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sql)); err != nil {
			return fmt.Errorf("store: migration %s: %w", path.Base(name), err)
		}
		if _, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			return fmt.Errorf("store: record migration %s: %w", path.Base(name), err)
		}
	}
	return tx.Commit(ctx)
}

// migrationVersion parses the numeric prefix of "migrations/0001_init.sql".
func migrationVersion(name string) (int, error) {
	prefix, _, ok := strings.Cut(path.Base(name), "_")
	if !ok {
		return 0, fmt.Errorf("store: migration %q has no version prefix", name)
	}
	v, err := strconv.Atoi(prefix)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("store: migration %q has an invalid version", name)
	}
	return v, nil
}

// TeamsForGroups returns the sorted, distinct team names mapped to any of the
// given groups. No groups means no teams.
func (s *Store) TeamsForGroups(ctx context.Context, groups []string) ([]string, error) {
	if len(groups) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT team_name FROM team_groups
		WHERE group_name = ANY($1)
		ORDER BY team_name`, groups)
	if err != nil {
		return nil, fmt.Errorf("store: teams for groups: %w", err)
	}
	teams, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("store: teams for groups: %w", err)
	}
	return teams, nil
}

// Session is a server-side BFF session. IDHash is the SHA-256 of the random
// id carried by the cookie.
type Session struct {
	IDHash     []byte
	Subject    string
	Username   string
	Groups     []string
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
}

// CreateSession inserts a new session.
func (s *Store) CreateSession(ctx context.Context, sess Session) error {
	groups := sess.Groups
	if groups == nil {
		groups = []string{}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO sessions (id_hash, subject, username, groups, created_at, last_seen_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		sess.IDHash, sess.Subject, sess.Username, groups, sess.CreatedAt, sess.LastSeenAt, sess.ExpiresAt)
	if err != nil {
		return fmt.Errorf("store: create session: %w", err)
	}
	return nil
}

// GetSession returns the session with the given id hash, or ErrNotFound.
func (s *Store) GetSession(ctx context.Context, idHash []byte) (Session, error) {
	var sess Session
	err := s.pool.QueryRow(ctx, `
		SELECT id_hash, subject, username, groups, created_at, last_seen_at, expires_at
		FROM sessions WHERE id_hash = $1`, idHash,
	).Scan(&sess.IDHash, &sess.Subject, &sess.Username, &sess.Groups,
		&sess.CreatedAt, &sess.LastSeenAt, &sess.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, fmt.Errorf("store: get session: %w", err)
	}
	return sess, nil
}

// TouchSession records activity on a session.
func (s *Store) TouchSession(ctx context.Context, idHash []byte, at time.Time) error {
	if _, err := s.pool.Exec(ctx,
		"UPDATE sessions SET last_seen_at = $2 WHERE id_hash = $1", idHash, at); err != nil {
		return fmt.Errorf("store: touch session: %w", err)
	}
	return nil
}

// DeleteSession removes a session. Deleting a missing session is not an error.
func (s *Store) DeleteSession(ctx context.Context, idHash []byte) error {
	if _, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE id_hash = $1", idHash); err != nil {
		return fmt.Errorf("store: delete session: %w", err)
	}
	return nil
}

// DeleteExpiredSessions removes sessions past their absolute expiry or idle
// for longer than idle, and returns how many were removed.
func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time, idle time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx,
		"DELETE FROM sessions WHERE expires_at <= $1 OR last_seen_at <= $2", now, now.Add(-idle))
	if err != nil {
		return 0, fmt.Errorf("store: delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
