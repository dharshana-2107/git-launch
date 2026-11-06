// Package deployment builds projects: it records deployments, runs builds
// in background goroutines, stores their logs and streams them live (SSE).
package deployment

import (
	"context"
	"errors"
	"time"
)

// Status is where a deployment is in its lifecycle:
//
//	queued -> cloning -> building -> built
//	   \_________\__________\-----> failed
type Status string

const (
	StatusQueued   Status = "queued"
	StatusCloning  Status = "cloning"
	StatusBuilding Status = "building"
	StatusBuilt    Status = "built"
	StatusFailed   Status = "failed"
)

// Final reports whether the deployment has stopped changing.
func (s Status) Final() bool {
	return s == StatusBuilt || s == StatusFailed
}

type Deployment struct {
	ID            int64      `json:"id"`
	ProjectID     int64      `json:"projectId"`
	Status        Status     `json:"status"`
	CommitSHA     string     `json:"commitSha"`
	CommitMessage string     `json:"commitMessage"`
	ImageTag      string     `json:"imageTag"`
	Error         string     `json:"error"`
	CreatedAt     time.Time  `json:"createdAt"`
	StartedAt     *time.Time `json:"startedAt"`  // pointer: null until it starts
	FinishedAt    *time.Time `json:"finishedAt"` // pointer: null until it finishes
}

// LogLine is one line of build output.
type LogLine struct {
	Line int    `json:"line"`
	Text string `json:"text"`
}

var (
	ErrNotFound        = errors.New("deployment not found")
	ErrProjectNotFound = errors.New("project not found")
	ErrAlreadyActive   = errors.New("this project already has a deployment in progress")
	ErrQueueFull       = errors.New("build queue is full, try again shortly")
)

// Store is the storage contract for deployments (Postgres in production,
// an in-memory fake in tests).
type Store interface {
	Create(ctx context.Context, projectID int64) (Deployment, error)
	Get(ctx context.Context, id int64) (Deployment, error)
	ListByProject(ctx context.Context, projectID int64, limit int) ([]Deployment, error)

	SetStatus(ctx context.Context, id int64, status Status) error
	SetCommit(ctx context.Context, id int64, sha, message string) error
	SetImage(ctx context.Context, id int64, tag string) error
	Fail(ctx context.Context, id int64, reason string) error

	AppendLog(ctx context.Context, id int64, line int, text string) error
	Logs(ctx context.Context, id int64, afterLine int) ([]LogLine, error)

	// FailUnfinished marks every unfinished deployment as failed. Called at
	// start-up: builds that were running when the server stopped are dead.
	FailUnfinished(ctx context.Context) (int64, error)
}
