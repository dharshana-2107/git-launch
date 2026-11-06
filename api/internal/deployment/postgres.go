package deployment

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"launchpad/api/internal/db"
)

type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(db *sql.DB) *PostgresStore {
	return &PostgresStore{db: db}
}

const columns = `id, project_id, status, commit_sha, commit_message, image_tag, error, created_at, started_at, finished_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Deployment, error) {
	var d Deployment
	// StartedAt / FinishedAt are *time.Time: database/sql sets them to nil for NULL.
	err := row.Scan(&d.ID, &d.ProjectID, &d.Status, &d.CommitSHA, &d.CommitMessage,
		&d.ImageTag, &d.Error, &d.CreatedAt, &d.StartedAt, &d.FinishedAt)
	return d, err
}

// Create inserts a queued deployment. The database itself refuses a second
// active deployment (partial unique index) or a missing project (foreign key).
func (s *PostgresStore) Create(ctx context.Context, projectID int64) (Deployment, error) {
	d, err := scan(s.db.QueryRowContext(ctx,
		`INSERT INTO deployments (project_id) VALUES ($1) RETURNING `+columns, projectID))
	switch db.SQLState(err) {
	case db.CodeUniqueViolation:
		return Deployment{}, ErrAlreadyActive
	case db.CodeForeignKeyViolation:
		return Deployment{}, ErrProjectNotFound
	}
	if err != nil {
		return Deployment{}, fmt.Errorf("create deployment: %w", err)
	}
	return d, nil
}

func (s *PostgresStore) Get(ctx context.Context, id int64) (Deployment, error) {
	d, err := scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM deployments WHERE id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	return d, err
}

func (s *PostgresStore) ListByProject(ctx context.Context, projectID int64, limit int) ([]Deployment, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+columns+` FROM deployments
		WHERE project_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT $2`, projectID, limit)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()

	list := []Deployment{}
	for rows.Next() {
		d, err := scan(rows)
		if err != nil {
			return nil, err
		}
		list = append(list, d)
	}
	return list, rows.Err()
}

// SetStatus also stamps started_at the first time work begins and
// finished_at when the status is final.
func (s *PostgresStore) SetStatus(ctx context.Context, id int64, status Status) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE deployments SET
			status      = $2,
			started_at  = CASE WHEN $2 <> 'queued' THEN COALESCE(started_at, now()) ELSE started_at END,
			finished_at = CASE WHEN $2 IN ('built', 'failed') THEN now() ELSE finished_at END
		WHERE id = $1`, id, string(status))
	return err
}

func (s *PostgresStore) SetCommit(ctx context.Context, id int64, sha, message string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deployments SET commit_sha = $2, commit_message = $3 WHERE id = $1`, id, sha, message)
	return err
}

func (s *PostgresStore) SetImage(ctx context.Context, id int64, tag string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deployments SET image_tag = $2 WHERE id = $1`, id, tag)
	return err
}

func (s *PostgresStore) Fail(ctx context.Context, id int64, reason string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE deployments
		SET status = 'failed', error = $2, finished_at = now(), started_at = COALESCE(started_at, now())
		WHERE id = $1`, id, reason)
	return err
}

func (s *PostgresStore) AppendLog(ctx context.Context, id int64, line int, text string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO deployment_logs (deployment_id, line_no, text) VALUES ($1, $2, $3)`, id, line, text)
	return err
}

// Logs returns the lines after afterLine (0 = from the start), in order.
func (s *PostgresStore) Logs(ctx context.Context, id int64, afterLine int) ([]LogLine, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT line_no, text FROM deployment_logs
		WHERE deployment_id = $1 AND line_no > $2
		ORDER BY line_no`, id, afterLine)
	if err != nil {
		return nil, fmt.Errorf("load logs: %w", err)
	}
	defer rows.Close()

	lines := []LogLine{}
	for rows.Next() {
		var l LogLine
		if err := rows.Scan(&l.Line, &l.Text); err != nil {
			return nil, err
		}
		lines = append(lines, l)
	}
	return lines, rows.Err()
}

func (s *PostgresStore) FailUnfinished(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		UPDATE deployments
		SET status = 'failed', error = 'the server restarted during this build', finished_at = now()
		WHERE status IN ('queued', 'cloning', 'building')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
