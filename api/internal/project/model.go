// Package project is everything about projects: the data types, input
// validation, storage in Postgres and the HTTP handlers.
//
// The package is organised by FEATURE (project/, later deployment/), not by
// layer (models/, controllers/). Everything you need to understand projects
// sits in one folder.
package project

import (
	"context"
	"errors"
	"time"
)

// Project is what the API returns. The `json:"..."` tags set the JSON field names.
type Project struct {
	ID        int64             `json:"id"`
	Name      string            `json:"name"`
	RepoURL   string            `json:"repoUrl"`
	Branch    string            `json:"branch"`
	Subdomain string            `json:"subdomain"`
	EnvVars   map[string]string `json:"envVars,omitempty"` // only filled when fetching one project
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

// Input is what a client sends to create or update a project.
// It is a separate type from Project so a client can never set id or createdAt.
type Input struct {
	Name      string            `json:"name"`
	RepoURL   string            `json:"repoUrl"`
	Branch    string            `json:"branch"`
	Subdomain string            `json:"subdomain"`
	EnvVars   map[string]string `json:"envVars"`
}

// Errors the store returns, so handlers can map them to HTTP status codes
// without knowing anything about Postgres.
var (
	ErrNotFound       = errors.New("project not found")
	ErrSubdomainTaken = errors.New("subdomain is already taken")
)

// Store is the storage contract the handlers depend on.
//
// WHY an interface: handlers call Store, not Postgres directly. In production
// it is PostgresStore; in tests it is an in-memory fake, so handler tests run
// in milliseconds with no database. ("Accept interfaces" is a Go idiom.)
type Store interface {
	List(ctx context.Context) ([]Project, error)
	Get(ctx context.Context, id int64) (Project, error)
	Create(ctx context.Context, in Input) (Project, error)
	Update(ctx context.Context, id int64, in Input) (Project, error)
	Delete(ctx context.Context, id int64) error
}
