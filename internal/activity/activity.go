package activity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"sync"
	"time"
)

const (
	WindowDays       = 30
	RefreshInterval  = 15 * time.Minute
	maxResponseBytes = 1 << 20
	maxCommitCount   = 1_000_000
	requestTimeout   = 10 * time.Second
)

var lastPagePattern = regexp.MustCompile(`<([^>]+)>;\s*rel="last"`)

type Repository struct {
	Name          string    `json:"name"`
	PushedAt      time.Time `json:"pushedAt"`
	RecentCommits int       `json:"recentCommits"`
}

type Snapshot struct {
	GeneratedAt  time.Time    `json:"generatedAt"`
	WindowDays   int          `json:"windowDays"`
	Repositories []Repository `json:"repositories"`
}

type Tracker struct {
	baseURL      string
	owner        string
	repositories []string
	client       *http.Client
	now          func() time.Time

	mu       sync.RWMutex
	snapshot *Snapshot
}

func NewTracker(baseURL, owner string, repositories []string, client *http.Client) *Tracker {
	if client == nil {
		client = &http.Client{Timeout: requestTimeout, CheckRedirect: sameHostRedirect}
	}
	return &Tracker{
		baseURL:      baseURL,
		owner:        owner,
		repositories: append([]string(nil), repositories...),
		client:       client,
		now:          time.Now,
	}
}

func sameHostRedirect(request *http.Request, via []*http.Request) error {
	if len(via) >= 3 || request.URL.Host != via[0].URL.Host || request.URL.Scheme != via[0].URL.Scheme {
		return http.ErrUseLastResponse
	}
	return nil
}

func (t *Tracker) Snapshot() (Snapshot, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	if t.snapshot == nil {
		return Snapshot{}, false
	}
	copied := *t.snapshot
	copied.Repositories = append([]Repository(nil), t.snapshot.Repositories...)
	return copied, true
}

func (t *Tracker) Run(ctx context.Context, interval time.Duration) {
	t.refreshAndLog(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.refreshAndLog(ctx)
		}
	}
}

func (t *Tracker) refreshAndLog(ctx context.Context) {
	if err := t.Refresh(ctx); err != nil && ctx.Err() == nil {
		slog.Warn("Sesame API could not refresh project activity", "error", err)
	}
}

func (t *Tracker) Refresh(ctx context.Context) error {
	now := t.now().UTC()
	since := now.Add(-WindowDays * 24 * time.Hour)
	repositories := make([]Repository, 0, len(t.repositories))
	for _, name := range t.repositories {
		pushedAt, err := t.pushedAt(ctx, name)
		if err != nil {
			return err
		}
		commits, err := t.commitCount(ctx, name, since)
		if err != nil {
			return err
		}
		repositories = append(repositories, Repository{Name: name, PushedAt: pushedAt, RecentCommits: commits})
	}
	t.mu.Lock()
	t.snapshot = &Snapshot{GeneratedAt: now, WindowDays: WindowDays, Repositories: repositories}
	t.mu.Unlock()
	return nil
}

func (t *Tracker) pushedAt(ctx context.Context, name string) (time.Time, error) {
	var repository struct {
		PushedAt time.Time `json:"pushed_at"`
	}
	response, err := t.get(ctx, fmt.Sprintf("/repos/%s/%s", url.PathEscape(t.owner), url.PathEscape(name)))
	if err != nil {
		return time.Time{}, err
	}
	defer response.Body.Close()
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&repository); err != nil {
		return time.Time{}, fmt.Errorf("%s: unreadable repository response: %w", name, err)
	}
	if repository.PushedAt.IsZero() {
		return time.Time{}, fmt.Errorf("%s: repository response has no push time", name)
	}
	return repository.PushedAt.UTC(), nil
}

func (t *Tracker) commitCount(ctx context.Context, name string, since time.Time) (int, error) {
	query := url.Values{"since": {since.Format(time.RFC3339)}, "per_page": {"1"}}
	response, err := t.get(ctx, fmt.Sprintf("/repos/%s/%s/commits?%s", url.PathEscape(t.owner), url.PathEscape(name), query.Encode()))
	if err != nil {
		return 0, err
	}
	defer response.Body.Close()
	var commits []json.RawMessage
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&commits); err != nil {
		return 0, fmt.Errorf("%s: unreadable commit list: %w", name, err)
	}
	match := lastPagePattern.FindStringSubmatch(response.Header.Get("Link"))
	if match == nil {
		if len(commits) > 1 {
			return 0, fmt.Errorf("%s: commit list ignored the page size", name)
		}
		return len(commits), nil
	}
	last, err := url.Parse(match[1])
	if err != nil {
		return 0, fmt.Errorf("%s: unreadable last page link", name)
	}
	count, err := strconv.Atoi(last.Query().Get("page"))
	if err != nil || count < 1 || count > maxCommitCount {
		return 0, fmt.Errorf("%s: last page link has no usable page number", name)
	}
	return count, nil
}

func (t *Tracker) get(ctx context.Context, path string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, t.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "sesame-api-activity")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	response, err := t.client.Do(request)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return nil, errors.New(path + " returned " + response.Status)
	}
	return response, nil
}
