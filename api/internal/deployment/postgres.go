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

const columns = `id, project_id, status, commit_sha, commit_message, image_tag, container_name, trigger, source_deployment_id, image_pruned, error, created_at, started_at, finished_at`

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Deployment, error) {
	var d Deployment
	// StartedAt / FinishedAt are *time.Time: database/sql sets them to nil for NULL.
	err := row.Scan(&d.ID, &d.ProjectID, &d.Status, &d.CommitSHA, &d.CommitMessage,
		&d.ImageTag, &d.ContainerName, &d.Trigger, &d.SourceID, &d.ImagePruned, &d.Error, &d.CreatedAt, &d.StartedAt, &d.FinishedAt)
	return d, err
}

// Create inserts a queued deployment. The database itself refuses a second
// active deployment (partial unique index) or a missing project (foreign key).
//
// With nd.Source set (rollback/config), the image and commit are copied from
// it, so the worker can skip cloning and building.
func (s *PostgresStore) Create(ctx context.Context, nd NewDeployment) (Deployment, error) {
	trigger := nd.Trigger
	if trigger == "" {
		trigger = TriggerManual
	}
	var sourceID *int64
	var image, sha, message string
	if nd.Source != nil {
		sourceID = &nd.Source.ID
		image, sha, message = nd.Source.ImageTag, nd.Source.CommitSHA, nd.Source.CommitMessage
	}

	d, err := scan(s.db.QueryRowContext(ctx, `
		INSERT INTO deployments (project_id, trigger, source_deployment_id, image_tag, commit_sha, commit_message)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+columns,
		nd.ProjectID, string(trigger), sourceID, image, sha, message))
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
			finished_at = CASE WHEN $2 IN ('live', 'failed') THEN now() ELSE finished_at END
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

func (s *PostgresStore) SetContainer(ctx context.Context, id int64, name string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE deployments SET container_name = $2 WHERE id = $1`, id, name)
	return err
}

// MarkLive swaps the live deployment in ONE transaction, so the project never
// has two live deployments (the partial unique index would refuse it) and
// never ends up with none because of a crash between the two updates.
func (s *PostgresStore) MarkLive(ctx context.Context, projectID, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		UPDATE deployments SET status = 'stopped'
		WHERE project_id = $1 AND status = 'live' AND id <> $2`, projectID, id); err != nil {
		return fmt.Errorf("stop previous live deployment: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE deployments SET status = 'live', finished_at = now()
		WHERE id = $1`, id); err != nil {
		return fmt.Errorf("mark live: %w", err)
	}
	return tx.Commit()
}

func (s *PostgresStore) LiveByProject(ctx context.Context, projectID int64) (Deployment, error) {
	d, err := scan(s.db.QueryRowContext(ctx,
		`SELECT `+columns+` FROM deployments WHERE project_id = $1 AND status = 'live'`, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return Deployment{}, ErrNotFound
	}
	return d, err
}

func (s *PostgresStore) CountLive(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM deployments WHERE status = 'live'`).Scan(&n)
	return n, err
}

// OldImages groups the project's deployments by image tag (several
// deployments can share one image: rollbacks, config restarts), orders the
// tags by their most recent use and skips the newest `keep` of them.
func (s *PostgresStore) OldImages(ctx context.Context, projectID int64, keep int) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT image_tag
		FROM deployments
		WHERE project_id = $1 AND image_tag <> '' AND NOT image_pruned
		GROUP BY image_tag
		HAVING bool_and(status <> 'live')        -- never the image that is live
		ORDER BY max(created_at) DESC
		OFFSET $2`, projectID, keep)
	if err != nil {
		return nil, fmt.Errorf("find old images: %w", err)
	}
	defer rows.Close()

	tags := []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		tags = append(tags, t)
	}
	return tags, rows.Err()
}

func (s *PostgresStore) MarkImagePruned(ctx context.Context, projectID int64, tag string) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE deployments SET image_pruned = true WHERE project_id = $1 AND image_tag = $2`, projectID, tag)
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
		SET status = 'failed', error = 'the server restarted during this deployment', finished_at = now()
		WHERE status IN ('queued', 'cloning', 'building', 'deploying')`)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
