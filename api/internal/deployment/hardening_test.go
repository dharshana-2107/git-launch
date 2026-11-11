package deployment

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"launchpad/api/internal/project"
)

func TestOldImagesAreDeletedNewestKept(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK), newCommits: true}
	store := newMemStore()
	w := newTestWorker(t, store, runner)
	w.KeepImages = 2

	var deployed []Deployment
	for i := 0; i < 5; i++ {
		deployed = append(deployed, deployLive(t, store, w))
	}

	// 5 images built, the live one plus the 2 newest others are kept:
	// deployments 1 and 2 lose their image.
	for i, d := range deployed {
		d, _ = store.Get(context.Background(), d.ID)
		wantPruned := i < 2
		if d.ImagePruned != wantPruned {
			t.Errorf("deployment %d (%s): pruned=%v, want %v", d.ID, d.ImageTag, d.ImagePruned, wantPruned)
		}
	}
	removed := 0
	for _, c := range runner.commands {
		if strings.HasPrefix(c, "image rm ") {
			removed++
		}
	}
	if removed != 2 {
		t.Errorf("want 2 images removed, got %d: %v", removed, runner.commands)
	}
}

func TestRollbackToPrunedImageIsRefused(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK), newCommits: true}
	store := newMemStore()
	w := newTestWorker(t, store, runner)
	w.KeepImages = 1
	first := deployLive(t, store, w)
	deployLive(t, store, w)
	deployLive(t, store, w) // first's image is now beyond the newest 1 + live

	mux := http.NewServeMux()
	NewHandler(store, w, NewBroker(), w.runtime).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/deployments/"+strconv.FormatInt(first.ID, 10)+"/redeploy", nil))
	if rec.Code != http.StatusConflict {
		t.Fatalf("rollback to a deleted image: want 409, got %d %s", rec.Code, rec.Body)
	}
}

func TestLiveAppLimit(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK)}
	store := newMemStore()
	projects := fakeProjects{
		1: project.Project{ID: 1, RepoURL: "https://github.com/a/one", Branch: "main", Subdomain: "one"},
		2: project.Project{ID: 2, RepoURL: "https://github.com/a/two", Branch: "main", Subdomain: "two"},
	}
	rt := NewRuntime(runner, "launchpad-apps", t.TempDir(), "localhost")
	rt.DrainDelay = 0
	w := NewWorker(store, projects, NewBroker(), runner, rt)
	w.MaxLiveApps = 1
	ctx := context.Background()

	d, err := w.Deploy(ctx, NewDeployment{ProjectID: 1})
	if err != nil {
		t.Fatal(err)
	}
	<-w.queue
	w.process(ctx, d.ID)

	// A second app would exceed the limit...
	if _, err := w.Deploy(ctx, NewDeployment{ProjectID: 2}); err != ErrAppLimit {
		t.Fatalf("second app: want ErrAppLimit, got %v", err)
	}
	// ...but redeploying the app that is already live is fine.
	if _, err := w.Deploy(ctx, NewDeployment{ProjectID: 1}); err != nil {
		t.Fatalf("redeploy of the live app should be allowed, got %v", err)
	}
}
