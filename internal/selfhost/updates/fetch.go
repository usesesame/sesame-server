package updates

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	DefaultFeedURL = "https://api.usesesame.app/v1/updates/selfhost.json"
	FetchTimeout   = 10 * time.Second
	maxRedirects   = 3
	userAgent      = "sesame-server-update-check"
)

var (
	errUnreachable = errors.New("updates: the feed could not be fetched")
	errTooLarge    = fmt.Errorf("%w: the feed is larger than %d bytes", ErrInvalid, MaxFeedBytes)
)

func ParseFeedURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxURLBytes {
		return nil, errors.New("the feed address must be an https URL of at most 2048 bytes")
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] <= 0x20 || raw[index] >= 0x7f {
			return nil, errors.New("the feed address must not hold spaces, control characters or non ASCII bytes")
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("the feed address is not a valid URL")
	}
	if parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.Opaque != "" {
		return nil, errors.New("the feed address must be an https URL with a host")
	}
	if parsed.User != nil {
		return nil, errors.New("the feed address must not hold a user name or password")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("the feed address must not hold a query or a fragment")
	}
	return parsed, nil
}

func sameOrigin(a, b *url.URL) bool {
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host)
}

type Fetcher struct {
	URL     *url.URL
	Client  *http.Client
	Timeout time.Duration
}

func (f Fetcher) Fetch(ctx context.Context) ([]byte, error) {
	if f.URL == nil {
		return nil, fmt.Errorf("%w: no feed address", errUnreachable)
	}
	client := http.Client{}
	if f.Client != nil {
		client = *f.Client
	}
	client.Jar = nil
	client.Timeout = FetchTimeout
	if f.Timeout > 0 && f.Timeout < FetchTimeout {
		client.Timeout = f.Timeout
	}
	origin := *f.URL
	client.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return errors.New("too many redirects")
		}
		if !sameOrigin(&origin, next.URL) || next.URL.User != nil {
			return errors.New("redirect to another origin refused")
		}
		return nil
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUnreachable, err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", userAgent)
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUnreachable, scrub(err))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%w: status %d", errUnreachable, response.StatusCode)
	}
	if response.ContentLength > MaxFeedBytes {
		return nil, errTooLarge
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxFeedBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errUnreachable, scrub(err))
	}
	if len(body) > MaxFeedBytes {
		return nil, errTooLarge
	}
	return body, nil
}

func scrub(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return urlErr.Err
	}
	return err
}
