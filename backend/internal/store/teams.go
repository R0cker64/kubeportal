package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrConflict is returned when creating a row that already exists.
var ErrConflict = errors.New("already exists")

// Team is a team and the identity-provider groups mapped to it. Callers
// validate the fields; the schema enforces the same rules as a last line.
type Team struct {
	Name        string
	DisplayName string
	Groups      []string // sorted
	CreatedAt   time.Time
}

const selectTeams = `
	SELECT t.name, t.display_name, t.created_at,
	       COALESCE(array_agg(g.group_name ORDER BY g.group_name)
	                FILTER (WHERE g.group_name IS NOT NULL), '{}')
	FROM teams t
	LEFT JOIN team_groups g ON g.team_name = t.name`

func scanTeam(row pgx.CollectableRow) (Team, error) {
	var t Team
	err := row.Scan(&t.Name, &t.DisplayName, &t.CreatedAt, &t.Groups)
	return t, err
}

// ListTeams returns every team with its groups, sorted by name.
func (s *Store) ListTeams(ctx context.Context) ([]Team, error) {
	rows, err := s.pool.Query(ctx, selectTeams+" GROUP BY t.name ORDER BY t.name")
	if err != nil {
		return nil, fmt.Errorf("store: list teams: %w", err)
	}
	teams, err := pgx.CollectRows(rows, scanTeam)
	if err != nil {
		return nil, fmt.Errorf("store: list teams: %w", err)
	}
	return teams, nil
}

// GetTeam returns one team, or ErrNotFound.
func (s *Store) GetTeam(ctx context.Context, name string) (Team, error) {
	rows, err := s.pool.Query(ctx, selectTeams+" WHERE t.name = $1 GROUP BY t.name", name)
	if err != nil {
		return Team{}, fmt.Errorf("store: get team: %w", err)
	}
	t, err := pgx.CollectExactlyOneRow(rows, scanTeam)
	if errors.Is(err, pgx.ErrNoRows) {
		return Team{}, ErrNotFound
	}
	if err != nil {
		return Team{}, fmt.Errorf("store: get team: %w", err)
	}
	return t, nil
}

// CreateTeam inserts a team and its groups atomically. It returns
// ErrConflict if a team with the same name exists.
func (s *Store) CreateTeam(ctx context.Context, t Team) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			"INSERT INTO teams (name, display_name) VALUES ($1, $2)", t.Name, t.DisplayName); err != nil {
			if isUniqueViolation(err) {
				return ErrConflict
			}
			return fmt.Errorf("store: create team: %w", err)
		}
		return replaceGroups(ctx, tx, t.Name, t.Groups)
	})
}

// UpdateTeam replaces the display name and the groups of an existing team,
// or returns ErrNotFound.
func (s *Store) UpdateTeam(ctx context.Context, t Team) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			"UPDATE teams SET display_name = $2 WHERE name = $1", t.Name, t.DisplayName)
		if err != nil {
			return fmt.Errorf("store: update team: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		return replaceGroups(ctx, tx, t.Name, t.Groups)
	})
}

// DeleteTeam removes a team and its group mapping, or returns ErrNotFound.
func (s *Store) DeleteTeam(ctx context.Context, name string) error {
	tag, err := s.pool.Exec(ctx, "DELETE FROM teams WHERE name = $1", name)
	if err != nil {
		return fmt.Errorf("store: delete team: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func replaceGroups(ctx context.Context, tx pgx.Tx, team string, groups []string) error {
	if _, err := tx.Exec(ctx, "DELETE FROM team_groups WHERE team_name = $1", team); err != nil {
		return fmt.Errorf("store: clear team groups: %w", err)
	}
	if len(groups) == 0 {
		return nil
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO team_groups (team_name, group_name)
		SELECT $1, g FROM unnest($2::text[]) AS g
		ON CONFLICT DO NOTHING`, team, groups); err != nil {
		return fmt.Errorf("store: set team groups: %w", err)
	}
	return nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
