package project

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"launchpad/api/internal/httpx"
)

// Handler turns HTTP requests into Store calls and Store results into HTTP
// responses. It holds no state of its own besides the Store.
type Handler struct {
	store Store

	// OnDelete, if set, runs after a project is deleted. main.go uses it to
	// stop the project's containers and remove its route. A hook keeps this
	// package independent of the deployment package.
	OnDelete func(ctx context.Context, projectID int64)
}

func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// Register adds the project routes to the router.
//
// Go 1.22+ routing understands the HTTP method and path parameters
// ("GET /api/projects/{id}"), so no third-party router is needed.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/projects", h.list)
	mux.HandleFunc("POST /api/projects", h.create)
	mux.HandleFunc("GET /api/projects/{id}", h.get)
	mux.HandleFunc("PUT /api/projects/{id}", h.update)
	mux.HandleFunc("DELETE /api/projects/{id}", h.delete)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	projects, err := h.store.List(r.Context())
	if err != nil {
		h.serverError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, projects)
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	p, err := h.store.Get(r.Context(), id)
	if err != nil {
		h.storeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	in, ok := readInput(w, r)
	if !ok {
		return
	}
	p, err := h.store.Create(r.Context(), in)
	if err != nil {
		h.storeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, p) // 201 = a new resource was created
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	in, ok := readInput(w, r)
	if !ok {
		return
	}
	p, err := h.store.Update(r.Context(), id, in)
	if err != nil {
		h.storeError(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, p)
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if err := h.store.Delete(r.Context(), id); err != nil {
		h.storeError(w, err)
		return
	}
	if h.OnDelete != nil {
		h.OnDelete(r.Context(), id)
	}
	w.WriteHeader(http.StatusNoContent) // 204 = success, nothing to return
}

// --- helpers ---------------------------------------------------------------

// parseID reads {id} from the path. On failure it writes a 400 and returns ok=false,
// so the caller just returns.
func parseID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		httpx.WriteError(w, http.StatusBadRequest, "id must be a positive number")
		return 0, false
	}
	return id, true
}

// readInput decodes, normalises and validates the body.
// Bad JSON -> 400; invalid fields -> 422 with one message per field.
func readInput(w http.ResponseWriter, r *http.Request) (Input, bool) {
	var in Input
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, err.Error())
		return in, false
	}
	in.Normalize()
	if errs := in.Validate(); len(errs) > 0 {
		httpx.WriteJSON(w, http.StatusUnprocessableEntity, httpx.ErrorBody{
			Error:  "validation failed",
			Fields: errs,
		})
		return in, false
	}
	return in, true
}

// storeError maps the store's known errors to status codes.
func (h *Handler) storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httpx.WriteError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, ErrSubdomainTaken):
		httpx.WriteJSON(w, http.StatusConflict, httpx.ErrorBody{
			Error:  err.Error(),
			Fields: map[string]string{"subdomain": err.Error()},
		})
	default:
		h.serverError(w, err)
	}
}

// serverError logs the real error but tells the client only "internal error",
// so database details never leak in a response.
func (h *Handler) serverError(w http.ResponseWriter, err error) {
	slog.Error("request failed", "err", err)
	httpx.WriteError(w, http.StatusInternalServerError, "internal error")
}
