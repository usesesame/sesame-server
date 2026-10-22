package syncv3http

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"usesesame.app/backend/internal/syncsvc"
)

func TestAnUnexpectedServiceFailureIsLoggedForTheOperatorAndAnsweredWithoutDetail(t *testing.T) {
	var logged bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logged, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	recorder := httptest.NewRecorder()
	writeServiceError(recorder, errors.New("database is locked fictional-detail"))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "fictional-detail") {
		t.Fatalf("the response leaks the failure detail: %s", recorder.Body.String())
	}
	if !strings.Contains(logged.String(), "fictional-detail") {
		t.Fatalf("the failure was not logged: %q", logged.String())
	}

	logged.Reset()
	writeServiceError(httptest.NewRecorder(), syncsvc.ErrConflict)
	if logged.Len() != 0 {
		t.Fatalf("an expected refusal must not be logged as a failure: %q", logged.String())
	}
}
