package httpapi

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strings"
)

// Every route declares the single audience that may reach it at registration,
// next to the registration itself. Origin policy, CSRF requirement, and body
// acceptance are functions of that audience crossed with method safety, so the
// middleware resolves one policy per request instead of classifying paths.

type routeAudience uint8

const (
	audienceUnknown routeAudience = iota
	audiencePublicMetadata
	audienceWebsiteSession
	audienceAdminConsole
	audienceDesktopClient
	audienceReleasePipeline
)

type routePolicy struct {
	audience routeAudience
	// Must arrive from the website's own origin even though the method is
	// safe, because the session cookie rides along unconditionally.
	requireWebOrigin bool
}

// The registry doubles as the machine-readable inventory of the API surface.
type routeRegistry struct {
	// Full registered pattern (method qualifier included) to its policy.
	policies map[string]routePolicy
	// Bare path pattern to every policy serving it; merged into one generated
	// OPTIONS preflight registration per path once routing is complete.
	preflight map[string][]routePolicy
	// Bare path pattern to its methods, used to render Allow on a 405.
	methods map[string][]string
}

func newRouteRegistry() *routeRegistry {
	return &routeRegistry{
		policies:  make(map[string]routePolicy),
		preflight: make(map[string][]routePolicy),
		methods:   make(map[string][]string),
	}
}

var audienceSeverity = []routeAudience{
	audienceAdminConsole, audienceDesktopClient, audienceWebsiteSession, audienceReleasePipeline, audiencePublicMetadata,
}

// Mixed-audience paths do not exist today; a future one fails toward whichever
// audience would have rejected the request first.
func strictestAudience(policies []routePolicy) routeAudience {
	for _, severity := range audienceSeverity {
		for _, policy := range policies {
			if policy.audience == severity {
				return policy.audience
			}
		}
	}
	return audienceUnknown
}

// route registers one endpoint together with the audience that may reach it.
func (a *api) route(mux *http.ServeMux, policy routePolicy, pattern string, handler http.HandlerFunc) {
	if _, duplicate := a.routes.policies[pattern]; duplicate {
		panic("duplicate route registration: " + pattern)
	}
	a.routes.policies[pattern] = policy
	if method, bare, ok := strings.Cut(pattern, " "); ok && method != http.MethodOptions {
		a.routes.preflight[bare] = append(a.routes.preflight[bare], policy)
		a.routes.methods[bare] = appendUniqueMethod(a.routes.methods[bare], method)
	}
	mux.HandleFunc(pattern, handler)
}

// finishRoutes emits one generated OPTIONS preflight endpoint per routed path
// after every real route exists, so a route cannot ship without coverage.
func (a *api) finishRoutes(mux *http.ServeMux) {
	for bare, policies := range a.routes.preflight {
		merged := routePolicy{audience: strictestAudience(policies), requireWebOrigin: everyRequireWebOrigin(policies)}
		a.routes.policies["OPTIONS "+bare] = merged
		mux.HandleFunc(http.MethodOptions+" "+bare, a.preflightFor(merged))
	}
}

// requireWebOrigin is itself part of the strictest merge: one private read on
// the path forces the whole preflight path to treat the cookie as present.
func everyRequireWebOrigin(policies []routePolicy) bool {
	required := false
	for _, policy := range policies {
		required = required || policy.requireWebOrigin
	}
	return required
}

func (a *api) preflightFor(policy routePolicy) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		expected := a.expectedOrigin(policy.audience)
		switch {
		case policy.audience == audienceAdminConsole && (expected == "" || origin != expected):
			writeError(response, http.StatusForbidden, "origin_not_allowed", "This origin is not allowed.")
			return
		case policy.audience == audienceDesktopClient && origin != "":
			writeError(response, http.StatusForbidden, "origin_not_allowed", "This origin is not allowed.")
			return
		case origin != "" && origin != expected:
			writeError(response, http.StatusForbidden, "origin_not_allowed", "This origin is not allowed.")
			return
		}
		a.writeCORSHeaders(response, request, policy.audience)
		response.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		response.Header().Set("Access-Control-Allow-Headers", "Content-Type, Idempotency-Key, X-Sesame-CSRF")
		response.Header().Set("Access-Control-Max-Age", "600")
		response.WriteHeader(http.StatusNoContent)
	}
}

func appendUniqueMethod(methods []string, method string) []string {
	for _, existing := range methods {
		if existing == method {
			return methods
		}
	}
	return append(methods, method)
}

var methodsInAllowOrder = []string{
	http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete,
}

