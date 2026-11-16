package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

type windowLimitStore struct {
	selfhost.Store
	mu       sync.Mutex
	now      func() time.Time
	calls    int
	attempts map[string][]time.Time
}

func (s *windowLimitStore) RateLimit(_ context.Context, key string, limit int, window time.Duration) (selfhost.RateDecision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	now := s.now()
	started := now
	if previous := s.attempts[key]; len(previous) > 0 && now.Sub(previous[0]) < window {
		started = previous[0]
	} else {
		s.attempts[key] = nil
	}
	s.attempts[key] = append(s.attempts[key], now)
	return selfhost.RateDecision{Allowed: len(s.attempts[key]) <= limit, RetryAfter: started.Add(window).Sub(now)}, nil
}

func (s *windowLimitStore) ResetRateLimit(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.attempts, key)
	return nil
}

func (s *windowLimitStore) callCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

type refusalHarness struct {
	server *server
	store  *windowLimitStore
	mu     sync.Mutex
	now    time.Time
}

func newRefusalHarness() *refusalHarness {
	harness := &refusalHarness{now: time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)}
	harness.store = &windowLimitStore{now: harness.clock, attempts: map[string][]time.Time{}}
	harness.server = &server{
		cfg:     Config{Store: harness.store, Now: harness.clock, IPPepper: []byte("fictional-pepper-for-tests-only!")},
		refused: newRefusals(),
	}
	return harness
}

func (h *refusalHarness) clock() time.Time {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.now
}

func (h *refusalHarness) advance(d time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.now = h.now.Add(d)
}

func (h *refusalHarness) allow(t *testing.T, key string, limit int) (bool, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/owner/login", nil)
	allowed := h.server.allowKey(recorder, request, key, limit, time.Minute)
	if !allowed && recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("refusal status = %d", recorder.Code)
	}
	return allowed, recorder.Header().Get("Retry-After")
}

func TestRefusedKeysAreAnsweredFromMemory(t *testing.T) {
	h := newRefusalHarness()
	for attempt := 1; attempt <= 200; attempt++ {
		allowed, retry := h.allow(t, "fictional-route:ip:one", 5)
		if allowed != (attempt <= 5) {
			t.Fatalf("attempt %d allowed = %v", attempt, allowed)
		}
		if !allowed && retry != "60" {
			t.Fatalf("attempt %d Retry-After = %q, want 60", attempt, retry)
		}
	}
	if calls := h.store.callCount(); calls != 6 {
		t.Fatalf("the store saw %d rate limit calls for 200 requests, want 6", calls)
	}

	h.advance(25 * time.Second)
	if allowed, retry := h.allow(t, "fictional-route:ip:one", 5); allowed || retry != "35" {
		t.Fatalf("after 25 seconds allowed = %v, Retry-After = %q, want 35", allowed, retry)
	}
	if calls := h.store.callCount(); calls != 6 {
		t.Fatalf("a remembered refusal reached the store, calls = %d", calls)
	}

	h.advance(35 * time.Second)
	if allowed, _ := h.allow(t, "fictional-route:ip:one", 5); !allowed {
		t.Fatal("the key stayed refused after its window ended")
	}
	if calls := h.store.callCount(); calls != 7 {
		t.Fatalf("an expired refusal did not consult the store, calls = %d", calls)
	}
	if allowed, _ := h.allow(t, "fictional-route:ip:two", 5); !allowed {
		t.Fatal("a refusal for one key refused another key")
	}
}

func TestForgettingAClientSubjectClearsTheRememberedRefusal(t *testing.T) {
	h := newRefusalHarness()
	request := httptest.NewRequest(http.MethodPost, "/v1/owner/login", nil)
	request.RemoteAddr = "198.51.100.20:4000"
	allow := func() bool {
		return h.server.allowClientSubject(httptest.NewRecorder(), request, "owner-login", "fictional-owner", 2, time.Minute)
	}
	for range 2 {
		if !allow() {
			t.Fatal("an attempt under the limit was refused")
		}
	}
	if allow() {
		t.Fatal("an attempt over the limit was allowed")
	}
	h.server.forgetClientSubject(request, "owner-login", "fictional-owner")
	if !allow() {
		t.Fatal("the sign-in reset left the name refused")
	}
}

func TestRememberedRefusalsAreBounded(t *testing.T) {
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	memory := newRefusals()
	for index := range maxRememberedRefusals {
		memory.remember("fictional-key-"+strconv.Itoa(index), now.Add(time.Minute), now)
	}
	memory.remember("fictional-key-overflow", now.Add(time.Minute), now)
	if len(memory.until) != maxRememberedRefusals {
		t.Fatalf("memory holds %d records, want %d", len(memory.until), maxRememberedRefusals)
	}
	if _, refused := memory.active("fictional-key-overflow", now); refused {
		t.Fatal("a record past the bound was kept")
	}
	memory.remember("fictional-key-0", now.Add(2*time.Minute), now)
	if remaining, refused := memory.active("fictional-key-0", now); !refused || remaining != 2*time.Minute {
		t.Fatalf("an existing record was not updated at the bound: %v %v", remaining, refused)
	}

	later := now.Add(time.Minute)
	memory.remember("fictional-key-late", later.Add(time.Minute), later)
	if len(memory.until) != 2 {
		t.Fatalf("memory holds %d records after the sweep, want the extended one and the newest one", len(memory.until))
	}
	if _, refused := memory.active("fictional-key-late", later); !refused {
		t.Fatal("the sweep did not make room for a new record")
	}
}

func TestRememberedRefusalsExpireAndIgnoreElapsedTimes(t *testing.T) {
	now := time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	memory := newRefusals()
	memory.remember("fictional-elapsed", now, now)
	if len(memory.until) != 0 {
		t.Fatal("a refusal that already ended was remembered")
	}
	memory.remember("fictional-key", now.Add(time.Second), now)
	if _, refused := memory.active("fictional-key", now.Add(time.Second)); refused {
		t.Fatal("a refusal was still active at its end time")
	}
	if len(memory.until) != 0 {
		t.Fatal("an expired refusal was not dropped on lookup")
	}
}

func TestRememberedRefusalsAreSafeForConcurrentUse(t *testing.T) {
	h := newRefusalHarness()
	var group sync.WaitGroup
	for worker := range 16 {
		group.Add(1)
		go func() {
			defer group.Done()
			key := "fictional-route:ip:" + strconv.Itoa(worker%4)
			for range 50 {
				recorder := httptest.NewRecorder()
				request := httptest.NewRequest(http.MethodGet, "/v1/sync/state", nil)
				h.server.allowKey(recorder, request, key, 10, time.Minute)
				h.server.refused.forget("fictional-route:ip:unused")
			}
		}()
	}
	group.Wait()
	if calls := h.store.callCount(); calls < 44 || calls > 16*50 {
		t.Fatalf("store calls = %d", calls)
	}
}
