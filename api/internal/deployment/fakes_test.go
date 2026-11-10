package deployment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"launchpad/api/internal/project"
)

// ---- in-memory Store ---------------------------------------------------------

type memStore struct {
	mu     sync.Mutex
	nextID int64
	deps   map[int64]Deployment
	logs   map[int64][]LogLine
}

func newMemStore() *memStore {
	return &memStore{nextID: 1, deps: map[int64]Deployment{}, logs: map[int64][]LogLine{}}
}

func (m *memStore) Create(ctx context.Context, projectID int64) (Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.deps {
		if d.ProjectID == projectID && !d.Status.Final() {
			return Deployment{}, ErrAlreadyActive
		}
	}
	d := Deployment{ID: m.nextID, ProjectID: projectID, Status: StatusQueued, CreatedAt: time.Now()}
	m.deps[d.ID] = d
	m.nextID++
	return d, nil
}

func (m *memStore) Get(ctx context.Context, id int64) (Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deps[id]
	if !ok {
		return Deployment{}, ErrNotFound
	}
	return d, nil
}

func (m *memStore) ListByProject(ctx context.Context, projectID int64, limit int) ([]Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Deployment{}
	for _, d := range m.deps {
		if d.ProjectID == projectID {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *memStore) update(id int64, f func(*Deployment)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	d, ok := m.deps[id]
	if !ok {
		return ErrNotFound
	}
	f(&d)
	m.deps[id] = d
	return nil
}

func (m *memStore) SetStatus(ctx context.Context, id int64, s Status) error {
	return m.update(id, func(d *Deployment) { d.Status = s })
}
func (m *memStore) SetCommit(ctx context.Context, id int64, sha, msg string) error {
	return m.update(id, func(d *Deployment) { d.CommitSHA, d.CommitMessage = sha, msg })
}
func (m *memStore) SetImage(ctx context.Context, id int64, tag string) error {
	return m.update(id, func(d *Deployment) { d.ImageTag = tag })
}
func (m *memStore) SetContainer(ctx context.Context, id int64, name string) error {
	return m.update(id, func(d *Deployment) { d.ContainerName = name })
}

func (m *memStore) MarkLive(ctx context.Context, projectID, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, d := range m.deps {
		if d.ProjectID == projectID && d.Status == StatusLive && k != id {
			d.Status = StatusStopped
			m.deps[k] = d
		}
	}
	d := m.deps[id]
	d.Status = StatusLive
	m.deps[id] = d
	return nil
}

func (m *memStore) LiveByProject(ctx context.Context, projectID int64) (Deployment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, d := range m.deps {
		if d.ProjectID == projectID && d.Status == StatusLive {
			return d, nil
		}
	}
	return Deployment{}, ErrNotFound
}

func (m *memStore) Fail(ctx context.Context, id int64, reason string) error {
	return m.update(id, func(d *Deployment) { d.Status, d.Error = StatusFailed, reason })
}

func (m *memStore) AppendLog(ctx context.Context, id int64, line int, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.logs[id] = append(m.logs[id], LogLine{Line: line, Text: text})
	return nil
}

func (m *memStore) Logs(ctx context.Context, id int64, after int) ([]LogLine, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []LogLine{}
	for _, l := range m.logs[id] {
		if l.Line > after {
			out = append(out, l)
		}
	}
	return out, nil
}

func (m *memStore) FailUnfinished(ctx context.Context) (int64, error) { return 0, nil }

// ---- fake projects -----------------------------------------------------------

type fakeProjects map[int64]project.Project

func (f fakeProjects) Get(ctx context.Context, id int64) (project.Project, error) {
	p, ok := f[id]
	if !ok {
		return project.Project{}, project.ErrNotFound
	}
	return p, nil
}

// ---- fake git / docker -------------------------------------------------------

// fakeRunner pretends to be git and docker. Each field switches on one
// failure, so a test can walk down every path of the worker.
type fakeRunner struct {
	noDockerfile bool   // the "cloned" repo has no Dockerfile
	cloneFails   bool   // git clone fails
	buildFails   bool   // docker build fails
	appPort      string // what "docker port" reports: a test HTTP server's port
	appCrashes   bool   // "docker inspect" reports the container exited

	mu       sync.Mutex
	commands []string // every docker command run, for assertions
	running  []string // container names "docker ps" reports
}

func (f *fakeRunner) record(args []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commands = append(f.commands, strings.Join(args, " "))
}

func (f *fakeRunner) Run(ctx context.Context, dir string, onLine func(string), name string, args ...string) error {
	switch {
	case name == "git": // clone
		if f.cloneFails {
			onLine("fatal: repository not found")
			return errors.New("exit status 128")
		}
		onLine("Cloning into '.'...")
		if !f.noDockerfile {
			return os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
		}
		return nil
	case name == "docker" && args[0] == "build":
		f.record(args)
		onLine("#1 [internal] load build definition from Dockerfile")
		if f.buildFails {
			onLine("ERROR: failed to solve")
			return errors.New("exit status 1")
		}
		onLine("#5 naming to docker.io/launchpad/demo:abc1234 done")
		return nil
	case name == "docker" && args[0] == "logs":
		onLine("Error: Cannot find module '/app/server.js'")
		return nil
	}
	return errors.New("unexpected command " + name)
}

func (f *fakeRunner) Output(ctx context.Context, dir string, name string, args ...string) (string, error) {
	if name == "git" {
		if args[0] == "rev-parse" {
			return "abc1234", nil
		}
		return "Initial commit", nil
	}
	f.record(args)
	switch args[0] {
	case "run":
		f.mu.Lock()
		f.running = append(f.running, args[3]) // args: run --detach --name <name> ...
		f.mu.Unlock()
		return "3f2a9c1e5b7d", nil
	case "port":
		if f.appCrashes {
			return "127.0.0.1:1", nil // nothing listens there: the app never answers
		}
		return "127.0.0.1:" + f.appPort, nil
	case "inspect":
		if f.appCrashes {
			return "false 0", nil
		}
		return "true 0", nil
	case "ps":
		f.mu.Lock()
		defer f.mu.Unlock()
		return strings.Join(f.running, "\n"), nil
	case "rm":
		f.mu.Lock()
		defer f.mu.Unlock()
		kept := f.running[:0]
		for _, n := range f.running {
			if n != args[len(args)-1] {
				kept = append(kept, n)
			}
		}
		f.running = kept
		return "", nil
	}
	return "", errors.New("unexpected docker command " + args[0])
}
