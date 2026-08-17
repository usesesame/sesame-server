package httpapi

import (
	"container/list"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

const (
	identityGuessLimit  = 10
	identityGuessWindow = 5 * time.Minute
	identityMailLimit   = 5
	identityMailWindow  = time.Hour
	recoveryPeerLimit   = 2
)

type authLimiter struct {
	mu       sync.Mutex
	attempts map[string]*limitEntry
	recency  *list.List
}

type limitEntry struct {
	attempts []time.Time
	element  *list.Element
}

func (a *api) userForRequest(response http.ResponseWriter, request *http.Request) (accounts.User, bool) {
	cookie, err := request.Cookie(a.sessionCookieName())
	if err != nil || cookie.Value == "" {
		writeError(response, http.StatusUnauthorized, "not_authenticated", "Sign in to continue.")
		return accounts.User{}, false
	}
	tokenHash := accounts.HashSessionToken(cookie.Value)
	var user accounts.User
	if store, ok := a.config.Accounts.(accounts.AccountSecurityStore); ok {
		user, _, err = store.SessionForToken(request.Context(), tokenHash)
	} else {
		user, err = a.config.Accounts.UserBySession(request.Context(), tokenHash)
	}
	if err != nil {
		a.clearSessionCookie(response)
		writeError(response, http.StatusUnauthorized, "session_expired", "Your session has expired. Sign in to continue.")
		return accounts.User{}, false
	}
	return user, true
}

func (a *api) desktopConnectionForRequest(response http.ResponseWriter, request *http.Request) (accounts.DesktopConnection, bool) {
	token, ok := desktopToken(response, request)
	if !ok {
		return accounts.DesktopConnection{}, false
	}
	store, ok := a.config.Accounts.(accounts.DesktopStore)
	if !ok {
		writeError(response, http.StatusServiceUnavailable, "desktop_link_unavailable", "Desktop linking is not configured.")
		return accounts.DesktopConnection{}, false
	}
	connection, err := store.DesktopConnectionForToken(request.Context(), accounts.HashSessionToken(token))
	if err != nil {
		writeError(response, http.StatusUnauthorized, "not_authenticated", "This desktop is no longer linked.")
		return accounts.DesktopConnection{}, false
	}
	if connection.AccountSuspended {
		writeError(response, http.StatusLocked, "account_suspended", "This Sesame account is suspended. The desktop connection will resume after the account is restored.")
		return accounts.DesktopConnection{}, false
	}
	return connection, true
}

func desktopToken(response http.ResponseWriter, request *http.Request) (string, bool) {
	value := request.Header.Get("Authorization")
	if !strings.HasPrefix(value, "Sesame ") || len(value) <= len("Sesame ") {
		writeError(response, http.StatusUnauthorized, "not_authenticated", "This desktop is not linked.")
		return "", false
	}
	return strings.TrimPrefix(value, "Sesame "), true
}

func (a *api) requireAccounts(response http.ResponseWriter) bool {
	if a.config.Accounts != nil {
		return true
	}
	writeError(response, http.StatusServiceUnavailable, "accounts_unavailable", "Website accounts are not configured.")
	return false
}

func (a *api) allowAuthAttempt(response http.ResponseWriter, request *http.Request, operation string) bool {
	return a.allowRequest(response, request, operation, 8, time.Minute)
}

func (a *api) allowRequest(response http.ResponseWriter, request *http.Request, operation string, limit int, window time.Duration) bool {
	return a.allowKeyed(response, request, operation+":"+a.clientIP(request), limit, window)
}

func (a *api) allowKeyed(response http.ResponseWriter, request *http.Request, key string, limit int, window time.Duration) bool {
	allowed, retryAfter, err := a.consumeKeyed(request, key, limit, window)
	if err != nil {
		writeError(response, http.StatusServiceUnavailable, "rate_limit_unavailable", "The security check is temporarily unavailable.")
		return false
	}
	if allowed {
		return true
	}
	response.Header().Set("Retry-After", strconv.Itoa(max(1, int(retryAfter.Round(time.Second)/time.Second))))
	writeError(response, http.StatusTooManyRequests, "too_many_attempts", "Try again later.")
	return false
}

// Lets recovery spend its budget without letting the status code disclose
// whether an address has an account.
func (a *api) consumeKeyed(request *http.Request, key string, limit int, window time.Duration) (bool, time.Duration, error) {
	if shared, ok := a.config.Accounts.(accounts.RateLimitStore); ok {
		return shared.ConsumeRateLimit(request.Context(), key, limit, window)
	}
	return a.limits.allow(key, limit, window), window, nil
}

// The identity is peppered and hashed, so the rate-limit table never stores
// an address or account id in the clear.
func (a *api) allowIdentity(response http.ResponseWriter, request *http.Request, operation, identity string, limit int, window time.Duration) bool {
	return a.allowKeyed(response, request, identityBudgetKey(operation, identity, a.config.AdminIPPepper), limit, window)
}

func (a *api) identityWithinBudget(request *http.Request, operation, identity string, limit int, window time.Duration) bool {
	allowed, _, err := a.consumeKeyed(request, identityBudgetKey(operation, identity, a.config.AdminIPPepper), limit, window)
	return allowed && err == nil
}

func identityBudgetKey(operation, identity, pepper string) string {
	digest := adminstore.HashIP(strings.ToLower(strings.TrimSpace(identity)), pepper)
	return operation + ":subject:" + digest[:32]
}

func (l *authLimiter) allow(key string, limit int, window time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-window)
	entry := l.attempts[key]
	if entry == nil {
		if len(l.attempts) >= 4096 {
			oldest := l.recency.Back()
			if oldest != nil {
				delete(l.attempts, oldest.Value.(string))
				l.recency.Remove(oldest)
			}
		}
		element := l.recency.PushFront(key)
		entry = &limitEntry{element: element}
		l.attempts[key] = entry
	} else {
		l.recency.MoveToFront(entry.element)
	}
	kept := entry.attempts[:0]
	for _, attempt := range entry.attempts {
		if attempt.After(cutoff) {
			kept = append(kept, attempt)
		}
	}
	if len(kept) >= limit {
		entry.attempts = kept
		return false
	}
	entry.attempts = append(kept, now)
	return true
}

