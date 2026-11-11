package deployment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// deployLive runs one normal deployment to "live" and returns it.
func deployLive(t *testing.T, store *memStore, w *Worker) Deployment {
	t.Helper()
	d, _ := store.Create(context.Background(), NewDeployment{ProjectID: 1})
	w.process(context.Background(), d.ID)
	d, _ = store.Get(context.Background(), d.ID)
	if d.Status != StatusLive {
		t.Fatalf("setup: want live, got %s (%s)", d.Status, d.Error)
	}
	return d
}

func countBuilds(r *fakeRunner) int {
	n := 0
	for _, c := range r.commands {
		if strings.HasPrefix(c, "build ") {
			n++
		}
	}
	return n
}

func TestRollbackReusesImageWithoutBuilding(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK)}
	store := newMemStore()
	w := newTestWorker(t, store, runner)
	ctx := context.Background()

	first := deployLive(t, store, w)
	deployLive(t, store, w) // second deploy: first becomes "stopped"
	first, _ = store.Get(ctx, first.ID)
	buildsBefore := countBuilds(runner)

	rb, err := store.Create(ctx, NewDeployment{ProjectID: 1, Trigger: TriggerRollback, Source: &first})
	if err != nil {
		t.Fatal(err)
	}
	w.process(ctx, rb.ID)
	rb, _ = store.Get(ctx, rb.ID)

	if rb.Status != StatusLive || rb.ImageTag != first.ImageTag {
		t.Fatalf("rollback: want live with image %s, got %s %s (%s)", first.ImageTag, rb.Status, rb.ImageTag, rb.Error)
	}
	if countBuilds(runner) != buildsBefore {
		t.Error("a rollback must not run docker build")
	}
	logs, _ := store.Logs(ctx, rb.ID, 0)
	if !strings.Contains(logs[0].Text, "Rolling back") {
		t.Errorf("first log line should explain the rollback, got %q", logs[0].Text)
	}
}

func TestRollbackFailsWhenImageIsGone(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK)}
	store := newMemStore()
	w := newTestWorker(t, store, runner)
	ctx := context.Background()

	live := deployLive(t, store, w)
	runner.imageMissing = true
	rb, _ := store.Create(ctx, NewDeployment{ProjectID: 1, Trigger: TriggerConfig, Source: &live})
	w.process(ctx, rb.ID)

	rb, _ = store.Get(ctx, rb.ID)
	live, _ = store.Get(ctx, live.ID)
	if rb.Status != StatusFailed || !strings.Contains(rb.Error, "no longer exists") {
		t.Fatalf("want failed/no longer exists, got %s %q", rb.Status, rb.Error)
	}
	if live.Status != StatusLive {
		t.Error("the current version must stay live")
	}
}

