package deployment

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"launchpad/api/internal/project"
)

var demoProject = fakeProjects{1: project.Project{
	ID: 1, Name: "Demo", RepoURL: "https://github.com/octocat/demo", Branch: "main", Subdomain: "demo",
}}

// testApp starts a tiny HTTP server that plays the role of the deployed app,
// and returns its port (what the fake "docker port" reports).
func testApp(t *testing.T, status int) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv.URL[strings.LastIndex(srv.URL, ":")+1:]
}

// newTestWorker builds a Worker whose runtime writes routes to a temp folder
// and does not wait between steps.
func newTestWorker(t *testing.T, store *memStore, runner *fakeRunner) *Worker {
	t.Helper()
	rt := NewRuntime(runner, "launchpad-apps", t.TempDir(), "localhost")
	rt.DrainDelay = 0
	rt.HealthTimeout = 3 * time.Second
	return NewWorker(store, demoProject, NewBroker(), runner, rt)
}

// runOnce creates a deployment and processes it synchronously.
func runOnce(t *testing.T, runner *fakeRunner) (*memStore, Deployment, *Worker) {
	t.Helper()
	store := newMemStore()
	w := newTestWorker(t, store, runner)

	d, err := store.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	w.process(context.Background(), d.ID)

	d, _ = store.Get(context.Background(), d.ID)
	return store, d, w
}

func TestDeploySucceeds(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK)}
	store, d, w := runOnce(t, runner)

	if d.Status != StatusLive {
		t.Fatalf("want live, got %s (error %q)", d.Status, d.Error)
	}
	if d.CommitSHA != "abc1234" || d.ImageTag != "launchpad/demo:abc1234" || d.ContainerName != "lp-demo-1" {
		t.Errorf("commit/image/container not recorded: %+v", d)
	}

	logs, _ := store.Logs(context.Background(), d.ID, 0)
	for i, l := range logs {
		if l.Line != i+1 {
			t.Fatalf("log lines must be numbered 1, 2, 3...: got %d at position %d", l.Line, i)
		}
	}
	if !strings.Contains(logs[len(logs)-1].Text, "Live at http://demo.localhost") {
		t.Errorf("last line should report the live URL, got %q", logs[len(logs)-1].Text)
	}

	// The route file must point the subdomain at the new container.
	route, err := os.ReadFile(w.runtime.routeFile(1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(route), "Host(`demo.localhost`)") || !strings.Contains(string(route), "http://lp-demo-1:3000") {
		t.Errorf("unexpected route file:\n%s", route)
	}
}

func TestRedeploySwapsContainersWithoutGap(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK)}
	store := newMemStore()
	w := newTestWorker(t, store, runner)
	ctx := context.Background()

	first, _ := store.Create(ctx, 1)
	w.process(ctx, first.ID)
	second, _ := store.Create(ctx, 1)
	w.process(ctx, second.ID)

	first, _ = store.Get(ctx, first.ID)
	second, _ = store.Get(ctx, second.ID)
	if first.Status != StatusStopped || second.Status != StatusLive {
		t.Fatalf("want first stopped and second live, got %s and %s", first.Status, second.Status)
	}
	if strings.Join(runner.running, ",") != "lp-demo-2" {
		t.Errorf("only the new container should remain, got %v", runner.running)
	}

	// Order matters for zero downtime: the old container may only be removed
	// AFTER the new one was started.
	var runAt, rmAt int
	for i, c := range runner.commands {
		if strings.HasPrefix(c, "run ") && strings.Contains(c, "lp-demo-2") {
			runAt = i
		}
		if c == "rm -f lp-demo-1" {
			rmAt = i
		}
	}
	if rmAt < runAt {
		t.Errorf("old container removed before the new one started: %v", runner.commands)
	}
}

func TestFailedHealthCheckKeepsOldVersion(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK)}
	store := newMemStore()
	w := newTestWorker(t, store, runner)
	ctx := context.Background()

	good, _ := store.Create(ctx, 1)
	w.process(ctx, good.ID)
	route, _ := os.ReadFile(w.runtime.routeFile(1))

	runner.appCrashes = true // the next version exits on start-up
	bad, _ := store.Create(ctx, 1)
	w.process(ctx, bad.ID)

	good, _ = store.Get(ctx, good.ID)
	bad, _ = store.Get(ctx, bad.ID)
	if bad.Status != StatusFailed || !strings.Contains(bad.Error, "did not become healthy") {
		t.Fatalf("bad deploy: want failed/unhealthy, got %s %q", bad.Status, bad.Error)
	}
	if good.Status != StatusLive {
		t.Fatalf("the previous version must stay live, got %s", good.Status)
	}
	after, _ := os.ReadFile(w.runtime.routeFile(1))
	if string(after) != string(route) {
		t.Error("the route must not change when the new version is unhealthy")
	}
	if strings.Join(runner.running, ",") != "lp-demo-1" {
		t.Errorf("the failed container should be removed, the good one kept: %v", runner.running)
	}
}

