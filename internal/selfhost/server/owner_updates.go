package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"usesesame.app/backend/internal/selfhost"
	"usesesame.app/backend/internal/selfhost/updates"
)

const (
	updateChecksPerHour = 3
	updateCheckWindow   = time.Hour
)

type updatesRequest struct {
	Enabled *bool   `json:"enabled"`
	Channel *string `json:"channel"`
}

func (s *server) getUpdates(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	status, err := s.cfg.Updates.Status(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *server) patchUpdates(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	var input updatesRequest
	if !s.decode(w, r, &input, "invalid_updates", "The update settings could not be read.") {
		return
	}
	if input.Enabled == nil && input.Channel == nil {
		writeError(w, http.StatusBadRequest, "invalid_updates", "Send enabled, channel or both.")
		return
	}
	status, err := s.cfg.Updates.Configure(detached(r), actorOf(session), selfhost.UpdateChange{Enabled: input.Enabled, Channel: input.Channel})
	if errors.Is(err, selfhost.ErrInvalidInput) {
		writeError(w, http.StatusBadRequest, "invalid_updates", "That update channel does not exist.")
		return
	}
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *server) checkUpdates(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	if !s.allowSubject(w, r, "owner-update-check", session.Owner.ID, updateChecksPerHour, updateCheckWindow) {
		return
	}
	status, err := s.cfg.Updates.Check(detached(r))
	if errors.Is(err, updates.ErrOff) {
		writeError(w, http.StatusConflict, "updates_off", "Turn on update checks before checking for updates.")
		return
	}
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func detached(r *http.Request) context.Context {
	return context.WithoutCancel(r.Context())
}
