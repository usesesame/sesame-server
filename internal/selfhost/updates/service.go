package updates

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"runtime"
	"sync"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const (
	CodeNotConfigured = "not_configured"

	DefaultInterval      = 24 * time.Hour
	DefaultRetryInterval = time.Hour
	startupJitter        = 10 * time.Minute
)

var ErrOff = errors.New("updates: update checks are not turned on")

type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type systemClock struct{}

func (systemClock) Now() time.Time                         { return time.Now() }
func (systemClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

type Options struct {
	Store       selfhost.UpdateStore
	Registry    *Registry
	Product     string
	Version     string
	Keys        Keys
	FeedURL     string
	Client      *http.Client
	Clock       Clock
	InstallKind InstallKind
	Platform    Platform
	Interval    time.Duration
	Retry       time.Duration
	Jitter      func(max time.Duration) time.Duration
	Logger      *slog.Logger
}

type Current struct {
	Product string `json:"product"`
	Version string `json:"version"`
}

type Latest struct {
	Version     string    `json:"version"`
	PublishedAt time.Time `json:"publishedAt"`
	NotesURL    string    `json:"notesUrl"`
	Security    bool      `json:"security"`
	MinimumFrom string    `json:"minimumFrom"`
	Images      []Image   `json:"images"`
	Binaries    []Binary  `json:"binaries"`
}

type Status struct {
	Configured  bool       `json:"configured"`
	Enabled     *bool      `json:"enabled"`
	Channel     string     `json:"channel"`
	InstallKind string     `json:"installKind"`
	Current     Current    `json:"current"`
	Latest      *Latest    `json:"latest"`
	Available   bool       `json:"available"`
	CheckedAt   *time.Time `json:"checkedAt"`
	Error       string     `json:"error"`
	Commands    []Command  `json:"commands"`
}

type Service struct {
	store      selfhost.UpdateStore
	registry   *Registry
	product    string
	version    string
	verifier   Verifier
	fetcher    Fetcher
	clock      Clock
	kind       InstallKind
	platform   Platform
	interval   time.Duration
	retry      time.Duration
	jitter     func(max time.Duration) time.Duration
	logger     *slog.Logger
	configured bool
	trigger    chan struct{}
	mu         sync.Mutex
}

func New(options Options) (*Service, error) {
	if options.Store == nil {
		return nil, errors.New("updates: a store is required")
	}
	registry := options.Registry
	if registry == nil {
		registry = DefaultRegistry()
	}
	product := options.Product
	if product == "" {
		product = ServerProductID
	}
	if _, registered := registry.Lookup(product); !registered {
		return nil, fmt.Errorf("updates: product %q is not registered", product)
	}
	feedURL := options.FeedURL
	if feedURL == "" {
		feedURL = DefaultFeedURL
	}
	parsed, err := ParseFeedURL(feedURL)
	if err != nil {
		return nil, err
	}
	service := &Service{
		store:      options.Store,
		registry:   registry,
		product:    product,
		version:    options.Version,
		verifier:   Verifier{Keys: options.Keys, Registry: registry},
		fetcher:    Fetcher{URL: parsed, Client: options.Client},
		clock:      options.Clock,
		kind:       options.InstallKind,
		platform:   options.Platform,
		interval:   options.Interval,
		retry:      options.Retry,
		jitter:     options.Jitter,
		logger:     options.Logger,
		configured: len(options.Keys) > 0,
		trigger:    make(chan struct{}, 1),
	}
	if service.clock == nil {
		service.clock = systemClock{}
	}
	if service.kind == "" {
		service.kind = InstallBinary
	}
	if service.platform.OS == "" {
		service.platform.OS, service.platform.Arch = runtime.GOOS, runtime.GOARCH
	}
	if service.platform.Executable == "" {
		if path, err := os.Executable(); err == nil {
			service.platform.Executable = path
		}
	}
	if service.interval <= 0 {
		service.interval = DefaultInterval
	}
	if service.retry <= 0 {
		service.retry = DefaultRetryInterval
	}
	if service.jitter == nil {
		service.jitter = randomJitter
	}
	if service.logger == nil {
		service.logger = slog.New(slog.DiscardHandler)
	}
	return service, nil
}

func (s *Service) Configured() bool { return s.configured }

func (s *Service) Product() string { return s.product }

func (s *Service) HasChannel(channel string) bool {
	spec, _ := s.registry.Lookup(s.product)
	return spec.HasChannel(channel)
}

func (s *Service) Status(ctx context.Context) (Status, error) {
	settings, err := s.store.UpdateSettings(ctx)
	if err != nil {
		return Status{}, err
	}
	status := Status{
		Configured:  s.configured,
		Channel:     settings.Channel,
		InstallKind: string(s.kind),
		Current:     Current{Product: s.product, Version: s.version},
		Commands:    []Command{},
	}
	switch settings.Choice {
	case selfhost.UpdatesOn:
		enabled := true
		status.Enabled = &enabled
	case selfhost.UpdatesOff:
		enabled := false
		status.Enabled = &enabled
	}
	if !s.configured {
		status.Error = CodeNotConfigured
		return status, nil
	}
	if settings.Choice != selfhost.UpdatesOn {
		return status, nil
	}
	status.Error = settings.LastError
	status.CheckedAt = settings.CheckedAt
	if len(settings.Feed) == 0 {
		return status, nil
	}
	document, err := s.verifier.Open(settings.Feed)
	if err != nil {
		s.logger.Warn("Sesame ignored a stored update feed that no longer verifies", "error", err)
		return status, nil
	}
	release, found := document.Payload.Release(s.product, settings.Channel)
	if !found || !Offered(release, settings.Channel) {
		return status, nil
	}
	fresh := true
	if err := CheckFresh(document.Payload, s.clock.Now()); err != nil {
		fresh = false
		status.Error = codeOf(err)
	}
	status.Latest = &Latest{
		Version:     release.Version,
		PublishedAt: release.PublishedAt.UTC().Truncate(time.Second),
		NotesURL:    release.NotesURL,
		Security:    release.Security,
		MinimumFrom: release.MinimumFrom,
		Images:      append([]Image{}, release.Images...),
		Binaries:    append([]Binary{}, release.Binaries...),
	}
	status.Available = fresh && Newer(release.Version, s.version)
	if status.Available {
		status.Commands = Commands(s.kind, release, s.platform)
	}
	return status, nil
}

func (s *Service) Available(ctx context.Context) bool {
	status, err := s.Status(ctx)
	return err == nil && status.Available
}

func (s *Service) Configure(ctx context.Context, by selfhost.Actor, change selfhost.UpdateChange) (Status, error) {
	if change.Channel != nil && !s.HasChannel(*change.Channel) {
		return Status{}, selfhost.ErrInvalidInput
	}
	before, err := s.store.UpdateSettings(ctx)
	if err != nil {
		return Status{}, err
	}
	after, err := s.store.ConfigureUpdates(ctx, by, change)
	if err != nil {
		return Status{}, err
	}
	if s.configured && after.Choice == selfhost.UpdatesOn && before.Choice != selfhost.UpdatesOn {
		if _, err := s.runCheck(ctx); err != nil && !errors.Is(err, ErrOff) {
			return Status{}, err
		}
	}
	return s.Status(ctx)
}

func (s *Service) Check(ctx context.Context) (Status, error) {
	if s.configured {
		if _, err := s.runCheck(ctx); err != nil {
			return Status{}, err
		}
	}
	return s.Status(ctx)
}

func (s *Service) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

func (s *Service) runCheck(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.store.UpdateSettings(ctx)
	if err != nil {
		return "", err
	}
	if settings.Choice != selfhost.UpdatesOn {
		return "", ErrOff
	}
	now := s.clock.Now()
	body, err := s.fetcher.Fetch(ctx)
	if err != nil {
		return s.fail(ctx, codeOf(err), now, err)
	}
	document, err := s.verifier.Verify(body, now)
	if err != nil {
		return s.fail(ctx, codeOf(err), now, err)
	}
	if document.Payload.Sequence < settings.Sequences[document.Envelope.KeyID] {
		return s.fail(ctx, selfhost.UpdateErrorRollback, now, ErrRollback)
	}
	err = s.store.RecordUpdateFeed(ctx, selfhost.UpdateFeed{Raw: document.Raw, KeyID: document.Envelope.KeyID, Sequence: document.Payload.Sequence, CheckedAt: now})
	if errors.Is(err, selfhost.ErrConflict) {
		return s.fail(ctx, selfhost.UpdateErrorRollback, now, ErrRollback)
	}
	if err != nil {
		return "", err
	}
	s.logger.Info("Sesame checked for updates", "sequence", document.Payload.Sequence)
	return "", nil
}

func (s *Service) fail(ctx context.Context, code string, at time.Time, cause error) (string, error) {
	if ctx.Err() != nil {
		return code, ctx.Err()
	}
	s.logger.Warn("Sesame could not use the update feed", "code", code, "error", cause)
	if err := s.store.RecordUpdateFailure(ctx, code, at); err != nil {
		return code, err
	}
	return code, nil
}

func codeOf(err error) string {
	switch {
	case errors.Is(err, ErrExpired):
		return selfhost.UpdateErrorExpired
	case errors.Is(err, ErrRollback):
		return selfhost.UpdateErrorRollback
	case errors.Is(err, ErrInvalid):
		return selfhost.UpdateErrorInvalid
	}
	return selfhost.UpdateErrorUnreachable
}

func (s *Service) Run(ctx context.Context) {
	if !s.configured {
		return
	}
	wait := s.startupWait(ctx)
	for {
		if !s.sleep(ctx, wait) {
			return
		}
		code, err := s.runCheck(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil && !errors.Is(err, ErrOff):
			s.logger.Warn("Sesame could not record an update check", "error", err)
			wait = s.retry
		case code == selfhost.UpdateErrorUnreachable:
			wait = s.retry + s.jitter(s.retry/4)
		default:
			wait = s.interval + s.jitter(s.interval/24)
		}
	}
}

func (s *Service) startupWait(ctx context.Context) time.Duration {
	settings, err := s.store.UpdateSettings(ctx)
	if err != nil {
		return s.jitter(startupJitter)
	}
	if settings.Choice != selfhost.UpdatesOn {
		return s.interval + s.jitter(s.interval/24)
	}
	if settings.CheckedAt != nil {
		elapsed := s.clock.Now().Sub(*settings.CheckedAt)
		if elapsed >= 0 && elapsed < s.interval {
			return s.interval - elapsed + s.jitter(s.interval/24)
		}
	}
	return s.jitter(startupJitter)
}

func (s *Service) sleep(ctx context.Context, wait time.Duration) bool {
	if wait <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-s.clock.After(wait):
		return true
	case <-s.trigger:
		return true
	}
}

func randomJitter(max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	value, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0
	}
	return time.Duration(value.Int64())
}
