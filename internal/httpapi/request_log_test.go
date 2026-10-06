package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type loggedLine map[string]any

func captureRequestLogs(t *testing.T) func() []loggedLine {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return func() []loggedLine {
		t.Helper()
		raw := strings.TrimSuffix(buffer.String(), "\n")
		if raw == "" {
			return nil
		}
		physical := strings.Split(raw, "\n")
		lines := make([]loggedLine, 0, len(physical))
		for _, text := range physical {
			var line loggedLine
			if err := json.Unmarshal([]byte(text), &line); err != nil {
				t.Fatalf("log output is not one JSON object per line: %q: %v", text, err)
			}
			lines = append(lines, line)
		}
		return lines
	}
}

func requestLogTestHandler() http.Handler {
	return New(Config{
		AllowedOrigin: "https://account.example.invalid",
		AdminOrigin:   "https://admin.example.invalid",
	})
}

func TestRequestWritesOneLogLineWithTheResponseRequestID(t *testing.T) {
	logs := captureRequestLogs(t)
	response := httptest.NewRecorder()
	requestLogTestHandler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/plans", nil))

	lines := logs()
	if len(lines) != 1 {
		t.Fatalf("log lines = %d, want 1: %v", len(lines), lines)
	}
	line := lines[0]
	if line["requestId"] == "" || line["requestId"] != response.Header().Get("X-Request-ID") {
		t.Fatalf("logged request id = %v, response header = %q", line["requestId"], response.Header().Get("X-Request-ID"))
	}
	if line["method"] != "GET" || line["route"] != "GET /v1/plans" || line["audience"] != "public" || line["status"] != float64(http.StatusOK) {
		t.Fatalf("log line = %v", line)
	}
	if _, ok := line["durationMs"]; !ok {
		t.Fatalf("log line has no duration: %v", line)
	}
}

func TestRequestLogUsesTheRoutePatternAndNotTheConcretePath(t *testing.T) {
	logs := captureRequestLogs(t)
	handler := requestLogTestHandler()
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/account/support/ticket-fictional-7731", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/no-such-route/fictional-7731", nil))
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("BREW", "/v1/plans", nil))

	lines := logs()
	if len(lines) != 3 {
		t.Fatalf("log lines = %d, want 3", len(lines))
	}
	if lines[0]["route"] != "GET /v1/account/support/{ticketID}" || lines[0]["audience"] != "website" {
		t.Fatalf("matched route line = %v", lines[0])
	}
	if lines[1]["route"] != "unmatched" || lines[1]["audience"] != "unknown" || lines[1]["status"] != float64(http.StatusNotFound) {
		t.Fatalf("unmatched route line = %v", lines[1])
	}
	if lines[2]["method"] != "OTHER" {
		t.Fatalf("unknown method line = %v", lines[2])
	}
	for _, line := range lines {
		encoded, _ := json.Marshal(line)
		if strings.Contains(string(encoded), "7731") {
			t.Fatalf("a concrete path segment reached the log: %s", encoded)
		}
	}
}

func TestRequestLogCarriesNoRequestData(t *testing.T) {
	logs := captureRequestLogs(t)
	const email = "person.fictional@example.invalid"
	const secret = "fictional-secret-value-4417"
	request := httptest.NewRequest(http.MethodPost, "/v1/auth/login?email="+email+"&token="+secret, strings.NewReader(`{"email":"`+email+`","password":"`+secret+`"}`))
	request.Header.Set("Origin", "https://account.example.invalid")
	request.Header.Set("Authorization", "Bearer "+secret)
	request.Header.Set("X-Forwarded-For", "203.0.113.77")
	request.AddCookie(&http.Cookie{Name: "sesame_session", Value: secret})
	request.RemoteAddr = "203.0.113.77:51000"
	requestLogTestHandler().ServeHTTP(httptest.NewRecorder(), request)
	requestLogTestHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/"+email, nil))

	lines := logs()
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want 2", len(lines))
	}
	for _, line := range lines {
		encoded, _ := json.Marshal(line)
		for _, forbidden := range []string{email, "person.fictional", secret, "203.0.113.77", "sesame_session", "Bearer"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("log line holds %q: %s", forbidden, encoded)
			}
		}
	}
}

