package deployment

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"launchpad/api/internal/project"
)

var demoProject = fakeProjects{1: project.Project{
	ID: 1, Name: "Demo", RepoURL: "https://github.com/octocat/demo", Branch: "main", Subdomain: "demo",
}}

// runOnce creates a deployment and processes it synchronously.
func runOnce(t *testing.T, runner Runner) (*memStore, Deployment) {
	t.Helper()
	store := newMemStore()
	w := NewWorker(store, demoProject, NewBroker(), runner)

	d, err := store.Create(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	w.process(context.Background(), d.ID)

	d, _ = store.Get(context.Background(), d.ID)
	return store, d
}

func TestBuildSucceeds(t *testing.T) {
	store, d := runOnce(t, fakeRunner{})

	if d.Status != StatusBuilt {
		t.Fatalf("want built, got %s (error %q)", d.Status, d.Error)
	}
	if d.CommitSHA != "abc1234" || d.ImageTag != "launchpad/demo:abc1234" {
		t.Errorf("commit/image not recorded: %+v", d)
	}
	logs, _ := store.Logs(context.Background(), d.ID, 0)
	for i, l := range logs {
		if l.Line != i+1 {
			t.Fatalf("log lines must be numbered 1, 2, 3...: got %d at position %d", l.Line, i)
		}
	}
	if !strings.Contains(logs[len(logs)-1].Text, "Built") {
		t.Errorf("last line should report success, got %q", logs[len(logs)-1].Text)
	}
}

func TestBuildFailures(t *testing.T) {
	cases := []struct {
		name      string
		runner    fakeRunner
		wantError string
	}{
		{"clone fails", fakeRunner{cloneFails: true}, "git clone failed"},
		{"no Dockerfile", fakeRunner{noDockerfile: true}, "no Dockerfile"},
		{"docker build fails", fakeRunner{buildFails: true}, "docker build failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, d := runOnce(t, tc.runner)
			if d.Status != StatusFailed {
				t.Fatalf("want failed, got %s", d.Status)
			}
			if !strings.Contains(d.Error, tc.wantError) {
				t.Errorf("want error containing %q, got %q", tc.wantError, d.Error)
			}
		})
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
	w := NewWorker(newMemStore(), demoProject, NewBroker(), fakeRunner{})
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
	store, d := runOnce(t, fakeRunner{})
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
	store, _ := runOnce(t, fakeRunner{})
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
