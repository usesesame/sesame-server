package server

import (
	"errors"
	"net/http"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
)

const (
	totpIssuer       = "Sesame"
	maxUserAgent     = 256
	maxTokenLength   = 256
	invalidLoginText = "The name, password or code is incorrect."
)

type tokenRequest struct {
	Token string `json:"token"`
}

type setupRequest struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Password string `json:"password"`
	Code     string `json:"code"`

	UpdateChecks *bool `json:"updateChecks"`
}

type loginRequest struct {
	Name     string `json:"name"`
	Password string `json:"password"`
	Code     string `json:"code"`
}

type stepUpRequest struct {
	Password string `json:"password"`
	Code     string `json:"code"`
}

type ownerView struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type sessionView struct {
	Owner           ownerView `json:"owner"`
	CSRFToken       string    `json:"csrfToken"`
	RecentAuthUntil time.Time `json:"recentAuthUntil"`
	ExpiresAt       time.Time `json:"expiresAt"`
}

func viewOfSession(session selfhost.OwnerSession) sessionView {
	return sessionView{
		Owner:           ownerView{ID: session.Owner.ID, Name: session.Owner.Name},
		CSRFToken:       session.CSRFToken,
		RecentAuthUntil: stamp(session.RecentAuthUntil()),
		ExpiresAt:       stamp(session.ExpiresAt),
	}
}

func userAgent(r *http.Request) string {
	value := r.UserAgent()
	if len(value) > maxUserAgent {
		return value[:maxUserAgent]
	}
	return value
}

func validTokenShape(value string) bool {
	return len(value) >= 32 && len(value) <= maxTokenLength
}

func validCodeShape(value string) bool {
	if len(value) != 6 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func (s *server) setupDetails(w http.ResponseWriter, r *http.Request) {
	if !s.allowIP(w, r, "owner-setup-details", 10, time.Minute) {
		return
	}
	var input tokenRequest
	if !s.decode(w, r, &input, "invalid_request", "The setup request could not be read.") {
		return
	}
	if !validTokenShape(input.Token) {
		writeError(w, http.StatusBadRequest, "setup_token_invalid", "This setup link is invalid or has expired.")
		return
	}
	details, err := s.cfg.Store.SetupDetails(r.Context(), input.Token)
	if err != nil {
		if errors.Is(err, selfhost.ErrSetupTokenInvalid) || errors.Is(err, selfhost.ErrNotFound) {
			writeError(w, http.StatusBadRequest, "setup_token_invalid", "This setup link is invalid or has expired.")
			return
		}
		s.storeError(w, r, err)
		return
	}
	account := details.OwnerName
	if account == "" {
		account = "owner"
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"totpSecret": details.TOTPSecret,
		"totpUri":    authkit.TOTPURI(totpIssuer, account, details.TOTPSecret),
		"ownerName":  details.OwnerName,
		"firstOwner": details.FirstOwner,
		"expiresAt":  stamp(details.ExpiresAt),
	})
}