// A known path reached with an unregistered method keeps the JSON envelope the
// method guards used to produce, with an Allow header derived from the registry.
func (a *api) allowedMethodsOnPath(request *http.Request, mux *http.ServeMux) (string, bool) {
	for _, probe := range methodsInAllowOrder {
		candidate := request.Clone(request.Context())
		candidate.Method = probe
		if _, pattern := mux.Handler(candidate); pattern != "" {
			_, bare, ok := strings.Cut(pattern, " ")
			if !ok || len(a.routes.methods[bare]) == 0 {
				continue
			}
			var allow []string
			for _, method := range methodsInAllowOrder {
				if containsString(a.routes.methods[bare], method) {
					allow = append(allow, method)
				}
			}
			return strings.Join(append(allow, http.MethodOptions), ", "), true
		}
	}
	return "", false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func (a *api) expectedOrigin(audience routeAudience) string {
	if audience == audienceAdminConsole {
		return a.config.AdminOrigin
	}
	return a.config.AllowedOrigin
}

func isUnsafeMethod(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func (a *api) writeCORSHeaders(response http.ResponseWriter, request *http.Request, audience routeAudience) {
	origin := request.Header.Get("Origin")
	expected := a.expectedOrigin(audience)
	publicSiteRead := audience == audiencePublicMetadata && origin != "" &&
		a.config.PublicSiteOrigin != "" && origin == a.config.PublicSiteOrigin && !isUnsafeMethod(request.Method)
	if origin != "" && origin == expected {
		response.Header().Set("Access-Control-Allow-Origin", origin)
		response.Header().Set("Access-Control-Allow-Credentials", "true")
		response.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
		response.Header().Set("Vary", "Origin")
	} else if publicSiteRead {
		// Deliberately without Access-Control-Allow-Credentials: public-site reads are anonymous.
		response.Header().Set("Access-Control-Allow-Origin", origin)
		response.Header().Set("Access-Control-Expose-Headers", "X-Request-ID, Retry-After")
		response.Header().Set("Vary", "Origin")
	}
}

// enforce resolves one route policy against the incoming request. It writes and
// reports false exactly when the request may proceed no further.
func (a *api) enforce(response http.ResponseWriter, request *http.Request, policy routePolicy) bool {
	audience := policy.audience
	origin := request.Header.Get("Origin")
	expected := a.expectedOrigin(audience)
	a.writeCORSHeaders(response, request, audience)

	if isUnsafeMethod(request.Method) {
		switch audience {
		case audienceWebsiteSession:
			if origin != expected {
				writeError(response, http.StatusForbidden, "origin_not_allowed", "This request origin is not allowed.")
				return false
			}
			if !a.validCSRF(request) {
				writeError(response, http.StatusForbidden, "invalid_csrf", "Refresh the Sesame website and try again.")
				return false
			}
		case audienceAdminConsole:
			if expected == "" || origin != expected {
				writeError(response, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame admin app.")
				return false
			}
			if !a.validAdminCSRF(request) {
				writeError(response, http.StatusForbidden, "invalid_csrf", "Refresh the Sesame admin app and try again.")
				return false
			}
		case audienceDesktopClient:
			if origin != "" {
				writeError(response, http.StatusForbidden, "origin_not_allowed", "Desktop linking is not available from a browser.")
				return false
			}
		}
	} else if audience == audienceAdminConsole {
		if expected == "" || origin != expected {
			writeError(response, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame admin app.")
			return false
		}
	} else if audience == audienceWebsiteSession && policy.requireWebOrigin && origin != expected {
		writeError(response, http.StatusForbidden, "origin_not_allowed", "This request must come from the Sesame website.")
		return false
	}
	if audience == audiencePublicMetadata && (request.ContentLength > 0 || len(request.TransferEncoding) > 0) {
		request.Body.Close()
		writeError(response, http.StatusUnsupportedMediaType, "request_body_not_supported", "This public metadata API does not accept request bodies.")
		return false
	}
	return true
}

// secureMux wraps the routing table: security headers, one policy lookup,
// one policy application, then dispatch. Anything unregistered falls through
// to the catch-all and answers with the JSON not-found envelope.
func (a *api) secureMux(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Request-ID", newRequestID())
		response.Header().Set("X-Content-Type-Options", "nosniff")
		response.Header().Set("Referrer-Policy", "no-referrer")
		response.Header().Set("Cross-Origin-Resource-Policy", "same-site")
		response.Header().Set("X-Frame-Options", "DENY")
		response.Header().Set("Cache-Control", "no-store")
		response.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		response.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")

		_, pattern := mux.Handler(request)
		policy, matched := a.routes.policies[pattern]
		switch {
		case matched && strings.HasPrefix(pattern, http.MethodOptions+" "):
			// The generated preflight handler applies its own audience rules.
			mux.ServeHTTP(response, request)
		case matched:
			if a.enforce(response, request, policy) {
				mux.ServeHTTP(response, request)
			}
		default:
			if allow, known := a.allowedMethodsOnPath(request, mux); known {
				response.Header().Set("Allow", allow)
				writeError(response, http.StatusMethodNotAllowed, "method_not_allowed", "This endpoint does not allow that method.")
				return
			}
			mux.ServeHTTP(response, request)
		}
	})
}

func newRequestID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "unavailable"
	}
	return base64.RawURLEncoding.EncodeToString(bytes)
}
