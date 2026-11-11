package deployment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"launchpad/api/internal/project"
)

// ProjectGetter is the one thing the worker needs from projects.
// (A small interface defined where it is USED is a common Go pattern.)
type ProjectGetter interface {
	Get(ctx context.Context, id int64) (project.Project, error)
}

// Worker builds deployments in the background.
//
// The job queue is a buffered Go CHANNEL of deployment ids. The HTTP handler
// puts an id in and returns immediately (202 Accepted); worker goroutines
// take ids out and do the slow work. Builds take minutes, so they must never
// run inside the HTTP request.
type Worker struct {
	deployments  Store
	projects     ProjectGetter
	broker       *Broker
	runner       Runner
	runtime      *Runtime
	queue        chan int64
	buildTimeout time.Duration
	maxLogLines  int
	KeepImages   int // images kept per project for rollback; older ones are deleted
	MaxLiveApps  int // most apps that may run at once (0 = no limit)

	// pending holds projects that got a git push while a deployment was
	// already running. When that deployment ends, ONE new deployment starts
	// and builds the branch's newest commit: pushes in between are skipped
	// ("newest commit wins").
	mu      sync.Mutex
	pending map[int64]bool
}

func NewWorker(deployments Store, projects ProjectGetter, broker *Broker, runner Runner, runtime *Runtime) *Worker {
	return &Worker{
		deployments:  deployments,
		projects:     projects,
		broker:       broker,
		runner:       runner,
		runtime:      runtime,
		queue:        make(chan int64, 100), // up to 100 waiting builds
		buildTimeout: 15 * time.Minute,
		maxLogLines:  5000, // stop storing lines after this, so one build cannot fill the database
		KeepImages:   5,
		MaxLiveApps:  10,
		pending:      map[int64]bool{},
	}
}

// ErrDeferred means a push arrived during a running deployment; it will be
// deployed as soon as that one finishes.
var ErrDeferred = errors.New("a deployment is already running; this push will deploy when it finishes")

// Deploy records a new deployment and queues it. Used by the Deploy button,
// rollbacks, config changes and GitHub pushes.
func (w *Worker) Deploy(ctx context.Context, nd NewDeployment) (Deployment, error) {
	if err := w.checkAppLimit(ctx, nd.ProjectID); err != nil {
		return Deployment{}, err
	}
	d, err := w.deployments.Create(ctx, nd)
	if errors.Is(err, ErrAlreadyActive) && nd.Trigger == TriggerPush {
		w.mu.Lock()
		w.pending[nd.ProjectID] = true
		w.mu.Unlock()
		return Deployment{}, ErrDeferred
	}
	if err != nil {
		return Deployment{}, err
	}
	if err := w.Enqueue(d.ID); err != nil {
		_ = w.deployments.Fail(ctx, d.ID, err.Error())
		return Deployment{}, err
	}
	return d, nil
}

// checkAppLimit refuses to bring ANOTHER app online once MaxLiveApps are
// running. Redeploying a project that is already live does not add an app,
// so it is always allowed.
//
// It is a soft limit: two projects deploying at the very same moment could
// both pass the check. Fine for a guard against filling the machine; a hard
// limit would need a lock or a counter row in the database.
func (w *Worker) checkAppLimit(ctx context.Context, projectID int64) error {
	if w.MaxLiveApps <= 0 {
		return nil
	}
	if _, err := w.deployments.LiveByProject(ctx, projectID); err == nil {
		return nil // already live: replacing it keeps the count the same
	}
	n, err := w.deployments.CountLive(ctx)
	if err != nil {
		return err
	}
	if n >= w.MaxLiveApps {
		return ErrAppLimit
	}
	return nil
}

// runPending starts the deferred push deployment of a project, if any.
func (w *Worker) runPending(ctx context.Context, projectID int64) {
	w.mu.Lock()
	wanted := w.pending[projectID]
	delete(w.pending, projectID)
	w.mu.Unlock()

	if !wanted || ctx.Err() != nil {
		return
	}
	if _, err := w.Deploy(ctx, NewDeployment{ProjectID: projectID, Trigger: TriggerPush}); err != nil {
		slog.Error("start deferred push deployment", "project", projectID, "err", err)
	}
}

// Enqueue adds a deployment to the queue without blocking.
func (w *Worker) Enqueue(id int64) error {
	select {
	case w.queue <- id:
		return nil
	default:
		return ErrQueueFull
	}
}

// Start launches n worker goroutines. They stop when ctx is cancelled
// (server shutdown), which also kills any build in progress.
func (w *Worker) Start(ctx context.Context, n int) {
	for i := 0; i < n; i++ {
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case id := <-w.queue:
					projectID := w.process(ctx, id)
					w.runPending(ctx, projectID)
				}
			}
		}()
	}
}