func (s *server) setupComplete(w http.ResponseWriter, r *http.Request) {
	if !s.allowIP(w, r, "owner-setup", 5, time.Minute) {
		return
	}
	var input setupRequest
	if !s.decode(w, r, &input, "invalid_request", "The setup request could not be read.") {
		return
	}
	name, nameOK := selfhost.NormalizeName(input.Name)
	if !validTokenShape(input.Token) || !nameOK || !selfhost.ValidPassword(input.Password) || !validCodeShape(input.Code) {
		writeError(w, http.StatusBadRequest, "invalid_setup", "Use a name of up to 64 characters, a password of 12 to 1024 characters and the current six digit code.")
		return
	}
	login, err := s.cfg.Store.CompleteSetup(r.Context(), selfhost.CompleteSetupInput{
		Token: input.Token, Name: name, Password: input.Password, Code: input.Code, UserAgent: userAgent(r), UpdateChecks: input.UpdateChecks,
	})
	switch {
	case err == nil:
	case errors.Is(err, selfhost.ErrSetupTokenInvalid), errors.Is(err, selfhost.ErrNotFound):
		writeError(w, http.StatusBadRequest, "setup_token_invalid", "This setup link is invalid or has expired.")
		return
	case errors.Is(err, selfhost.ErrInvalidCredentials), errors.Is(err, selfhost.ErrReplayedCode), errors.Is(err, selfhost.ErrInvalidInput):
		writeError(w, http.StatusBadRequest, "invalid_setup", "The code is incorrect or was already used. Wait for the next code and try again.")
		return
	case errors.Is(err, selfhost.ErrConflict):
		writeError(w, http.StatusConflict, "name_taken", "Another owner already uses that name.")
		return
	default:
		s.storeError(w, r, err)
		return
	}
	s.setSessionCookie(w, login.Token, login.Session.ExpiresAt)
	if input.UpdateChecks != nil && *input.UpdateChecks {
		s.cfg.Updates.Trigger()
	}
	writeJSON(w, http.StatusCreated, viewOfSession(login.Session))
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	var input loginRequest
	if !s.decode(w, r, &input, "invalid_request", "The sign-in request could not be read.") {
		return
	}
	name, nameOK := selfhost.NormalizeName(input.Name)
	if !s.allowIP(w, r, "owner-login-peer", 20, time.Minute) {
		return
	}
	if nameOK && !s.allowClientSubject(w, r, "owner-login", selfhost.NameKey(name), 5, time.Minute) {
		return
	}
	if !nameOK || !selfhost.ValidPassword(input.Password) || !validCodeShape(input.Code) {
		authkit.DummyVerifyPassword()
		writeError(w, http.StatusUnauthorized, "invalid_credentials", invalidLoginText)
		return
	}
	login, err := s.cfg.Store.Login(r.Context(), selfhost.LoginInput{Name: name, Password: input.Password, Code: input.Code, UserAgent: userAgent(r)})
	if err != nil {
		if errors.Is(err, selfhost.ErrInvalidCredentials) || errors.Is(err, selfhost.ErrReplayedCode) || errors.Is(err, selfhost.ErrNotFound) || errors.Is(err, selfhost.ErrInvalidInput) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", invalidLoginText)
			return
		}
		s.storeError(w, r, err)
		return
	}
	s.forgetClientSubject(r, "owner-login", selfhost.NameKey(name))
	s.setSessionCookie(w, login.Token, login.Session.ExpiresAt)
	writeJSON(w, http.StatusOK, viewOfSession(login.Session))
}

func (s *server) logout(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not_authenticated", "Sign in to continue.")
		return
	}
	if err := s.cfg.Store.Logout(r.Context(), cookie.Value); err != nil && !errors.Is(err, selfhost.ErrSessionInvalid) {
		s.storeError(w, r, err)
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) session(w http.ResponseWriter, _ *http.Request, session selfhost.OwnerSession) {
	writeJSON(w, http.StatusOK, viewOfSession(session))
}

func (s *server) stepUp(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	if !s.allowSubject(w, r, "owner-step-up", session.Owner.ID, 10, 5*time.Minute) || !s.allowIP(w, r, "owner-step-up-peer", 20, time.Minute) {
		return
	}
	var input stepUpRequest
	if !s.decode(w, r, &input, "invalid_request", "The confirmation request could not be read.") {
		return
	}
	if !selfhost.ValidPassword(input.Password) || !validCodeShape(input.Code) {
		authkit.DummyVerifyPassword()
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "The password or code is incorrect.")
		return
	}
	cookie, err := r.Cookie(s.cookieName)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "not_authenticated", "Sign in to continue.")
		return
	}
	updated, err := s.cfg.Store.StepUp(r.Context(), selfhost.StepUpInput{SessionToken: cookie.Value, Password: input.Password, Code: input.Code})
	if err != nil {
		switch {
		case errors.Is(err, selfhost.ErrInvalidCredentials), errors.Is(err, selfhost.ErrReplayedCode):
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "The password or code is incorrect.")
		case errors.Is(err, selfhost.ErrSessionInvalid):
			s.clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, "session_expired", "Your session has expired. Sign in to continue.")
		default:
			s.storeError(w, r, err)
		}
		return
	}
	writeJSON(w, http.StatusOK, viewOfSession(updated))
}
