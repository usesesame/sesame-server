package server

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const maxBodyBytes = 16 * 1024

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		slog.Error("Sesame server response encoding failed", "requestId", w.Header().Get(requestIDHeader), "status", status, "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *server) decode(w http.ResponseWriter, r *http.Request, target any, code, message string) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "json_required", "Send the request as JSON.")
		return false
	}
	if r.ContentLength > maxBodyBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The request is larger than this server accepts.")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		s.decodeFailure(w, err, code, message)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		s.decodeFailure(w, err, code, message)
		return false
	}
	return true
}

func (s *server) decodeFailure(w http.ResponseWriter, err error, code, message string) {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "The request is larger than this server accepts.")
		return
	}
	writeError(w, http.StatusBadRequest, code, message)
}

func (s *server) storeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, selfhost.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "That record does not exist.")
	case errors.Is(err, selfhost.ErrLastOwner):
		writeError(w, http.StatusConflict, "last_owner", "The last active owner cannot be removed.")
	case errors.Is(err, selfhost.ErrConflict):
		writeError(w, http.StatusConflict, "conflict", "That name or record is already in use.")
	case errors.Is(err, selfhost.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_request", "The request contains a value this server does not accept.")
	default:
		requestLog(r.Context()).Error("Sesame server store call failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "unavailable", "The server could not complete that action and no change was committed.")
	}
}

func stamp(value time.Time) time.Time { return value.UTC().Truncate(time.Second) }

func stampPointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	converted := stamp(*value)
	return &converted
}