// build holds the state of one build while it runs.
type build struct {
	w    *Worker
	ctx  context.Context
	id   int64
	line int // number of the last log line written
}

// log stores a line and pushes it to anyone watching.
func (b *build) log(text string) {
	if b.line >= b.w.maxLogLines {
		return
	}
	b.line++
	if b.line == b.w.maxLogLines {
		text = fmt.Sprintf("… log truncated after %d lines", b.w.maxLogLines)
	}
	if err := b.w.deployments.AppendLog(b.ctx, b.id, b.line, text); err != nil {
		slog.Error("append log", "deployment", b.id, "err", err)
	}
	b.w.broker.Publish(b.id, Event{Type: "log", Line: b.line, Text: text})
}

func (b *build) setStatus(s Status) error {
	if err := b.w.deployments.SetStatus(b.ctx, b.id, s); err != nil {
		return err
	}
	b.w.broker.Publish(b.id, Event{Type: "status", Status: s})
	return nil
}

func (b *build) fail(reason string) {
	b.log("✗ " + reason)
	// context.Background(): even if the server is shutting down (b.ctx
	// cancelled), still record the failure.
	if err := b.w.deployments.Fail(context.Background(), b.id, reason); err != nil {
		slog.Error("mark failed", "deployment", b.id, "err", err)
	}
	b.w.broker.Publish(b.id, Event{Type: "status", Status: StatusFailed, Error: reason})
}

// process runs one deployment: clone -> read commit -> check Dockerfile ->
// docker build -> release (run it and switch traffic to it).
// A rollback or config change skips straight to release with the old image.
// It returns the project id (0 if the deployment could not be loaded).
func (w *Worker) process(ctx context.Context, id int64) int64 {
	ctx, cancel := context.WithTimeout(ctx, w.buildTimeout)
	defer cancel()

	b := &build{w: w, ctx: ctx, id: id}

	d, err := w.deployments.Get(ctx, id)
	if err != nil {
		slog.Error("load deployment", "deployment", id, "err", err)
		return 0
	}
	p, err := w.projects.Get(ctx, d.ProjectID)
	if err != nil {
		b.fail("project not found (was it deleted?)")
		return d.ProjectID
	}

	if d.Redeploy() {
		w.redeploy(b, p, d)
	} else {
		w.buildAndRelease(b, p)
	}
	return d.ProjectID
}

// redeploy runs an image that was built earlier: no clone, no build, so it
// takes seconds. Env vars come from the project as it is NOW.
func (w *Worker) redeploy(b *build, p project.Project, d Deployment) {
	if d.Trigger == TriggerConfig {
		b.log(fmt.Sprintf("→ Restarting %s with the current environment variables (no rebuild)", d.ImageTag))
	} else {
		b.log(fmt.Sprintf("→ Rolling back to %s from deployment #%d (no rebuild)", d.ImageTag, *d.SourceID))
	}
	if !w.runtime.ImageExists(b.ctx, d.ImageTag) {
		b.fail("image " + d.ImageTag + " no longer exists on this machine")
		return
	}
	w.release(b, p, d.ImageTag)
}

// buildAndRelease clones the branch, builds its newest commit and releases it.
func (w *Worker) buildAndRelease(b *build, p project.Project) {
	ctx, id := b.ctx, b.id

	// A fresh, empty folder per build, removed afterwards whatever happens.
	dir, err := os.MkdirTemp("", "launchpad-build-*")
	if err != nil {
		b.fail("could not create a build folder")
		return
	}
	defer os.RemoveAll(dir)

	// 1. Clone only the latest commit of the branch (--depth 1 is much faster).
	if err := b.setStatus(StatusCloning); err != nil {
		b.fail("could not update status")
		return
	}
	b.log(fmt.Sprintf("→ Cloning %s (branch %s)", p.RepoURL, p.Branch))
	if err := w.runner.Run(ctx, dir, b.log, "git", "clone", "--depth", "1", "--branch", p.Branch, p.RepoURL, "."); err != nil {
		b.fail(explain("git clone failed: check the repo is public and the branch exists", err))
		return
	}

	// 2. Record which commit we are building.
	sha, err := w.runner.Output(ctx, dir, "git", "rev-parse", "--short=7", "HEAD")
	if err != nil {
		b.fail("could not read the commit")
		return
	}
	message, _ := w.runner.Output(ctx, dir, "git", "log", "-1", "--pretty=%s")
	_ = w.deployments.SetCommit(ctx, id, sha, message)
	b.log(fmt.Sprintf("→ Commit %s: %s", sha, message))

	// 3. Version 1 needs a Dockerfile in the repo root.
	if _, err := os.Stat(filepath.Join(dir, "Dockerfile")); err != nil {
		b.fail("no Dockerfile in the repository root")
		return
	}

	// 4. Build the image, tagged with the commit so every build is traceable.
	tag := fmt.Sprintf("launchpad/%s:%s", p.Subdomain, sha)
	if err := b.setStatus(StatusBuilding); err != nil {
		b.fail("could not update status")
		return
	}
	b.log("→ Building image " + tag)
	if err := w.runner.Run(ctx, dir, b.log, "docker", "build", "--progress=plain",
		"--label", fmt.Sprintf("%s=%d", labelProject, p.ID), // lets RemoveProject find its images
		"-t", tag, "."); err != nil {
		b.fail(explain("docker build failed: see the lines above", err))
		return
	}

	_ = w.deployments.SetImage(ctx, id, tag)
	b.log("✓ Built " + tag)

	w.release(b, p, tag)
}

