// Package httpx holds small JSON helpers shared by every HTTP handler,
// so each handler stays focused on its own logic.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

// ErrorBody is the one shape every error response uses, so the frontend
// can always read .error (and .fields for validation problems).
type ErrorBody struct {
	Error  string            `json:"error"`
	Fields map[string]string `json:"fields,omitempty"`
}

// WriteJSON sends v as JSON with the given status code.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// Headers are already sent, so all we can do is log it.
		slog.Error("write json", "err", err)
	}
}

// WriteError sends {"error": msg} with the given status code.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, ErrorBody{Error: msg})
}

// maxBody caps request bodies at 1 MB so a huge upload cannot exhaust memory.
const maxBody = 1 << 20

// DecodeJSON reads the request body into dst. It rejects unknown fields
// (catches typos like "sub_domain"; note that names match case-insensitively) and bodies holding more than one JSON value.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("request body is empty")
		}
		return fmt.Errorf("invalid JSON: %w", err)
	}
	if dec.More() {
		return errors.New("request body must hold a single JSON object")
	}
	return nil
}
