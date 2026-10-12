package httpx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A request whose browser went away (or that the shutdown of an update ends) is no server
// error: no ERROR log line, no JSON error body. A real failure still is one.
func TestCanceledRequestIsNoServerError(t *testing.T) {
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil).WithContext(ctx)
	w := httptest.NewRecorder()
	WriteError(w, r, fmt.Errorf("query: %w", context.Canceled))
	if w.Code != http.StatusServiceUnavailable || w.Body.Len() != 0 || strings.Contains(logs.String(), "ERROR") {
		t.Fatalf("canceled request: %d %q, log %q", w.Code, w.Body.String(), logs.String())
	}

	// The same error while the request is still alive is a real failure.
	r = httptest.NewRequest(http.MethodGet, "/api/v1/alerts", nil)
	w = httptest.NewRecorder()
	WriteError(w, r, errors.New("disk on fire"))
	if w.Code != http.StatusInternalServerError || !strings.Contains(logs.String(), "level=ERROR") {
		t.Fatalf("real failure: %d, log %q", w.Code, logs.String())
	}
}
