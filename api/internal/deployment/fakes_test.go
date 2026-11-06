package deployment

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	noDockerfile bool // the "cloned" repo has no Dockerfile
	cloneFails   bool
	buildFails   bool
}

func (f fakeRunner) Run(ctx context.Context, dir string, onLine func(string), name string, args ...string) error {
	switch name {
	case "git": // clone
		if f.cloneFails {
			onLine("fatal: repository not found")
			return errors.New("exit status 128")
		}
		onLine("Cloning into '.'...")
		if !f.noDockerfile {
			return os.WriteFile(filepath.Join(dir, "Dockerfile"), []byte("FROM scratch\n"), 0o644)
		}
		return nil
	case "docker": // build
		onLine("#1 [internal] load build definition from Dockerfile")
		if f.buildFails {
			onLine("ERROR: failed to solve")
			return errors.New("exit status 1")
		}
		onLine("#5 naming to docker.io/launchpad/demo:abc1234 done")
		return nil
	}
	return errors.New("unexpected command " + name)
}

func (f fakeRunner) Output(ctx context.Context, dir string, name string, args ...string) (string, error) {
	if len(args) > 0 && args[0] == "rev-parse" {
		return "abc1234", nil
	}
	return "Initial commit", nil
}
