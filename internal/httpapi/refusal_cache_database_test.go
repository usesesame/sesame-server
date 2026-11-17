package httpapi

import (
	"container/list"
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	"usesesame.app/backend/internal/testdb"
)

func TestARefusedClientCausesNoFurtherRateLimitWrites(t *testing.T) {
	ctx := context.Background()
	store := testdb.Open(t, accounts.Open)
	lockDatabaseTests(t, store.DB())
	key := fmt.Sprintf("refusal-cache-test:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		_, _ = store.DB().ExecContext(context.Background(), `DELETE FROM sesame_rate_limits WHERE key = $1`, key)
	})
	service := &api{
		config:  Config{Accounts: store},
		limits:  &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()},
		refused: newRefusals(),
	}
	attempts := func() int {
		var count int
		if err := store.DB().QueryRowContext(ctx, `SELECT attempts FROM sesame_rate_limits WHERE key = $1`, key).Scan(&count); err != nil {
			t.Fatalf("read attempts: %v", err)
		}
		return count
	}
	for attempt := 1; attempt <= 4; attempt++ {
		response := limitAttempt(service, key, 3, time.Minute)
		if attempt <= 3 && response.Code != http.StatusOK {
			t.Fatalf("attempt %d = %d, want allowed", attempt, response.Code)
		}
		if attempt == 4 && response.Code != http.StatusTooManyRequests {
			t.Fatalf("attempt %d = %d, want refused", attempt, response.Code)
		}
	}
	if got := attempts(); got != 4 {
		t.Fatalf("attempts recorded = %d, want 4", got)
	}
	for range 25 {
		if response := limitAttempt(service, key, 3, time.Minute); response.Code != http.StatusTooManyRequests || response.Header().Get("Retry-After") == "" {
			t.Fatalf("remembered refusal = %d retry %q", response.Code, response.Header().Get("Retry-After"))
		}
	}
	if got := attempts(); got != 4 {
		t.Fatalf("attempts recorded = %d after remembered refusals, want no further writes", got)
	}
	other := &api{config: Config{Accounts: store}, limits: &authLimiter{attempts: map[string]*limitEntry{}, recency: list.New()}, refused: newRefusals()}
	if response := limitAttempt(other, key, 3, time.Minute); response.Code != http.StatusTooManyRequests {
		t.Fatalf("a second process without the remembered refusal = %d, want the database to refuse", response.Code)
	}
}
