package updates

import (
	"context"
	"errors"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustURL(t testing.TB, raw string) *url.URL {
	t.Helper()
	parsed, err := ParseFeedURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestParseFeedURL(t *testing.T) {
	good := []string{
		DefaultFeedURL,
		"https://updates.example.net/feed.json",
		"https://updates.example.net:8443/a/b/feed.json",
		"https://127.0.0.1:9000/feed.json",
		"https://[::1]:9000/feed.json",
	}
	for _, raw := range good {
		if _, err := ParseFeedURL(raw); err != nil {
			t.Errorf("%s: %v", raw, err)
		}
	}
	bad := map[string]string{
		"http scheme":     "http://updates.example.net/feed.json",
		"ftp scheme":      "ftp://updates.example.net/feed.json",
		"file scheme":     "file:///etc/passwd",
		"no scheme":       "updates.example.net/feed.json",
		"user info":       "https://user:pass@updates.example.net/feed.json",
		"user only":       "https://user@updates.example.net/feed.json",
		"query":           "https://updates.example.net/feed.json?x=1",
		"empty query":     "https://updates.example.net/feed.json?",
		"fragment":        "https://updates.example.net/feed.json#x",
		"no host":         "https:///feed.json",
		"empty":           "",
		"spaces":          "https://updates.example.net/a b",
		"control":         "https://updates.example.net/a\nb",
		"non ascii":       "https://updates.example.net/é",
		"opaque":          "https:updates.example.net",
		"too long":        "https://updates.example.net/" + strings.Repeat("a", 3000),
		"upper http":      "HTTP://updates.example.net/feed.json",
		"javascript":      "javascript:alert(1)",
		"scheme relative": "//updates.example.net/feed.json",
	}
	for name, raw := range bad {
		if _, err := ParseFeedURL(raw); err == nil {
			t.Errorf("%s: %q was accepted", name, raw)
		}
	}
}

func TestFetchSendsOneAnonymousGet(t *testing.T) {
	var mu sync.Mutex
	var seen []*http.Request
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Clone(context.Background()))
		mu.Unlock()
		http.SetCookie(w, &http.Cookie{Name: "tracker", Value: "1"})
		_, _ = w.Write([]byte("body"))
	}))
	defer server.Close()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	target := mustURL(t, server.URL+"/v1/updates/selfhost.json")
	jar.SetCookies(target, []*http.Cookie{{Name: "session", Value: "secret"}})
	client := server.Client()
	client.Jar = jar
	fetcher := Fetcher{URL: target, Client: client}
	for round := 0; round < 2; round++ {
		body, err := fetcher.Fetch(t.Context())
		if err != nil || string(body) != "body" {
			t.Fatalf("round %d: body %q err %v", round, body, err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) != 2 {
		t.Fatalf("requests = %d", len(seen))
	}
	allowed := map[string]bool{"Accept": true, "Accept-Encoding": true, "User-Agent": true}
	for _, request := range seen {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/updates/selfhost.json" || request.URL.RawQuery != "" || request.URL.Fragment != "" {
			t.Fatalf("request line = %s %s", request.Method, request.URL)
		}
		for name := range request.Header {
			if !allowed[name] {
				t.Errorf("unexpected request header %s", name)
			}
		}
		if request.Header.Get("User-Agent") != userAgent {
			t.Errorf("User-Agent = %q", request.Header.Get("User-Agent"))
		}
		if request.ContentLength > 0 || len(request.Cookies()) != 0 || request.Referer() != "" {
			t.Errorf("the request carried a body, a cookie or a referer: %+v", request)
		}
	}
}

