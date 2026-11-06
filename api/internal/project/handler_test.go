package project

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory Store used only by tests. Because Handler depends
// on the Store interface, it cannot tell this apart from Postgres.
type memStore struct {
	mu       sync.Mutex // handlers may run concurrently, so guard the map
	nextID   int64
	projects map[int64]Project
}

func newMemStore() *memStore {
	return &memStore{nextID: 1, projects: map[int64]Project{}}
}

func (m *memStore) List(ctx context.Context) ([]Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []Project{}
	for _, p := range m.projects {
		out = append(out, p)
	}
	return out, nil
}

func (m *memStore) Get(ctx context.Context, id int64) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok {
		return Project{}, ErrNotFound
	}
	return p, nil
}

func (m *memStore) Create(ctx context.Context, in Input) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.projects {
		if p.Subdomain == in.Subdomain {
			return Project{}, ErrSubdomainTaken
		}
	}
	now := time.Now()
	p := Project{ID: m.nextID, Name: in.Name, RepoURL: in.RepoURL, Branch: in.Branch,
		Subdomain: in.Subdomain, EnvVars: in.EnvVars, CreatedAt: now, UpdatedAt: now}
	m.projects[p.ID] = p
	m.nextID++
	return p, nil
}

func (m *memStore) Update(ctx context.Context, id int64, in Input) (Project, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.projects[id]
	if !ok {
		return Project{}, ErrNotFound
	}
	p.Name, p.RepoURL, p.Branch, p.Subdomain, p.EnvVars = in.Name, in.RepoURL, in.Branch, in.Subdomain, in.EnvVars
	p.UpdatedAt = time.Now()
	m.projects[id] = p
	return p, nil
}

func (m *memStore) Delete(ctx context.Context, id int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.projects[id]; !ok {
		return ErrNotFound
	}
	delete(m.projects, id)
	return nil
}

// newTestServer wires a fresh in-memory store into the real router.
func newTestServer() *http.ServeMux {
	mux := http.NewServeMux()
	NewHandler(newMemStore()).Register(mux)
	return mux
}

// do sends one request and returns the recorded response.
func do(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

const validBody = `{"name":"Demo","repoUrl":"https://github.com/octocat/Hello-World","subdomain":"demo","envVars":{"PORT":"3000"}}`

func TestCreateThenGet(t *testing.T) {
	mux := newTestServer()

	rec := do(t, mux, "POST", "/api/projects", validBody)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: want 201, got %d: %s", rec.Code, rec.Body)
	}
	var created Project
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Branch != "main" {
		t.Errorf("branch should default to main, got %q", created.Branch)
	}

	rec = do(t, mux, "GET", "/api/projects/1", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: want 200, got %d", rec.Code)
	}
}

func TestCreateValidation(t *testing.T) {
	// Table-driven test: each case is one row, the loop runs them all.
	cases := []struct {
		name      string
		body      string
		wantCode  int
		wantField string
	}{
		{"bad repo url", `{"name":"x","repoUrl":"https://gitlab.com/a/b","subdomain":"ok"}`, 422, "repoUrl"},
		{"uppercase subdomain is lowered, bad chars rejected", `{"name":"x","repoUrl":"https://github.com/a/b","subdomain":"no_underscores"}`, 422, "subdomain"},
		{"reserved subdomain", `{"name":"x","repoUrl":"https://github.com/a/b","subdomain":"api"}`, 422, "subdomain"},
		{"missing name", `{"repoUrl":"https://github.com/a/b","subdomain":"ok"}`, 422, "name"},
		{"bad env key", `{"name":"x","repoUrl":"https://github.com/a/b","subdomain":"ok","envVars":{"1BAD":"v"}}`, 422, "envVars"},
		// Note: encoding/json matches names case-insensitively, so "subDomain"
		// would be ACCEPTED as "subdomain". A truly unknown field is rejected.
		{"unknown field", `{"name":"x","sub_domain":"typo"}`, 400, ""},
		{"not json", `hello`, 400, ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, newTestServer(), "POST", "/api/projects", tc.body)
			if rec.Code != tc.wantCode {
				t.Fatalf("want %d, got %d: %s", tc.wantCode, rec.Code, rec.Body)
			}
			if tc.wantField == "" {
				return
			}
			var body struct {
				Fields map[string]string `json:"fields"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if _, ok := body.Fields[tc.wantField]; !ok {
				t.Errorf("want an error on field %q, got %v", tc.wantField, body.Fields)
			}
		})
	}
}

func TestDuplicateSubdomainIsConflict(t *testing.T) {
	mux := newTestServer()
	do(t, mux, "POST", "/api/projects", validBody)

	rec := do(t, mux, "POST", "/api/projects", validBody)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d", rec.Code)
	}
}

func TestNotFoundAndBadID(t *testing.T) {
	mux := newTestServer()
	if rec := do(t, mux, "GET", "/api/projects/99", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing project: want 404, got %d", rec.Code)
	}
	if rec := do(t, mux, "GET", "/api/projects/abc", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: want 400, got %d", rec.Code)
	}
	if rec := do(t, mux, "DELETE", "/api/projects/99", ""); rec.Code != http.StatusNotFound {
		t.Errorf("delete missing: want 404, got %d", rec.Code)
	}
}

func TestUpdateAndDelete(t *testing.T) {
	mux := newTestServer()
	do(t, mux, "POST", "/api/projects", validBody)

	updated := strings.Replace(validBody, `"Demo"`, `"Renamed"`, 1)
	rec := do(t, mux, "PUT", "/api/projects/1", updated)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Renamed") {
		t.Fatalf("update: got %d %s", rec.Code, rec.Body)
	}

	if rec := do(t, mux, "DELETE", "/api/projects/1", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: want 204, got %d", rec.Code)
	}
	if rec := do(t, mux, "GET", "/api/projects/1", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("after delete: want 404, got %d", rec.Code)
	}
}
