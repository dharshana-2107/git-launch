package deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"launchpad/api/internal/httpx"
)

type Handler struct {
	store   Store
	worker  *Worker
	broker  *Broker
	runtime *Runtime
}

func NewHandler(store Store, worker *Worker, broker *Broker, runtime *Runtime) *Handler {
	return &Handler{store: store, worker: worker, broker: broker, runtime: runtime}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/projects/{id}/deployments", h.create)
	mux.HandleFunc("GET /api/projects/{id}/deployments", h.list)
	mux.HandleFunc("GET /api/deployments/{id}", h.get)
	mux.HandleFunc("GET /api/deployments/{id}/events", h.events)
	mux.HandleFunc("POST /api/deployments/{id}/redeploy", h.redeploy)
	mux.HandleFunc("GET /api/projects/{id}/app-logs", h.appLogs)
}

// create records a queued deployment and hands it to the worker.
// It answers 202 Accepted: "received, the work happens later".
func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := h.worker.Deploy(r.Context(), NewDeployment{ProjectID: projectID, Trigger: TriggerManual})
	if err != nil {
		deployError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, d)
}

// redeploy runs an earlier deployment's image again, without rebuilding:
//   - an older deployment  -> a ROLLBACK to that version
//   - the live deployment  -> a CONFIG restart (picks up changed env vars)
func (h *Handler) redeploy(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	src, err := h.store.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	// Only a deployment that went live has an image known to work.
	if src.ImageTag == "" || src.ImagePruned || (src.Status != StatusLive && src.Status != StatusStopped) {
		httpx.WriteError(w, http.StatusConflict, ErrNoImage.Error())
		return
	}

	trigger := TriggerRollback
	if src.Status == StatusLive {
		trigger = TriggerConfig
	}
	d, err := h.worker.Deploy(r.Context(), NewDeployment{ProjectID: src.ProjectID, Trigger: trigger, Source: &src})
	if err != nil {
		deployError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusAccepted, d)
}

// appLogs returns what the project's running app printed recently
// (runtime logs, as opposed to build logs).
func (h *Handler) appLogs(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(w, r)
	if !ok {
		return
	}
	live, err := h.store.LiveByProject(r.Context(), projectID)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, "the project has no live deployment")
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	tail := 200
	if n, err := strconv.Atoi(r.URL.Query().Get("tail")); err == nil && n > 0 && n <= 2000 {
		tail = n
	}
	lines, err := h.runtime.AppLogs(r.Context(), live.ContainerName, tail)
	if err != nil {
		httpx.WriteError(w, http.StatusBadGateway, "could not read the app's logs (is the container running?)")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"container": live.ContainerName,
		"lines":     lines,
	})
}

// deployError maps Deploy's errors to status codes.
func deployError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrProjectNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrAlreadyActive), errors.Is(err, ErrAppLimit):
		httpx.WriteError(w, http.StatusConflict, err.Error())
	case errors.Is(err, ErrQueueFull):
		httpx.WriteError(w, http.StatusServiceUnavailable, err.Error())
	default:
		serverError(w, err)
	}
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	projectID, ok := pathID(w, r)
	if !ok {
		return
	}
	list, err := h.store.ListByProject(r.Context(), projectID, 20)
	if err != nil {
		serverError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, list)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	d, err := h.store.Get(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// events streams a deployment's logs and status changes with Server-Sent
// Events (SSE): one long HTTP response the server keeps writing to.
//
// Wire format, one event per blank-line-separated block:
//
//	id: 12
//	event: log
//	data: {"line":12,"text":"Step 3/7 ..."}
//
// Order matters, to never miss or repeat a line:
//  1. SUBSCRIBE to live events first,
//  2. then read the status,
//  3. then send the stored lines (history),
//  4. then forward live events, skipping lines already sent.
//
// Lines printed between steps 1 and 3 arrive both from the database and from
// the channel; the line number lets us drop the duplicate.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpx.WriteError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	// On reconnect the browser sends the id of the last event it received.
	lastSent, _ := strconv.Atoi(r.Header.Get("Last-Event-ID"))

	live, unsubscribe := h.broker.Subscribe(id) // step 1
	defer unsubscribe()

	d, err := h.store.Get(r.Context(), id) // step 2
	if errors.Is(err, ErrNotFound) {
		httpx.WriteError(w, http.StatusNotFound, err.Error())
		return
	}
	if err != nil {
		serverError(w, err)
		return
	}
	history, err := h.store.Logs(r.Context(), id, lastSent) // step 3
	if err != nil {
		serverError(w, err)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	for _, l := range history {
		writeEvent(w, "log", l.Line, LogLine{Line: l.Line, Text: l.Text})
		lastSent = l.Line
	}
	writeEvent(w, "status", 0, Event{Type: "status", Status: d.Status, Error: d.Error})
	flusher.Flush()

	// Already finished: every line is in the history we just sent.
	if d.Status.Final() {
		writeEvent(w, "done", 0, struct{}{})
		flusher.Flush()
		return
	}

	// A comment line every 15 s keeps proxies from closing an idle connection.
	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for { // step 4
		select {
		case <-r.Context().Done(): // the browser went away
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case e, open := <-live:
			if !open {
				return // dropped as a slow subscriber; the browser reconnects
			}
			switch e.Type {
			case "log":
				if e.Line <= lastSent {
					continue // already sent from history
				}
				writeEvent(w, "log", e.Line, LogLine{Line: e.Line, Text: e.Text})
				lastSent = e.Line
			case "status":
				writeEvent(w, "status", 0, e)
				if e.Status.Final() {
					writeEvent(w, "done", 0, struct{}{})
					flusher.Flush()
					return
				}
			}
			flusher.Flush()
		}
	}
}

// writeEvent writes one SSE event. id > 0 adds an "id:" line, which the
// browser remembers and sends back as Last-Event-ID when it reconnects.
func writeEvent(w http.ResponseWriter, event string, id int, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		slog.Error("encode event", "err", err)
		return
	}
	if id > 0 {
		fmt.Fprintf(w, "id: %d\n", id)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
}

func pathID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "id must be a positive number")
		return 0, false
	}
	return id, true
}

func serverError(w http.ResponseWriter, err error) {
	slog.Error("request failed", "err", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal error")
}