func TestFetchRefusesOversizeBodies(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"declared length": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", "999999")
			_, _ = w.Write([]byte("x"))
		},
		"streamed": func(w http.ResponseWriter, _ *http.Request) {
			chunk := []byte(strings.Repeat("x", 32<<10))
			for index := 0; index < 20; index++ {
				if _, err := w.Write(chunk); err != nil {
					return
				}
				w.(http.Flusher).Flush()
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewTLSServer(handler)
			defer server.Close()
			_, err := Fetcher{URL: mustURL(t, server.URL), Client: server.Client()}.Fetch(t.Context())
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want an invalid feed", err)
			}
		})
	}
	exact := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", MaxFeedBytes)))
	}))
	defer exact.Close()
	body, err := Fetcher{URL: mustURL(t, exact.URL), Client: exact.Client()}.Fetch(t.Context())
	if err != nil || len(body) != MaxFeedBytes {
		t.Fatalf("a body of exactly the limit: len %d err %v", len(body), err)
	}
}

func TestFetchTreatsFailuresAsUnreachable(t *testing.T) {
	for _, status := range []int{204, 301, 403, 404, 500, 503} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
		}))
		_, err := Fetcher{URL: mustURL(t, server.URL), Client: server.Client()}.Fetch(t.Context())
		server.Close()
		if !errors.Is(err, errUnreachable) {
			t.Errorf("status %d err = %v", status, err)
		}
	}
	closed := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	target, client := mustURL(t, closed.URL), closed.Client()
	closed.Close()
	if _, err := (Fetcher{URL: target, Client: client}).Fetch(t.Context()); !errors.Is(err, errUnreachable) {
		t.Fatalf("closed server err = %v", err)
	}
}

func TestFetchHonoursTimeoutAndCancellation(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()
	defer close(release)
	slow := Fetcher{URL: mustURL(t, server.URL), Client: server.Client(), Timeout: 100 * time.Millisecond}
	started := time.Now()
	if _, err := slow.Fetch(t.Context()); !errors.Is(err, errUnreachable) || time.Since(started) > 5*time.Second {
		t.Fatalf("timeout err = %v after %v", err, time.Since(started))
	}
	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := (Fetcher{URL: mustURL(t, server.URL), Client: server.Client()}).Fetch(cancelled); err == nil {
		t.Fatal("a cancelled context still fetched")
	}
}

func TestFetchRefusesRedirectsToAnotherOrigin(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		elsewhere.Add(1)
		_, _ = w.Write([]byte("elsewhere"))
	}))
	defer other.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-other":
			http.Redirect(w, r, other.URL+"/feed.json", http.StatusFound)
		case "/to-plain-http":
			http.Redirect(w, r, "http://"+r.Host+"/feed.json", http.StatusFound)
		case "/to-user-info":
			http.Redirect(w, r, "https://user:pass@"+r.Host+"/feed.json", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/same":
			http.Redirect(w, r, "/final", http.StatusFound)
		default:
			_, _ = w.Write([]byte("final"))
		}
	}))
	defer origin.Close()
	fetch := func(path string) ([]byte, error) {
		return Fetcher{URL: mustURL(t, origin.URL+path), Client: origin.Client()}.Fetch(t.Context())
	}
	for _, path := range []string{"/to-other", "/to-plain-http", "/to-user-info", "/loop"} {
		if _, err := fetch(path); !errors.Is(err, errUnreachable) {
			t.Errorf("%s err = %v", path, err)
		}
	}
	if elsewhere.Load() != 0 {
		t.Fatalf("the other origin received %d requests", elsewhere.Load())
	}
	if body, err := fetch("/same"); err != nil || string(body) != "final" {
		t.Fatalf("a same origin redirect body %q err %v", body, err)
	}
}

func TestFetchIgnoresRedirectPolicyOfInjectedClient(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { elsewhere.Add(1) }))
	defer other.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusFound)
	}))
	defer origin.Close()
	client := origin.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return nil }
	if _, err := (Fetcher{URL: mustURL(t, origin.URL), Client: client}).Fetch(t.Context()); err == nil || elsewhere.Load() != 0 {
		t.Fatalf("err = %v, other origin hits = %d", err, elsewhere.Load())
	}
}
