package deployment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
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
					w.process(ctx, id)
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
func (w *Worker) process(ctx context.Context, id int64) {
	ctx, cancel := context.WithTimeout(ctx, w.buildTimeout)
	defer cancel()

	b := &build{w: w, ctx: ctx, id: id}

	d, err := w.deployments.Get(ctx, id)
	if err != nil {
		slog.Error("load deployment", "deployment", id, "err", err)
		return
	}
	p, err := w.projects.Get(ctx, d.ProjectID)
	if err != nil {
		b.fail("project not found (was it deleted?)")
		return
	}

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
	if err := w.runner.Run(ctx, dir, b.log, "docker", "build", "--progress=plain", "-t", tag, "."); err != nil {
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
