// Package db opens the Postgres connection pool and applies schema migrations.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	// The blank import registers the "postgres" driver with database/sql.
	// Our code only ever talks to the standard database/sql API.
	_ "github.com/jackc/pgx/v5/stdlib"
)

// The SQL files are compiled INTO the binary with go:embed, so the server
// never depends on files sitting next to it at runtime.
//
//go:embed migrations/*.sql
var migrationFiles embed.FS

// Connect opens a pool of reusable connections and checks the database answers.
//
// WHY a pool: opening a Postgres connection is slow (TCP + auth). *sql.DB is
// itself a pool: it keeps a few connections open, lends one to each query,
// and is safe to share between goroutines (every HTTP request runs in its own).
func Connect(ctx context.Context, url string) (*sql.DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(10)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)

	// sql.Open does not connect yet, so ping to fail fast on a bad URL.
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := db.PingContext(pingCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return db, nil
}

// Migrate applies every migrations/NNN_name.sql file not yet recorded in the
// schema_migrations table, in file-name order, each in its own transaction.
//
// WHY migrations: the schema lives in version control and every machine ends up
// with the same tables. A file is never edited after it has run; a change is a
// new file (002_..., 003_...).
func Migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	names, err := fs.Glob(migrationFiles, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(names) // 001 before 002 before 010

	for _, name := range names {
		var exists bool
		err := db.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists)
		if err != nil {
			return err
		}
		if exists {
			continue
		}
		if err := applyFile(ctx, db, name); err != nil {
			return err
		}
		slog.Info("applied migration", "file", name)
	}
	return nil
}

// applyFile runs one migration file and records it, in a single transaction:
// either the whole file applies and is recorded, or nothing changes.
func applyFile(ctx context.Context, db *sql.DB, name string) error {
	content, err := migrationFiles.ReadFile(name)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // no-op after a successful Commit

	if _, err := tx.ExecContext(ctx, string(content)); err != nil {
		return fmt.Errorf("apply %s: %w", name, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, name); err != nil {
		return err
	}
	return tx.Commit()
}