func decodeAuthRequest(response http.ResponseWriter, request *http.Request) (authRequest, bool) {
	var input authRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_request", "Account details could not be read.") {
		return authRequest{}, false
	}
	return input, true
}

func decodePasswordChange(response http.ResponseWriter, request *http.Request) (passwordChangeRequest, bool) {
	var input passwordChangeRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_request", "Account details could not be read.") {
		return passwordChangeRequest{}, false
	}
	return input, true
}

func decodeAccountDelete(response http.ResponseWriter, request *http.Request) (accountDeleteRequest, bool) {
	var input accountDeleteRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_request", "Account details could not be read.") {
		return accountDeleteRequest{}, false
	}
	return input, true
}

func decodeJSONBodyWith(response http.ResponseWriter, request *http.Request, target any, code, message string) bool {
	return decodeJSONBodyLimited(response, request, target, code, message, maxAuthBodyBytes)
}

func decodeJSONBodyLimited(response http.ResponseWriter, request *http.Request, target any, code, message string, limit int64) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(response, http.StatusUnsupportedMediaType, "json_required", "Send account details as JSON.")
		return false
	}
	request.Body = http.MaxBytesReader(response, request.Body, limit)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		writeError(response, http.StatusBadRequest, code, message)
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeError(response, http.StatusBadRequest, code, message)
		return false
	}
	return true
}

func decodeDesktopLinkRequest(response http.ResponseWriter, request *http.Request) (desktopLinkRequest, bool) {
	var input desktopLinkRequest
	if !decodeJSONBodyWith(response, request, &input, "invalid_desktop_link", "Desktop link details could not be read.") {
		return desktopLinkRequest{}, false
	}
	return input, true
}

func hostOnlySecureCookieName(plain, domain string, secure bool) string {
	if secure && domain == "" {
		return "__Host-" + plain
	}
	return plain
}

func (a *api) sessionCookieName() string {
	return hostOnlySecureCookieName(sessionCookieName, a.config.SessionDomain, a.config.SessionSecure)
}

func (a *api) csrfCookieName() string {
	return hostOnlySecureCookieName(csrfCookieName, a.config.SessionDomain, a.config.SessionSecure)
}

func (a *api) passkeyCeremonyCookieName() string {
	return hostOnlySecureCookieName(passkeyCeremonyCookie, a.config.SessionDomain, a.config.SessionSecure)
}

func (a *api) setSessionCookie(response http.ResponseWriter, token string) {
	http.SetCookie(response, &http.Cookie{
		Name:     a.sessionCookieName(),
		Value:    token,
		Path:     "/",
		Domain:   a.config.SessionDomain,
		MaxAge:   int(a.config.SessionDuration.Seconds()),
		HttpOnly: true,
		Secure:   a.config.SessionSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (a *api) clearSessionCookie(response http.ResponseWriter) {
	http.SetCookie(response, &http.Cookie{Name: a.sessionCookieName(), Value: "", Path: "/", Domain: a.config.SessionDomain, MaxAge: -1, HttpOnly: true, Secure: a.config.SessionSecure, SameSite: http.SameSiteLaxMode})
}

func (a *api) notFound(response http.ResponseWriter, request *http.Request) {
	writeError(response, http.StatusNotFound, "not_found", "The requested API endpoint does not exist.")
}

func allowMethod(response http.ResponseWriter, request *http.Request, allowed string) bool {
	if request.Method == allowed {
		return true
	}
	response.Header().Set("Allow", allowed+", OPTIONS")
	writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
	return false
}

func isUnsafeMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func (a *api) clientIP(request *http.Request) string {
	peer := requestIP(request)
	if a.isTrustedProxy(peer) {
		if forwarded := request.Header.Get("X-Forwarded-For"); forwarded != "" {
			first := strings.TrimSpace(strings.SplitN(forwarded, ",", 2)[0])
			if ip, err := netip.ParseAddr(first); err == nil {
				return ip.Unmap().String()
			}
		}
	}
	return peer
}

func (a *api) isTrustedProxy(value string) bool {
	ip, err := netip.ParseAddr(value)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, prefix := range a.config.TrustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}
	return false
}

func requestIP(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return request.RemoteAddr
	}
	return host
}

func writeError(response http.ResponseWriter, status int, code, message string) {
	writeJSON(response, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func writeJSON(response http.ResponseWriter, status int, value any) {
	response.Header().Set("Content-Type", "application/json; charset=utf-8")
	response.WriteHeader(status)
	if err := json.NewEncoder(response).Encode(value); err != nil {
		slog.Error("Sesame API response encoding failed", "error", err, "status", status)
	}
}