// release runs the image and moves traffic to it with no downtime:
//
//  1. start the NEW container next to the old one (old keeps serving)
//  2. wait until the new one answers HTTP (health check)
//  3. switch the route to the new container (one atomic file rename)
//  4. mark it live, wait a moment for Traefik to reload, remove the old one
//
// If step 1 or 2 fails, the new container is removed and the route is never
// touched, so the previous version stays live.
func (w *Worker) release(b *build, p project.Project, image string) {
	ctx, rt := b.ctx, w.runtime
	name := ContainerName(p, b.id)

	if err := b.setStatus(StatusDeploying); err != nil {
		b.fail("could not update status")
		return
	}

	b.log(fmt.Sprintf("→ Starting container %s (port %d, %s memory, %s CPU)", name, AppPort(p), rt.Memory, rt.CPUs))
	hostPort, err := rt.Start(ctx, p, b.id, image)
	if err != nil {
		b.log(err.Error())
		rt.Remove(context.Background(), name)
		b.fail(explain("could not start the container", err))
		return
	}
	_ = w.deployments.SetContainer(ctx, b.id, name)

	b.log(fmt.Sprintf("→ Waiting for the app to answer HTTP on port %d", AppPort(p)))
	if err := rt.WaitHealthy(ctx, name, hostPort, b.log); err != nil {
		rt.ContainerLogs(context.Background(), name, b.log)
		rt.Remove(context.Background(), name)
		b.fail(explain(fmt.Sprintf("%s: check the app listens on port %d (set the PORT env var to change it)", err, AppPort(p)), err))
		return
	}

	if err := rt.Route(p, name); err != nil {
		rt.Remove(context.Background(), name)
		b.fail("could not update routing: " + err.Error())
		return
	}
	b.log("→ Routing " + rt.AppURL(p) + " to " + name)

	if err := w.deployments.MarkLive(ctx, p.ID, b.id); err != nil {
		b.fail("could not mark the deployment live: " + err.Error())
		return
	}

	// Give Traefik a moment to load the new route before the old container
	// disappears, so requests in flight are not cut off.
	select {
	case <-time.After(rt.DrainDelay):
	case <-ctx.Done():
	}
	rt.StopOthers(context.Background(), p.ID, name, b.log)
	w.pruneImages(context.Background(), p, b.log)

	b.log("✓ Live at " + rt.AppURL(p))
	b.w.broker.Publish(b.id, Event{Type: "status", Status: StatusLive})
}

// explain picks a clear message for timeouts and shutdowns.
func explain(msg string, err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deployment timed out"
	case errors.Is(err, context.Canceled):
		return "deployment cancelled (server shutting down)"
	}
	return msg
}

// pruneImages deletes the project's images beyond the newest KeepImages, so
// disk use stays bounded. The live image is never deleted. Deployments using
// a deleted image are flagged, and the dashboard stops offering a rollback.
func (w *Worker) pruneImages(ctx context.Context, p project.Project, logf func(string)) {
	if w.KeepImages <= 0 {
		return
	}
	tags, err := w.deployments.OldImages(ctx, p.ID, w.KeepImages)
	if err != nil {
		slog.Error("find old images", "project", p.ID, "err", err)
		return
	}
	for _, tag := range tags {
		if err := w.runtime.RemoveImage(ctx, tag); err != nil {
			slog.Warn("remove old image", "image", tag, "err", err)
			continue
		}
		_ = w.deployments.MarkImagePruned(ctx, p.ID, tag)
		logf("→ Deleted old image " + tag + " (keeping the newest " + strconv.Itoa(w.KeepImages) + ")")
	}
}
