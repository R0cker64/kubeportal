package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"slices"
	"testing"
	"time"
)

// testStore connects to the database in KUBEPORTAL_TEST_DATABASE_URL. The
// tests are skipped when it is not set; CI provides a PostgreSQL service.
// The database must be disposable: the tests drop and recreate the schema.
func testStore(t *testing.T) *Store {
	t.Helper()
	url := os.Getenv("KUBEPORTAL_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("KUBEPORTAL_TEST_DATABASE_URL not set")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	s, err := Open(ctx, url)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.pool.Exec(ctx,
		"DROP TABLE IF EXISTS sessions, team_groups, teams, schema_migrations"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := testStore(t)
	if err := s.Migrate(t.Context()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestTeamsForGroups(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	for _, q := range []string{
		"INSERT INTO teams (name, display_name) VALUES ('payments', 'Payments'), ('catalog', 'Catalog')",
		"INSERT INTO team_groups (team_name, group_name) VALUES ('payments', 'team-payments'), " +
			"('catalog', 'team-catalog'), ('payments', 'shared'), ('catalog', 'shared')",
	} {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		groups []string
		want   []string
	}{
		{nil, nil},
		{[]string{"unknown"}, nil},
		{[]string{"team-payments"}, []string{"payments"}},
		{[]string{"shared", "team-payments"}, []string{"catalog", "payments"}},
		// Group names are matched exactly, never as patterns.
		{[]string{"team-%"}, nil},
	}
	for _, c := range cases {
		got, err := s.TeamsForGroups(ctx, c.groups)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, c.want) {
			t.Errorf("TeamsForGroups(%v) = %v, want %v", c.groups, got, c.want)
		}
	}
}

func TestTeamNameConstraint(t *testing.T) {
	s := testStore(t)
	for _, name := range []string{"Payments", "-x", "a b", `x"}`} {
		if _, err := s.pool.Exec(t.Context(),
			"INSERT INTO teams (name, display_name) VALUES ($1, 'x')", name); err == nil {
			t.Errorf("team name %q was accepted", name)
		}
	}
}

func TestSessionLifecycle(t *testing.T) {
	s := testStore(t)
	ctx := t.Context()
	hash := sha256.Sum256([]byte("session-id"))
	now := time.Now().UTC().Truncate(time.Microsecond)

	sess := Session{
		IDHash: hash[:], Subject: "sub-1", Username: "alice", Groups: []string{"team-payments"},
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSession(ctx, hash[:])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.IDHash, hash[:]) || got.Username != "alice" || !slices.Equal(got.Groups, sess.Groups) {
		t.Fatalf("unexpected session %+v", got)
	}

	later := now.Add(10 * time.Minute)
	if err := s.TouchSession(ctx, hash[:], later); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetSession(ctx, hash[:]); !got.LastSeenAt.Equal(later) {
		t.Fatalf("LastSeenAt = %v, want %v", got.LastSeenAt, later)
	}

	if n, err := s.DeleteExpiredSessions(ctx, now.Add(2*time.Hour), time.Hour); err != nil || n != 1 {
		t.Fatalf("DeleteExpiredSessions = %d, %v", n, err)
	}
	if _, err := s.GetSession(ctx, hash[:]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := s.DeleteSession(ctx, hash[:]); err != nil {
		t.Fatalf("deleting a missing session: %v", err)
	}
}

func TestMigrationVersion(t *testing.T) {
	if v, err := migrationVersion("migrations/0012_x.sql"); err != nil || v != 12 {
		t.Fatalf("got %d, %v", v, err)
	}
	for _, bad := range []string{"migrations/init.sql", "migrations/x_init.sql", "migrations/0000_x.sql"} {
		if _, err := migrationVersion(bad); err == nil {
			t.Errorf("%s: expected error", bad)
		}
	}
}
