package project

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
)

// PostgresStore implements Store with plain SQL through Go's standard
// database/sql package (no ORM).
//
// WHY no ORM: every query is visible and easy to explain in an interview,
// and there is no hidden N+1 query problem.
type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

// Postgres error code for a UNIQUE constraint violation.
const uniqueViolation = "23505"

const projectColumns = `id, name, repo_url, branch, subdomain, created_at, updated_at`

// scanner is satisfied by both *sql.Row and *sql.Rows, so one function can
// read a project from either.
type scanner interface {
	Scan(dest ...any) error
}

// scanProject reads one row's columns, in projectColumns order, into a Project.
func scanProject(row scanner) (Project, error) {
	var p Project
	err := row.Scan(&p.ID, &p.Name, &p.RepoURL, &p.Branch, &p.Subdomain, &p.CreatedAt, &p.UpdatedAt)
	return p, err
}

func (s *PostgresStore) List(ctx context.Context) ([]Project, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close() // always close, or the connection never returns to the pool

	projects := []Project{} // empty slice, not nil, so the JSON is [] and not null
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *PostgresStore) Get(ctx context.Context, id int64) (Project, error) {
	p, err := scanProject(s.db.QueryRowContext(ctx, `SELECT `+projectColumns+` FROM projects WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("get project: %w", err)
	}

	p.EnvVars, err = s.envVars(ctx, id)
	return p, err
}

// Create inserts the project and its env vars in ONE transaction: if saving
// the env vars fails, the project row is rolled back too, so we never keep a
// half-saved project.
func (s *PostgresStore) Create(ctx context.Context, in Input) (Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback() // no-op once Commit has succeeded

	// $1, $2... are bind parameters: values are sent separately from the SQL
	// text, which is what prevents SQL injection.
	p, err := scanProject(tx.QueryRowContext(ctx, `
		INSERT INTO projects (name, repo_url, branch, subdomain)
		VALUES ($1, $2, $3, $4)
		RETURNING `+projectColumns,
		in.Name, in.RepoURL, in.Branch, in.Subdomain))
	if err != nil {
		return Project{}, mapWriteError(err)
	}

	if err := replaceEnvVars(ctx, tx, p.ID, in.EnvVars); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	p.EnvVars = in.EnvVars
	return p, nil
}

// Update replaces every field and the full set of env vars (PUT semantics).
func (s *PostgresStore) Update(ctx context.Context, id int64, in Input) (Project, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Project{}, err
	}
	defer tx.Rollback()

	p, err := scanProject(tx.QueryRowContext(ctx, `
		UPDATE projects
		SET name = $2, repo_url = $3, branch = $4, subdomain = $5, updated_at = now()
		WHERE id = $1
		RETURNING `+projectColumns,
		id, in.Name, in.RepoURL, in.Branch, in.Subdomain))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, mapWriteError(err)
	}

	if err := replaceEnvVars(ctx, tx, id, in.EnvVars); err != nil {
		return Project{}, err
	}
	if err := tx.Commit(); err != nil {
		return Project{}, err
	}
	p.EnvVars = in.EnvVars
	return p, nil
}

// Delete removes the project; ON DELETE CASCADE removes its env vars.
func (s *PostgresStore) Delete(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM projects WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete project: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) envVars(ctx context.Context, projectID int64) (map[string]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT key, value FROM env_vars WHERE project_id = $1`, projectID)
	if err != nil {
		return nil, fmt.Errorf("load env vars: %w", err)
	}
	defer rows.Close()

	vars := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		vars[k] = v
	}
	return vars, rows.Err()
}

// replaceEnvVars deletes the project's env vars and inserts the new set.
// Simpler than working out which keys were added, changed or removed.
func replaceEnvVars(ctx context.Context, tx *sql.Tx, projectID int64, vars map[string]string) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM env_vars WHERE project_id = $1`, projectID); err != nil {
		return fmt.Errorf("clear env vars: %w", err)
	}
	for k, v := range vars {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO env_vars (project_id, key, value) VALUES ($1, $2, $3)`, projectID, k, v); err != nil {
			return fmt.Errorf("save env var %s: %w", k, err)
		}
	}
	return nil
}

// mapWriteError turns a Postgres unique-violation into ErrSubdomainTaken
// (subdomain is the only UNIQUE column we write).
func mapWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return ErrSubdomainTaken
	}
	return fmt.Errorf("save project: %w", err)
}
