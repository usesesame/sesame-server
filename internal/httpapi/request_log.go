package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

const (
	requestIDHeader    = "X-Request-ID"
	minRequestIDLength = 16
	maxRequestIDLength = 64
)

type requestIDContextKey struct{}

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

func requestIDFor(request *http.Request) string {
	if incoming := request.Header.Get(requestIDHeader); validRequestID(incoming) {
		return incoming
	}
	return newRequestID()
}

func requestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDContextKey{}).(string)
	return id
}

func requestLog(ctx context.Context) *slog.Logger {
	logger := slog.Default()
	if id := requestIDFromContext(ctx); id != "" {
		return logger.With("requestId", id)
	}
	return logger
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(status int) {
	if recorder.status == 0 {
		recorder.status = status
	}
	recorder.ResponseWriter.WriteHeader(status)
}

func (recorder *statusRecorder) Write(body []byte) (int, error) {
	if recorder.status == 0 {
		recorder.status = http.StatusOK
	}
	return recorder.ResponseWriter.Write(body)
}

func (recorder *statusRecorder) Unwrap() http.ResponseWriter {
	return recorder.ResponseWriter
}

func (recorder *statusRecorder) statusCode() int {
	if recorder.status == 0 {
		return http.StatusOK
	}
	return recorder.status
}

func (audience routeAudience) label() string {
	switch audience {
	case audiencePublicMetadata:
		return "public"
	case audienceWebsiteSession:
		return "website"
	case audienceAdminConsole:
		return "admin"
	case audienceDesktopClient:
		return "desktop"
	case audienceReleasePipeline:
		return "release"
	default:
		return "unknown"
	}
}

func methodLabel(method string) string {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodOptions:
		return method
	default:
		return "OTHER"
	}
}

func routeLabel(pattern string) string {
	if pattern == "" {
		return "unmatched"
	}
	return pattern
}

func isProbeRoute(pattern string) bool {
	switch strings.TrimPrefix(pattern, http.MethodGet+" ") {
	case "/livez", "/readyz", "/healthz":
		return true
	}
	return false
}

func logRequest(request *http.Request, pattern string, audience routeAudience, status int, started time.Time) {
	level := slog.LevelInfo
	switch {
	case status >= http.StatusInternalServerError:
		level = slog.LevelError
	case isProbeRoute(pattern):
		level = slog.LevelDebug
	}
	requestLog(request.Context()).Log(request.Context(), level, "Sesame API request",
		"method", methodLabel(request.Method),
		"route", routeLabel(pattern),
		"audience", audience.label(),
		"status", status,
		"durationMs", time.Since(started).Milliseconds(),
	)
}