func TestIncomingRequestIDIsKeptOnlyWhenItIsWellFormed(t *testing.T) {
	valid := "edge-trace_0123456789ABCDEF"
	cases := []struct {
		name     string
		incoming string
		keep     bool
	}{
		{"well formed", valid, true},
		{"newline and forged line", "abcdefghijklmnop\n{\"level\":\"ERROR\",\"msg\":\"forged\"}", false},
		{"carriage return", "abcdefghijklmnop\rforged", false},
		{"space", "abcdefgh ijklmnop", false},
		{"quote", `abcdefghijklmnop"`, false},
		{"equals sign", "abcdefghijklmnop=1", false},
		{"non ascii", "abcdefghijklmnopéé", false},
		{"too short", "short", false},
		{"too long", strings.Repeat("a", 65), false},
		{"empty", "", false},
		{"null byte", "abcdefghijklmnop\x00", false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			logs := captureRequestLogs(t)
			request := httptest.NewRequest(http.MethodGet, "/v1/plans", nil)
			request.Header["X-Request-Id"] = []string{testCase.incoming}
			response := httptest.NewRecorder()
			requestLogTestHandler().ServeHTTP(response, request)

			echoed := response.Header().Get("X-Request-ID")
			if testCase.keep != (echoed == testCase.incoming) {
				t.Fatalf("response id = %q for incoming %q, keep = %v", echoed, testCase.incoming, testCase.keep)
			}
			if !validRequestID(echoed) {
				t.Fatalf("response id %q is not well formed", echoed)
			}
			lines := logs()
			if len(lines) != 1 || lines[0]["requestId"] != echoed {
				t.Fatalf("log lines = %v, want one line carrying %q", lines, echoed)
			}
			if strings.Contains(strings.ToLower(lines[0]["msg"].(string)), "forged") || lines[0]["level"] == "ERROR" {
				t.Fatalf("an incoming id injected a log line: %v", lines[0])
			}
		})
	}
}

func TestEveryRequestGetsADistinctGeneratedID(t *testing.T) {
	captureRequestLogs(t)
	handler := requestLogTestHandler()
	seen := make(map[string]bool)
	for range 20 {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/plans", nil))
		id := response.Header().Get("X-Request-ID")
		if !validRequestID(id) || seen[id] {
			t.Fatalf("generated id %q is malformed or repeated", id)
		}
		seen[id] = true
	}
}

func TestHandlerErrorLogsCarryTheRequestIDAndAServerErrorIsLoggedAsAnError(t *testing.T) {
	logs := captureRequestLogs(t)
	service := &api{routes: newRouteRegistry()}
	mux := http.NewServeMux()
	service.route(mux, routePolicy{audience: audiencePublicMetadata}, "GET /v1/boom", func(response http.ResponseWriter, request *http.Request) {
		requestLog(request.Context()).Error("Sesame test handler failed", "error", errors.New("database unavailable"))
		writeError(response, http.StatusServiceUnavailable, "boom", "Unavailable.")
	})
	service.finishRoutes(mux)
	response := httptest.NewRecorder()
	service.secureMux(mux).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/boom", nil))

	id := response.Header().Get("X-Request-ID")
	lines := logs()
	if len(lines) != 2 {
		t.Fatalf("log lines = %d, want the handler error and the request line", len(lines))
	}
	for _, line := range lines {
		if line["requestId"] != id {
			t.Fatalf("line %v does not carry request id %q", line, id)
		}
	}
	if lines[0]["msg"] != "Sesame test handler failed" {
		t.Fatalf("first line = %v", lines[0])
	}
	if lines[1]["level"] != "ERROR" || lines[1]["status"] != float64(http.StatusServiceUnavailable) {
		t.Fatalf("request line = %v, want an ERROR line with status 503", lines[1])
	}
}

func TestPanickingHandlerIsLoggedAsAServerError(t *testing.T) {
	logs := captureRequestLogs(t)
	service := &api{routes: newRouteRegistry()}
	mux := http.NewServeMux()
	service.route(mux, routePolicy{audience: audiencePublicMetadata}, "GET /v1/panic", func(http.ResponseWriter, *http.Request) {
		panic("fictional failure")
	})
	service.finishRoutes(mux)
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("the middleware swallowed the panic")
			}
		}()
		service.secureMux(mux).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/panic", nil))
	}()
	lines := logs()
	if len(lines) != 1 || lines[0]["status"] != float64(http.StatusInternalServerError) || lines[0]["level"] != "ERROR" {
		t.Fatalf("log lines = %v, want one ERROR line with status 500", lines)
	}
}

func TestProbeRoutesLogBelowInfo(t *testing.T) {
	logs := captureRequestLogs(t)
	requestLogTestHandler().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/readyz", nil))
	lines := logs()
	if len(lines) != 1 || lines[0]["level"] != "DEBUG" {
		t.Fatalf("probe log lines = %v, want one DEBUG line", lines)
	}
}

func TestResponseEncodingFailureLogCarriesTheRequestID(t *testing.T) {
	logs := captureRequestLogs(t)
	response := httptest.NewRecorder()
	response.Header().Set("X-Request-ID", "trace0123456789abcdef")
	writeJSON(response, http.StatusOK, map[string]any{"bad": make(chan int)})
	lines := logs()
	if len(lines) != 1 || lines[0]["requestId"] != "trace0123456789abcdef" {
		t.Fatalf("log lines = %v, want one line with the request id", lines)
	}
}

func TestRequestLogWithoutAContextIDStillLogs(t *testing.T) {
	logs := captureRequestLogs(t)
	requestLog(context.Background()).Warn("Sesame test background warning")
	lines := logs()
	if len(lines) != 1 {
		t.Fatalf("log lines = %v", lines)
	}
	if _, present := lines[0]["requestId"]; present {
		t.Fatalf("a background line invented a request id: %v", lines[0])
	}
}
