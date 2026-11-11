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
//	queued -> cloning -> building -> deploying -> live -> stopped (replaced by a newer one)
//	   \_________\__________\___________\-----> failed
type Status string

const (
	StatusQueued    Status = "queued"
	StatusCloning   Status = "cloning"
	StatusBuilding  Status = "building"
	StatusDeploying Status = "deploying" // container started, waiting for it to be healthy
	StatusLive      Status = "live"      // serving traffic
	StatusStopped   Status = "stopped"   // was live, replaced by a newer deployment
	StatusFailed    Status = "failed"
)

// Final reports whether the build/deploy process is over. ("live" can still
// become "stopped" later, but nothing more happens to it in this run.)
func (s Status) Final() bool {
	return s == StatusLive || s == StatusStopped || s == StatusFailed
}

// Trigger says why a deployment was started.
type Trigger string

const (
	TriggerManual   Trigger = "manual"   // Deploy button
	TriggerPush     Trigger = "push"     // GitHub webhook
	TriggerRollback Trigger = "rollback" // redeploy an earlier image, no rebuild
	TriggerConfig   Trigger = "config"   // restart the live image with new env vars
)

// NewDeployment is what Create needs. With Source set, the new deployment
// reuses Source's image and commit and skips cloning and building.
type NewDeployment struct {
	ProjectID int64
	Trigger   Trigger
	Source    *Deployment
}

// Redeploy reports whether this deployment reuses an existing image.
func (d Deployment) Redeploy() bool {
	return d.SourceID != nil && d.ImageTag != ""
}

type Deployment struct {
	ID            int64      `json:"id"`
	ProjectID     int64      `json:"projectId"`
	Status        Status     `json:"status"`
	CommitSHA     string     `json:"commitSha"`
	CommitMessage string     `json:"commitMessage"`
	ImageTag      string     `json:"imageTag"`
	ContainerName string     `json:"containerName"`
	Trigger       Trigger    `json:"trigger"`
	SourceID      *int64     `json:"sourceDeploymentId"` // rollback/config: the deployment whose image was reused
	ImagePruned   bool       `json:"imagePruned"`        // the image was deleted to save disk: no rollback
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
	ErrNoImage         = errors.New("this deployment has no image to redeploy")
	ErrAppLimit        = errors.New("the limit of running apps is reached: delete a project first")
)

// Store is the storage contract for deployments (Postgres in production,
// an in-memory fake in tests).
type Store interface {
	Create(ctx context.Context, nd NewDeployment) (Deployment, error)
	Get(ctx context.Context, id int64) (Deployment, error)
	ListByProject(ctx context.Context, projectID int64, limit int) ([]Deployment, error)

	SetStatus(ctx context.Context, id int64, status Status) error
	SetCommit(ctx context.Context, id int64, sha, message string) error
	SetImage(ctx context.Context, id int64, tag string) error
	SetContainer(ctx context.Context, id int64, name string) error
	// MarkLive makes this deployment the project's live one, and its previous
	// live deployment (if any) "stopped", in one transaction.
	MarkLive(ctx context.Context, projectID, id int64) error
	// LiveByProject returns the project's live deployment, or ErrNotFound.
	LiveByProject(ctx context.Context, projectID int64) (Deployment, error)
	// CountLive returns how many projects have a live deployment.
	CountLive(ctx context.Context) (int, error)

	// OldImages returns the project's image tags beyond the newest `keep`
	// (by last use), never the live one: these can be deleted.
	OldImages(ctx context.Context, projectID int64, keep int) ([]string, error)
	// MarkImagePruned flags every deployment of the project using tag.
	MarkImagePruned(ctx context.Context, projectID int64, tag string) error
	Fail(ctx context.Context, id int64, reason string) error

	AppendLog(ctx context.Context, id int64, line int, text string) error
	Logs(ctx context.Context, id int64, afterLine int) ([]LogLine, error)

	// FailUnfinished marks every unfinished deployment as failed. Called at
	// start-up: builds that were running when the server stopped are dead.
	FailUnfinished(ctx context.Context) (int64, error)
}