func TestBuildFailures(t *testing.T) {
	cases := []struct {
		name      string
		runner    *fakeRunner
		wantError string
	}{
		{"clone fails", &fakeRunner{cloneFails: true}, "git clone failed"},
		{"no Dockerfile", &fakeRunner{noDockerfile: true}, "no Dockerfile"},
		{"docker build fails", &fakeRunner{buildFails: true}, "docker build failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d, _ := runOnce(t, tc.runner)
			if d.Status != StatusFailed {
				t.Fatalf("want failed, got %s", d.Status)
			}
			if !strings.Contains(d.Error, tc.wantError) {
				t.Errorf("want error containing %q, got %q", tc.wantError, d.Error)
			}
		})
	}
}

func TestAppPort(t *testing.T) {
	if got := AppPort(project.Project{}); got != 3000 {
		t.Errorf("default port: want 3000, got %d", got)
	}
	if got := AppPort(project.Project{EnvVars: map[string]string{"PORT": "8080"}}); got != 8080 {
		t.Errorf("PORT env var: want 8080, got %d", got)
	}
	if got := AppPort(project.Project{EnvVars: map[string]string{"PORT": "abc"}}); got != 3000 {
		t.Errorf("invalid PORT: want 3000, got %d", got)
	}
}

func TestOnlyOneActiveDeploymentPerProject(t *testing.T) {
	store := newMemStore()
	if _, err := store.Create(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(context.Background(), 1); err != ErrAlreadyActive {
		t.Fatalf("want ErrAlreadyActive, got %v", err)
	}
}

func TestQueueFull(t *testing.T) {
	w := newTestWorker(t, newMemStore(), &fakeRunner{})
	w.queue = make(chan int64, 1) // room for one job; no worker is running
	if err := w.Enqueue(1); err != nil {
		t.Fatal(err)
	}
	if err := w.Enqueue(2); err != ErrQueueFull {
		t.Fatalf("want ErrQueueFull, got %v", err)
	}
}

func TestBrokerDeliversAndUnsubscribes(t *testing.T) {
	b := NewBroker()
	ch, unsubscribe := b.Subscribe(7)

	b.Publish(7, Event{Type: "log", Line: 1, Text: "hello"})
	if e := <-ch; e.Text != "hello" {
		t.Fatalf("got %+v", e)
	}

	unsubscribe()
	if _, open := <-ch; open {
		t.Fatal("channel should be closed after unsubscribe")
	}
	unsubscribe()                    // calling twice must not panic
	b.Publish(7, Event{Type: "log"}) // publishing with no subscribers is fine
}

func TestBrokerDropsSlowSubscriber(t *testing.T) {
	b := NewBroker()
	ch, _ := b.Subscribe(7)
	for i := 0; i <= subscriberBuffer; i++ { // one more than the buffer holds
		b.Publish(7, Event{Type: "log", Line: i + 1})
	}
	count := 0
	for range ch { // loop ends because the broker closed the channel
		count++
	}
	if count != subscriberBuffer {
		t.Fatalf("want %d buffered events then a closed channel, got %d", subscriberBuffer, count)
	}
}

// TestStreamReplaysFinishedBuild checks the SSE endpoint for a build that has
// already finished: every stored line, then the status, then "done".
func TestStreamReplaysFinishedBuild(t *testing.T) {
	store, d, _ := runOnce(t, &fakeRunner{appPort: testApp(t, http.StatusOK)})
	mux := http.NewServeMux()
	NewHandler(store, nil, NewBroker()).Register(mux)

	srv := httptest.NewServer(mux)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/api/deployments/1/events", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()

	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("want text/event-stream, got %q", ct)
	}

	var events []string
	sc := bufio.NewScanner(res.Body)
	for sc.Scan() {
		if name, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
			events = append(events, name)
		}
	}

	logs, _ := store.Logs(context.Background(), d.ID, 0)
	if len(events) != len(logs)+2 {
		t.Fatalf("want %d log events + status + done, got %v", len(logs), events)
	}
	if events[len(events)-2] != "status" || events[len(events)-1] != "done" {
		t.Errorf("stream should end with status then done, got %v", events[len(events)-2:])
	}
}

// TestStreamResumesAfterLastEventID checks reconnect: lines the browser
// already has are not sent again.
func TestStreamResumesAfterLastEventID(t *testing.T) {
	store, _, _ := runOnce(t, &fakeRunner{appPort: testApp(t, http.StatusOK)})
	mux := http.NewServeMux()
	NewHandler(store, nil, NewBroker()).Register(mux)

	req := httptest.NewRequest("GET", "/api/deployments/1/events", nil)
	req.Header.Set("Last-Event-ID", "3")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	body := rec.Body.String()
	if strings.Contains(body, "id: 3\n") || !strings.Contains(body, "id: 4\n") {
		t.Fatalf("should resume after line 3:\n%s", body)
	}
}
