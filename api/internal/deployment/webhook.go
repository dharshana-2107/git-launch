package deployment

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"launchpad/api/internal/httpx"
	"launchpad/api/internal/project"
)

// ProjectLister is what the webhook needs from projects: all of them, to
// find the ones built from the pushed repo and branch.
type ProjectLister interface {
	List(ctx context.Context) ([]project.Project, error)
}

// WebhookHandler receives GitHub "push" events and deploys matching projects.
//
// GitHub signs every delivery with the shared secret: header
// X-Hub-Signature-256 = "sha256=" + HMAC-SHA256(secret, raw body). We compute
// the same HMAC and compare. Without this check, anyone who finds the URL
// could trigger deployments.
type WebhookHandler struct {
	secret   []byte
	projects ProjectLister
	worker   *Worker
}

func NewWebhookHandler(secret string, projects ProjectLister, worker *Worker) *WebhookHandler {
	return &WebhookHandler{secret: []byte(secret), projects: projects, worker: worker}
}

func (h *WebhookHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/webhooks/github", h.github)
}

// Enabled reports whether a secret is configured (WEBHOOK_SECRET).
func (h *WebhookHandler) Enabled() bool { return len(h.secret) > 0 }

// pushEvent holds only the fields of GitHub's push payload that we use.
type pushEvent struct {
	Ref        string `json:"ref"`     // "refs/heads/main"
	After      string `json:"after"`   // SHA of the newest commit pushed
	Deleted    bool   `json:"deleted"` // true when the branch was deleted
	Repository struct {
		HTMLURL string `json:"html_url"` // "https://github.com/owner/repo"
	} `json:"repository"`
}

const maxWebhookBody = 5 << 20 // 5 MB

func (h *WebhookHandler) github(w http.ResponseWriter, r *http.Request) {
	if !h.Enabled() {
		httpx.WriteError(w, http.StatusServiceUnavailable, "webhooks are off: set WEBHOOK_SECRET and restart the API")
		return
	}

	// Read the RAW bytes: the signature is over the exact body GitHub sent,
	// so we must verify before decoding (decoding and re-encoding could
	// change spacing or key order).
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxWebhookBody))
	if err != nil {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "body too large")
		return
	}
	if !ValidSignature(h.secret, body, r.Header.Get("X-Hub-Signature-256")) {
		slog.Warn("webhook with bad signature", "delivery", r.Header.Get("X-GitHub-Delivery"))
		httpx.WriteError(w, http.StatusUnauthorized, "invalid signature")
		return
	}

	switch event := r.Header.Get("X-GitHub-Event"); event {
	case "ping": // sent once when the webhook is created
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"message": "pong"})
		return
	case "push":
	default:
		httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"message": "ignored event " + event})
		return
	}

	var push pushEvent
	if err := json.Unmarshal(body, &push); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "invalid push payload")
		return
	}
	branch, isBranch := strings.CutPrefix(push.Ref, "refs/heads/")
	if !isBranch || push.Deleted {
		httpx.WriteJSON(w, http.StatusAccepted, map[string]string{"message": "ignored: not a branch push"})
		return
	}

	projects, err := h.projects.List(r.Context())
	if err != nil {
		serverError(w, err)
		return
	}

	// One repo can back several projects (e.g. main and staging branches).
	type result struct {
		Project string `json:"project"`
		Outcome string `json:"outcome"`
	}
	results := []result{}
	for _, p := range projects {
		if !sameRepo(p.RepoURL, push.Repository.HTMLURL) || p.Branch != branch {
			continue
		}
		d, err := h.worker.Deploy(r.Context(), NewDeployment{ProjectID: p.ID, Trigger: TriggerPush})
		switch {
		case err == nil:
			results = append(results, result{p.Name, "queued deployment #" + strconv.FormatInt(d.ID, 10)})
		case errors.Is(err, ErrDeferred):
			results = append(results, result{p.Name, "deferred: deploys when the current deployment ends"})
		default:
			results = append(results, result{p.Name, "error: " + err.Error()})
		}
	}
	slog.Info("github push", "repo", push.Repository.HTMLURL, "branch", branch,
		"commit", short(push.After), "projects", len(results))

	// Answer quickly: GitHub gives up after 10 seconds. The work is queued.
	httpx.WriteJSON(w, http.StatusAccepted, map[string]any{"deployments": results})
}

// ValidSignature checks GitHub's X-Hub-Signature-256 header.
//
// hmac.Equal compares in CONSTANT TIME. A normal == stops at the first
// different byte, so an attacker timing many requests could guess a valid
// signature one byte at a time.
func ValidSignature(secret, body []byte, header string) bool {
	got, ok := strings.CutPrefix(header, "sha256=")
	if !ok {
		return false
	}
	gotBytes, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(body)
	return hmac.Equal(gotBytes, mac.Sum(nil))
}

// sameRepo compares GitHub URLs ignoring case, a trailing slash and ".git".
func sameRepo(a, b string) bool {
	norm := func(u string) string {
		u = strings.ToLower(strings.TrimSpace(u))
		u = strings.TrimSuffix(u, "/")
		return strings.TrimSuffix(u, ".git")
	}
	return norm(a) != "" && norm(a) == norm(b)
}

func short(sha string) string {
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}
