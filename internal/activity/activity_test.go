package activity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

var fixedNow = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)

type fakeGitHub struct {
	commitStatus int
	link         string
	commitBody   string
	repoBody     string
	requests     []string
}

func (f *fakeGitHub) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	f.requests = append(f.requests, request.URL.RequestURI())
	if strings.HasSuffix(request.URL.Path, "/commits") {
		if f.commitStatus != 0 && f.commitStatus != http.StatusOK {
			response.WriteHeader(f.commitStatus)
			return
		}
		if f.link != "" {
			response.Header().Set("Link", f.link)
		}
		_, _ = response.Write([]byte(f.commitBody))
		return
	}
	_, _ = response.Write([]byte(f.repoBody))
}

func newTestTracker(t *testing.T, fake *fakeGitHub) *Tracker {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	tracker := NewTracker(server.URL, "fictional-org", []string{"fictional-app"}, nil)
	tracker.now = func() time.Time { return fixedNow }
	return tracker
}

func lastPageLink(page string) string {
	return fmt.Sprintf(`<https://api.example.invalid/commits?per_page=1&page=2>; rel="next", <https://api.example.invalid/commits?per_page=1&page=%s>; rel="last"`, page)
}

func TestRefreshCountsCommitsFromTheLastPageLink(t *testing.T) {
	fake := &fakeGitHub{link: lastPageLink("467"), commitBody: `[{"sha":"a"}]`, repoBody: `{"pushed_at":"2026-10-04T10:00:08Z"}`}
	tracker := newTestTracker(t, fake)
	if _, ok := tracker.Snapshot(); ok {
		t.Fatal("a tracker that never refreshed reported a snapshot")
	}
	if err := tracker.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	snapshot, ok := tracker.Snapshot()
	if !ok {
		t.Fatal("no snapshot after a successful refresh")
	}
	if !snapshot.GeneratedAt.Equal(fixedNow) || snapshot.WindowDays != 30 || len(snapshot.Repositories) != 1 {
		t.Fatalf("snapshot = %+v", snapshot)
	}
	repository := snapshot.Repositories[0]
	if repository.Name != "fictional-app" || repository.RecentCommits != 467 || !repository.PushedAt.Equal(time.Date(2026, 10, 4, 10, 0, 8, 0, time.UTC)) {
		t.Fatalf("repository = %+v", repository)
	}
	wantSince := "since=2026-09-04T12%3A00%3A00Z"
	if !strings.Contains(strings.Join(fake.requests, " "), wantSince) {
		t.Fatalf("requests %v do not ask for the %d-day window", fake.requests, WindowDays)
	}
}

func TestRefreshCountsAShortListWithoutALink(t *testing.T) {
	for body, want := range map[string]int{`[]`: 0, `[{"sha":"a"}]`: 1} {
		tracker := newTestTracker(t, &fakeGitHub{commitBody: body, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`})
		if err := tracker.Refresh(context.Background()); err != nil {
			t.Fatalf("refresh %s: %v", body, err)
		}
		snapshot, _ := tracker.Snapshot()
		if snapshot.Repositories[0].RecentCommits != want {
			t.Fatalf("%s counted %d, want %d", body, snapshot.Repositories[0].RecentCommits, want)
		}
	}
}

func TestRefreshRefusesMalformedResponses(t *testing.T) {
	cases := map[string]*fakeGitHub{
		"rate limited":           {commitStatus: http.StatusForbidden, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`},
		"non-numeric last page":  {link: lastPageLink("many"), commitBody: `[{}]`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`},
		"zero last page":         {link: lastPageLink("0"), commitBody: `[{}]`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`},
		"oversized last page":    {link: lastPageLink("1000001"), commitBody: `[{}]`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`},
		"page size ignored":      {commitBody: `[{},{}]`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`},
		"next without last":      {link: `<https://api.example.invalid/commits?per_page=1&page=2>; rel="next"`, commitBody: `[{}]`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`},
		"commit list not a list": {commitBody: `{"message":"Not Found"}`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`},
		"no push time":           {commitBody: `[]`, repoBody: `{}`},
		"unreadable repository":  {commitBody: `[]`, repoBody: `not json`},
	}
	for name, fake := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTestTracker(t, fake)
			if err := tracker.Refresh(context.Background()); err == nil {
				t.Fatal("refresh accepted a malformed response")
			}
			if _, ok := tracker.Snapshot(); ok {
				t.Fatal("a failed refresh published a snapshot")
			}
		})
	}
}

func TestAFailedRefreshKeepsTheLastGoodSnapshot(t *testing.T) {
	fake := &fakeGitHub{link: lastPageLink("12"), commitBody: `[{}]`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`}
	tracker := newTestTracker(t, fake)
	if err := tracker.Refresh(context.Background()); err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	fake.commitStatus = http.StatusServiceUnavailable
	if err := tracker.Refresh(context.Background()); err == nil {
		t.Fatal("refresh ignored an upstream failure")
	}
	snapshot, ok := tracker.Snapshot()
	if !ok || snapshot.Repositories[0].RecentCommits != 12 {
		t.Fatalf("snapshot after a failed refresh = %+v, %v", snapshot, ok)
	}
}

func TestSnapshotReturnsACopy(t *testing.T) {
	tracker := newTestTracker(t, &fakeGitHub{link: lastPageLink("5"), commitBody: `[{}]`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`})
	if err := tracker.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	first, _ := tracker.Snapshot()
	first.Repositories[0].RecentCommits = 999
	second, _ := tracker.Snapshot()
	if second.Repositories[0].RecentCommits != 5 {
		t.Fatal("a caller changed the shared snapshot")
	}
}

func TestRedirectsStayOnTheSameHost(t *testing.T) {
	var reached atomic.Bool
	elsewhere := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		reached.Store(true)
	}))
	t.Cleanup(elsewhere.Close)
	origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, elsewhere.URL+request.URL.Path, http.StatusFound)
	}))
	t.Cleanup(origin.Close)
	tracker := NewTracker(origin.URL, "fictional-org", []string{"fictional-app"}, nil)
	if err := tracker.Refresh(context.Background()); err == nil {
		t.Fatal("refresh accepted a cross-host redirect")
	}
	if reached.Load() {
		t.Fatal("the tracker followed a redirect to another host")
	}
}

func TestRunStopsWhenTheContextEnds(t *testing.T) {
	tracker := newTestTracker(t, &fakeGitHub{commitBody: `[]`, repoBody: `{"pushed_at":"2026-10-01T00:00:00Z"}`})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tracker.Run(ctx, time.Hour)
		close(done)
	}()
	deadline := time.After(5 * time.Second)
	for {
		if _, ok := tracker.Snapshot(); ok {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Run did not refresh on start")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run kept going after its context ended")
	}
}