func TestRedeployEndpointPicksTrigger(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK)}
	store := newMemStore()
	w := newTestWorker(t, store, runner)
	first := deployLive(t, store, w)
	second := deployLive(t, store, w)

	mux := http.NewServeMux()
	NewHandler(store, w, NewBroker(), w.runtime).Register(mux)
	post := func(id int64) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/deployments/"+strconv.FormatInt(id, 10)+"/redeploy", nil))
		return rec
	}

	// Older deployment -> rollback.
	if rec := post(first.ID); rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"trigger":"rollback"`) {
		t.Fatalf("rollback: got %d %s", rec.Code, rec.Body)
	}
	// While that is queued, a second redeploy is refused.
	if rec := post(second.ID); rec.Code != http.StatusConflict {
		t.Fatalf("second redeploy while one is queued: want 409, got %d", rec.Code)
	}
}

func TestPushDuringDeploymentIsDeferredThenRuns(t *testing.T) {
	runner := &fakeRunner{appPort: testApp(t, http.StatusOK)}
	store := newMemStore()
	w := newTestWorker(t, store, runner)
	ctx := context.Background()

	running, _ := w.Deploy(ctx, NewDeployment{ProjectID: 1, Trigger: TriggerPush})
	// Two more pushes arrive while it is still queued/running.
	for i := 0; i < 2; i++ {
		if _, err := w.Deploy(ctx, NewDeployment{ProjectID: 1, Trigger: TriggerPush}); err != ErrDeferred {
			t.Fatalf("push during a deployment: want ErrDeferred, got %v", err)
		}
	}
	<-w.queue // take the running one off the queue, as a worker would
	w.process(ctx, running.ID)
	w.runPending(ctx, 1)

	// Exactly ONE follow-up deployment, not two: newest commit wins.
	list, _ := store.ListByProject(ctx, 1, 10)
	if len(list) != 2 {
		t.Fatalf("want 2 deployments (original + one follow-up), got %d", len(list))
	}
	select {
	case id := <-w.queue:
		if id == running.ID {
			t.Fatal("the follow-up must be a new deployment")
		}
	default:
		t.Fatal("the follow-up deployment should be queued")
	}
}

// --- webhook --------------------------------------------------------------------

const testSecret = "s3cret"

func sign(body string) string {
	mac := hmac.New(sha256.New, []byte(testSecret))
	mac.Write([]byte(body))
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

func sendWebhook(t *testing.T, w *Worker, event, body, signature string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	NewWebhookHandler(testSecret, demoProject, w).Register(mux)
	req := httptest.NewRequest("POST", "/api/webhooks/github", strings.NewReader(body))
	req.Header.Set("X-GitHub-Event", event)
	req.Header.Set("X-Hub-Signature-256", signature)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// demoProject's repo is https://github.com/octocat/demo, branch main.
const pushMain = `{"ref":"refs/heads/main","after":"0123456789abcdef","repository":{"html_url":"https://github.com/Octocat/demo"}}`

func TestWebhook(t *testing.T) {
	cases := []struct {
		name      string
		event     string
		body      string
		signature string
		wantCode  int
		wantBody  string
	}{
		{"push to main deploys", "push", pushMain, sign(pushMain), 202, "queued deployment"},
		{"bad signature", "push", pushMain, sign(pushMain + " "), 401, "invalid signature"},
		{"missing signature", "push", pushMain, "", 401, "invalid signature"},
		{"ping", "ping", `{"zen":"hi"}`, sign(`{"zen":"hi"}`), 200, "pong"},
		{"other event ignored", "issues", `{}`, sign(`{}`), 202, "ignored event"},
		{"other branch: nothing to deploy", "push",
			`{"ref":"refs/heads/dev","repository":{"html_url":"https://github.com/octocat/demo"}}`,
			sign(`{"ref":"refs/heads/dev","repository":{"html_url":"https://github.com/octocat/demo"}}`), 202, `"deployments":[]`},
		{"tag push ignored", "push", `{"ref":"refs/tags/v1"}`, sign(`{"ref":"refs/tags/v1"}`), 202, "not a branch push"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := newTestWorker(t, newMemStore(), &fakeRunner{})
			rec := sendWebhook(t, w, tc.event, tc.body, tc.signature)
			if rec.Code != tc.wantCode || !strings.Contains(rec.Body.String(), tc.wantBody) {
				t.Fatalf("want %d containing %q, got %d %s", tc.wantCode, tc.wantBody, rec.Code, rec.Body)
			}
		})
	}
}

func TestWebhookOffWithoutSecret(t *testing.T) {
	mux := http.NewServeMux()
	NewWebhookHandler("", demoProject, nil).Register(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/webhooks/github", strings.NewReader("{}")))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 when no secret is set, got %d", rec.Code)
	}
}

func TestSameRepo(t *testing.T) {
	if !sameRepo("https://github.com/Owner/Repo.git", "https://github.com/owner/repo/") {
		t.Error("case, .git and trailing slash should not matter")
	}
	if sameRepo("https://github.com/owner/repo", "https://github.com/owner/repo2") {
		t.Error("different repos must not match")
	}
}
