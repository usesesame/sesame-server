package server

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	requestIDHeader    = "X-Request-ID"
	minRequestIDLength = 16
	maxRequestIDLength = 64
	apiCSP             = "default-src 'none'; frame-ancestors 'none'"
	consoleCSP         = "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; font-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
)

type requestIDKey struct{}

func validRequestID(value string) bool {
	if len(value) < minRequestIDLength || len(value) > maxRequestIDLength {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		switch {
		case character >= 'a' && character <= 'z', character >= 'A' && character <= 'Z', character >= '0' && character <= '9', character == '-', character == '_':
		default:
			return false
		}
	}
	return true
}

func requestIDFor(r *http.Request) string {
	if incoming := r.Header.Get(requestIDHeader); validRequestID(incoming) {
		return incoming
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "unavailable-request"
	}
	return base64.RawURLEncoding.EncodeToString(raw)
}

func requestLog(ctx context.Context) *slog.Logger {
	logger := slog.Default()
	if id, _ := ctx.Value(requestIDKey{}).(string); id != "" {
		return logger.With("requestId", id)
	}
	return logger
}

type recorder struct {
	http.ResponseWriter
	status int
	route  string
}

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *recorder) Write(body []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.ResponseWriter.Write(body)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) committed() bool { return r.status != 0 }

func (r *recorder) statusCode() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	}
	return "OTHER"
}

func isProbeRoute(route string) bool {
	switch strings.TrimPrefix(route, http.MethodGet+" ") {
	case "/livez", "/readyz", "static":
		return true
	}
	return false
}

func (s *server) wrap(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		requestID := requestIDFor(r)
		r = r.WithContext(context.WithValue(r.Context(), requestIDKey{}, requestID))
		_, pattern := mux.Handler(r)
		rec := &recorder{ResponseWriter: w, route: pattern}
		header := rec.Header()
		header.Set(requestIDHeader, requestID)
		header.Set("X-Content-Type-Options", "nosniff")
		header.Set("Referrer-Policy", "no-referrer")
		header.Set("Cross-Origin-Resource-Policy", "same-origin")
		header.Set("X-Frame-Options", "DENY")
		header.Set("Cache-Control", "no-store")
		header.Set("Content-Security-Policy", apiCSP)
		if s.cfg.PublicURL.Secure {
			header.Set("Strict-Transport-Security", "max-age=31536000")
		}
		completed := false
		defer func() {
			status := rec.statusCode()
			if !completed {
				if recovered := recover(); recovered != nil {
					requestLog(r.Context()).Error("Sesame server request panicked", "panic", recovered)
					if !rec.committed() {
						writeError(rec, http.StatusInternalServerError, "internal_error", "The server could not complete that request.")
					}
					status = rec.statusCode()
				} else if !rec.committed() {
					status = http.StatusInternalServerError
				}
			}
			s.finish(r, rec.route, status, started)
		}()
		mux.ServeHTTP(rec, r)
		completed = true
	})
}

func (s *server) finish(r *http.Request, route string, status int, started time.Time) {
	if route == "" || route == "/" {
		route = "unmatched"
	}
	level := slog.LevelInfo
	switch {
	case status >= http.StatusInternalServerError:
		level = slog.LevelError
	case isProbeRoute(route):
		level = slog.LevelDebug
	}
	elapsed := time.Since(started)
	requestLog(r.Context()).Log(r.Context(), level, "Sesame server request",
		"method", methodLabel(r.Method),
		"route", route,
		"status", status,
		"durationMs", elapsed.Milliseconds(),
	)
	s.metrics.observe(methodLabel(r.Method), route, status, elapsed)
}
