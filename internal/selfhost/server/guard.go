package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

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
	if err := s.cfg.Store.ResetRateLimit(r.Context(), s.clientSubjectKey(operation, subject, r)); err != nil {
		requestLog(r.Context()).Error("Sesame server rate limit reset failed", "error", err)
	}
}

func (s *server) allowKey(w http.ResponseWriter, r *http.Request, key string, limit int, window time.Duration) bool {
	decision, err := s.cfg.Store.RateLimit(r.Context(), key, limit, window)
	if err != nil {
		requestLog(r.Context()).Error("Sesame server rate limit check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "rate_limit_unavailable", "The security check is temporarily unavailable.")
		return false
	}
	if decision.Allowed {
		return true
	}
	seconds := int(decision.RetryAfter.Round(time.Second) / time.Second)
	w.Header().Set("Retry-After", strconv.Itoa(max(1, seconds)))
	writeError(w, http.StatusTooManyRequests, "too_many_attempts", "Try again later.")
	return false
}

func (s *server) clientIP(r *http.Request) string {
	peer := peerIP(r)
	if !s.trustedProxy(peer) {
		if r.Header.Get("X-Forwarded-For") != "" && privatePeer(peer) && s.forwarded.CompareAndSwap(false, true) {
			requestLog(r.Context()).Warn("A request from a private address carried X-Forwarded-For but that address is not a trusted proxy, so every visitor shares one rate limit. Set SESAME_TRUSTED_PROXIES to the range your reverse proxy uses.")
		}
		return peer
	}
	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer
	}
	hops := strings.Split(forwarded, ",")
	for index := len(hops) - 1; index >= 0; index-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(hops[index]))
		if err != nil {
			return peer
		}
		ip = ip.Unmap()
		if s.trustedProxy(ip.String()) {
			continue
		}
		return ip.String()
	}
	return peer
}

func (s *server) clientLimitKey(r *http.Request) string {
	client := s.clientIP(r)
	address, err := netip.ParseAddr(client)
	if err != nil {
		return client
	}
	address = address.Unmap()
	if !address.Is6() {
		return address.String()
	}
	prefix, err := address.Prefix(64)
	if err != nil {
		return client
	}
	return prefix.String()
}

func (s *server) trustedProxy(value string) bool {
	ip, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range s.cfg.TrustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func (s *server) forwardedFromUntrusted() bool { return s.forwarded.Load() }

func peerIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

func privatePeer(value string) bool {
	ip, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()
}
