package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"usesesame.app/backend/internal/clientaddr"
	"usesesame.app/backend/internal/selfhost"
)

type ownerHandler func(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession)

func isUnsafeMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func (s *server) hostGuard(w http.ResponseWriter, r *http.Request) bool {
	if canonicalHost(r.Host, s.cfg.PublicURL.Secure) == s.publicHost {
		return true
	}
	writeError(w, http.StatusMisdirectedRequest, "host_mismatch", "This server accepts sign-in and pairing requests only at "+s.publicHost+".")
	return false
}

func (s *server) browserOrigin(w http.ResponseWriter, r *http.Request) bool {
	if isUnsafeMethod(r.Method) {
		if r.Header.Get("Origin") != s.cfg.PublicURL.Origin {
			writeError(w, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame console.")
			return false
		}
		return true
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.cfg.PublicURL.Origin {
		writeError(w, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame console.")
		return false
	}
	return true
}

func (s *server) ownerOpen(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.hostGuard(w, r) && s.browserOrigin(w, r) {
			next(w, r)
		}
	}
}

func (s *server) owner(next ownerHandler) http.HandlerFunc {
	return s.ownerOpen(func(w http.ResponseWriter, r *http.Request) {
		session, ok := s.authenticate(w, r)
		if ok {
			next(w, r, session)
		}
	})
}

func (s *server) ownerStepUp(next ownerHandler) http.HandlerFunc {
	return s.owner(func(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
		if !session.RecentAuth(s.now()) {
			writeError(w, http.StatusForbidden, "step_up_required", "Confirm your password and a current code to continue.")
			return
		}
		next(w, r, session)
	})
}

func (s *server) authenticate(w http.ResponseWriter, r *http.Request) (selfhost.OwnerSession, bool) {
	cookie, err := r.Cookie(s.cookieName)
	if err != nil || cookie.Value == "" {
		writeError(w, http.StatusUnauthorized, "not_authenticated", "Sign in to continue.")
		return selfhost.OwnerSession{}, false
	}
	session, err := s.cfg.Store.Session(r.Context(), cookie.Value)
	if err != nil {
		if errors.Is(err, selfhost.ErrSessionInvalid) {
			s.clearSessionCookie(w)
			writeError(w, http.StatusUnauthorized, "session_expired", "Your session has expired. Sign in to continue.")
			return selfhost.OwnerSession{}, false
		}
		s.storeError(w, r, err)
		return selfhost.OwnerSession{}, false
	}
	if isUnsafeMethod(r.Method) && !validCSRF(r.Header.Get("X-Sesame-CSRF"), session.CSRFToken) {
		writeError(w, http.StatusForbidden, "invalid_csrf", "Reload the console and try again.")
		return selfhost.OwnerSession{}, false
	}
	return session, true
}

func validCSRF(header, expected string) bool {
	if header == "" || expected == "" || len(header) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

func (s *server) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	maxAge := int(expires.Sub(s.now()).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   s.cfg.PublicURL.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     s.cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.PublicURL.Secure,
		SameSite: http.SameSiteStrictMode,
	})
}

func (s *server) digest(scope, value string) string {
	mac := hmac.New(sha256.New, s.cfg.IPPepper)
	mac.Write([]byte(scope))
	mac.Write([]byte{0})
	mac.Write([]byte(value))
	return hex.EncodeToString(mac.Sum(nil))[:32]
}

func (s *server) allowIP(w http.ResponseWriter, r *http.Request, operation string, limit int, window time.Duration) bool {
	return s.allowKey(w, r, operation+":ip:"+s.digest("ip", s.clientLimitKey(r)), limit, window)
}

func (s *server) allowSubject(w http.ResponseWriter, r *http.Request, operation, subject string, limit int, window time.Duration) bool {
	return s.allowKey(w, r, operation+":subject:"+s.digest("subject", strings.ToLower(strings.TrimSpace(subject))), limit, window)
}

func (s *server) clientSubjectKey(operation, subject string, r *http.Request) string {
	return operation + ":client-subject:" + s.digest("client-subject", strings.ToLower(strings.TrimSpace(subject))+"\x00"+s.clientLimitKey(r))
}

func (s *server) allowClientSubject(w http.ResponseWriter, r *http.Request, operation, subject string, limit int, window time.Duration) bool {
	return s.allowKey(w, r, s.clientSubjectKey(operation, subject, r), limit, window)
}

func (s *server) forgetClientSubject(r *http.Request, operation, subject string) {
	key := s.clientSubjectKey(operation, subject, r)
	s.refused.forget(key)
	if err := s.cfg.Store.ResetRateLimit(r.Context(), key); err != nil {
		requestLog(r.Context()).Error("Sesame server rate limit reset failed", "error", err)
	}
}

func (s *server) allowKey(w http.ResponseWriter, r *http.Request, key string, limit int, window time.Duration) bool {
	now := s.now()
	if remaining, refused := s.refused.active(key, now); refused {
		writeTooManyAttempts(w, remaining)
		return false
	}
	decision, err := s.cfg.Store.RateLimit(r.Context(), key, limit, window)
	if err != nil {
		requestLog(r.Context()).Error("Sesame server rate limit check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "The security check is temporarily unavailable.")
		return false
	}
	if decision.Allowed {
		return true
	}
	s.refused.remember(key, now.Add(decision.RetryAfter), now)
	writeTooManyAttempts(w, decision.RetryAfter)
	return false
}

func writeTooManyAttempts(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(retryAfter.Round(time.Second) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(max(1, seconds)))
	writeError(w, http.StatusTooManyRequests, "too_many_attempts", "Try again later.")
}

func (s *server) clientLimitKey(r *http.Request) string {
	address := clientaddr.Resolve(r, s.cfg.TrustedProxies)
	if address.ForwardingIgnored && privatePeer(address.Peer) && s.forwarded.CompareAndSwap(false, true) {
		requestLog(r.Context()).Warn("A request from a private address carried X-Forwarded-For but that address is not a trusted proxy, so every visitor shares one rate limit. Set SESAME_TRUSTED_PROXIES to the range your reverse proxy uses.")
	}
	return address.LimitKey()
}

func (s *server) forwardedFromUntrusted() bool { return s.forwarded.Load() }

func privatePeer(value string) bool {
	ip, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
