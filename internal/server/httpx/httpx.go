// Package httpx provides the small HTTP toolkit used by all server modules: handlers
// that return errors, a uniform JSON error format, strict JSON decoding and middleware.
package httpx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"opensignage/internal/contracts"
)

// Error is an API error with an HTTP status and a stable code the UI translates
// (`errors.<code>`). Message is an English fallback for logs and tools.
type Error struct {
	Status  int            `json:"-"`
	Code    string         `json:"code"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params,omitempty"`
	cause   error
}

func (e *Error) Error() string {
	if e.cause != nil {
		return e.Code + ": " + e.Message + ": " + e.cause.Error()
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.cause }

// E creates an API error.
func E(status int, code, message string) *Error {
	return &Error{Status: status, Code: code, Message: message}
}

// With adds a parameter used by the UI message template.
func (e *Error) With(key string, v any) *Error {
	c := *e
	c.Params = map[string]any{}
	for k, val := range e.Params {
		c.Params[k] = val
	}
	c.Params[key] = v
	return &c
}

// Wrap attaches an internal cause (logged, never sent to the client).
func (e *Error) Wrap(err error) *Error {
	c := *e
	c.cause = err
	return &c
}

// Common errors.
var (
	ErrBadRequest   = E(http.StatusBadRequest, "bad_request", "invalid request")
	ErrUnauthorized = E(http.StatusUnauthorized, "unauthorized", "login required")
	ErrForbidden    = E(http.StatusForbidden, "forbidden", "not allowed")
	ErrNotFound     = E(http.StatusNotFound, "not_found", "not found")
	ErrConflict     = E(http.StatusConflict, "conflict", "the object was changed by someone else")
	ErrTooLarge     = E(http.StatusRequestEntityTooLarge, "too_large", "request too large")
	ErrInternal     = E(http.StatusInternalServerError, "internal", "internal error")
)

// Invalid creates a 422 validation error for one field.
func Invalid(field, code string) *Error {
	return E(http.StatusUnprocessableEntity, "invalid_field", "invalid field "+field).
		With("field", field).With("reason", code)
}

// HandlerFunc returns an error instead of writing it.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// WriteError writes err in the uniform format and logs server-side problems.
func WriteError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, context.Canceled) && r.Context().Err() != nil {
		// The browser went away or the server is shutting down (update, restart): nobody
		// waits for the answer, and it is no server error.
		slog.Debug("request canceled", "method", r.Method, "path", r.URL.Path)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var apiErr *Error
	var fe *contracts.FieldError
	var de *contracts.DocumentError
	var mbe *http.MaxBytesError
	switch {
	case errors.As(err, &apiErr):
	case errors.As(err, &fe):
		apiErr = Invalid(fe.Field, fe.Code)
	case errors.As(err, &de):
		apiErr = E(http.StatusUnprocessableEntity, "invalid_document", "invalid document").
			With("problems", de.Problems)
	case errors.As(err, &mbe):
		apiErr = ErrTooLarge
	default:
		apiErr = ErrInternal.Wrap(err)
	}
	level := slog.LevelDebug
	if apiErr.Status >= 500 {
		level = slog.LevelError
	}
	slog.Log(r.Context(), level, "request failed", "method", r.Method, "path", r.URL.Path,
		"status", apiErr.Status, "code", apiErr.Code, "err", err.Error())
	WriteJSON(w, apiErr.Status, map[string]any{"error": apiErr})
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if v == nil {
		return
	}
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Debug("write json", "err", err)
	}
}

// OK writes a 200 JSON response and returns nil (for `return httpx.OK(w, v)`).
func OK(w http.ResponseWriter, v any) error {
	WriteJSON(w, http.StatusOK, v)
	return nil
}

// NoContent writes 204.
func NoContent(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Decode strictly decodes a JSON body (unknown fields rejected) of at most maxBytes.
func Decode(w http.ResponseWriter, r *http.Request, maxBytes int64, v any) error {
	if maxBytes <= 0 {
		maxBytes = 1 << 20
	}
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return E(http.StatusUnsupportedMediaType, "unsupported_media_type", "expected application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return ErrTooLarge
		}
		return ErrBadRequest.With("detail", err.Error()).Wrap(err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return ErrBadRequest.With("detail", "trailing data")
	}
	return nil
}

// Recover turns panics into 500 responses; one bad request never stops the server.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if p := recover(); p != nil {
				if p == http.ErrAbortHandler {
					panic(p)
				}
				slog.Error("panic in handler", "path", r.URL.Path, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
				WriteError(w, r, ErrInternal)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// SecurityHeaders sets conservative headers for the WebGUIs.
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=31536000")
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; media-src 'self' blob:; "+
			"style-src 'self' 'unsafe-inline'; font-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; object-src 'none'")
		next.ServeHTTP(w, r)
	})
}

// Adapt converts a HandlerFunc into an http.Handler.
func Adapt(h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(w, r, err)
		}
	})
}

// QueryInt reads an integer query parameter with default and bounds.
func QueryInt(r *http.Request, name string, def, lo, hi int) int {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return max(lo, min(hi, n))
}

// ClientIP returns the remote IP without port.
func ClientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return strings.Trim(host, "[]")
}

// Timeout bounds a handler's context.
func Timeout(d time.Duration, next http.Handler) http.Handler {
	return http.TimeoutHandler(next, d, `{"error":{"code":"timeout","message":"request timed out"}}`)
}
