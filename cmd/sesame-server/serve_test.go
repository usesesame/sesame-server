package main

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

var plainClient = &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type serveHarness struct {
	cancel  context.CancelFunc
	exit    chan int
	addr    string
	stderr  *syncBuffer
	stopped atomic.Bool
}

func startServe(t *testing.T, values map[string]string, builder ApplicationBuilder, backup Runner) *serveHarness {
	t.Helper()
	addr := freeAddr(t)
	merged := map[string]string{config.EnvAddr: addr}
	for key, value := range values {
		merged[key] = value
	}
	env, _, stderr := testEnvironment(t, merged, Commands{Serve: builder, Backup: backup})
	ctx, cancel := context.WithCancel(context.Background())
	harness := &serveHarness{cancel: cancel, exit: make(chan int, 1), addr: addr, stderr: stderr}
	go func() { harness.exit <- run(ctx, nil, env) }()
	t.Cleanup(func() {
		cancel()
		if harness.stopped.Load() {
			return
		}
		select {
		case <-harness.exit:
		case <-time.After(10 * time.Second):
		}
	})
	return harness
}

func (h *serveHarness) waitReady(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case code := <-h.exit:
			t.Fatalf("serve exited early with %d: %s", code, h.stderr.String())
		default:
		}
		response, err := plainClient.Get("http://" + h.addr + "/livez")
		if err == nil {
			response.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server never answered: %s", h.stderr.String())
}

func (h *serveHarness) stop(t *testing.T) int {
	t.Helper()
	h.cancel()
	select {
	case code := <-h.exit:
		h.stopped.Store(true)
		return code
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not stop")
		return -1
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
}

func TestServeGeneratesSecretsBuildsAndShutsDownOnCancel(t *testing.T) {
	var input BuildInput
	var closed atomic.Bool
	var jobStopped atomic.Bool
	jobStarted := make(chan struct{})
	builder := ApplicationBuilderFunc(func(_ context.Context, in BuildInput) (*Application, error) {
		input = in
		return &Application{
			Handler: okHandler(),
			Jobs: []func(context.Context){func(ctx context.Context) {
				close(jobStarted)
				<-ctx.Done()
				jobStopped.Store(true)
			}},
			Close: func() error { closed.Store(true); return nil },
		}, nil
	})
	harness := startServe(t, nil, builder, nil)
	harness.waitReady(t)
	select {
	case <-jobStarted:
	case <-time.After(time.Second):
		t.Fatal("background job never started")
	}
	if code := harness.stop(t); code != exitOK {
		t.Fatalf("exit %d: %s", code, harness.stderr.String())
	}
	if !jobStopped.Load() || !closed.Load() {
		t.Fatalf("jobStopped = %v, closed = %v", jobStopped.Load(), closed.Load())
	}
	if input.Secrets == nil || len(input.Secrets.AdminEncryptionKey) != secrets.KeySize || input.Config.Addr != harness.addr || input.Logger == nil {
		t.Fatalf("build input = %+v", input)
	}
	for _, name := range secrets.Files() {
		if _, err := os.Stat(filepath.Join(secrets.Dir(input.Config.DataDir), name)); err != nil {
			t.Fatal(err)
		}
	}
	log := harness.stderr.String()
	for _, want := range []string{"Sesame configuration", "name=SESAME_ADDR", "Sesame generated an instance secret", "Sesame server listening"} {
		if !strings.Contains(log, want) {
			t.Fatalf("log lacks %q:\n%s", want, log)
		}
	}
	if strings.Contains(log, "ip-pepper") == false {
		t.Fatalf("log should name the generated files, never their contents:\n%s", log)
	}
}

func TestServeReusesSecretsOnTheNextStart(t *testing.T) {
	dataDir := t.TempDir()
	var seeds [][]byte
	var mu sync.Mutex
	builder := ApplicationBuilderFunc(func(_ context.Context, in BuildInput) (*Application, error) {
		mu.Lock()
		seeds = append(seeds, in.Secrets.SigningKey.Seed())
		mu.Unlock()
		return &Application{Handler: okHandler()}, nil
	})
	for range 2 {
		harness := startServe(t, map[string]string{config.EnvDataDir: dataDir}, builder, nil)
		harness.waitReady(t)
		if code := harness.stop(t); code != exitOK {
			t.Fatalf("exit %d", code)
		}
	}
	if len(seeds) != 2 || string(seeds[0]) != string(seeds[1]) {
		t.Fatal("the signing key changed between starts")
	}
}

func TestServeRefusesLooseSecretsBeforeBuilding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("modes are not enforced on windows")
	}
	dataDir := t.TempDir()
	if _, _, err := secrets.LoadOrCreate(dataDir); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(secrets.Dir(dataDir), secrets.IPPepperFile)
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	built := false
	builder := ApplicationBuilderFunc(func(context.Context, BuildInput) (*Application, error) { built = true; return nil, nil })
	env, _, stderr := testEnvironment(t, map[string]string{config.EnvDataDir: dataDir, config.EnvAddr: freeAddr(t)}, Commands{Serve: builder})
	if code := run(context.Background(), []string{"serve"}, env); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if built || !strings.Contains(stderr.String(), path) || !strings.Contains(stderr.String(), "SESAME_DATA_DIR") {
		t.Fatalf("built = %v, stderr = %q", built, stderr.String())
	}
}

func TestServeReportsAnOccupiedAddress(t *testing.T) {
	addr := freeAddr(t)
	blocker, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	builder := ApplicationBuilderFunc(func(context.Context, BuildInput) (*Application, error) {
		return &Application{Handler: okHandler()}, nil
	})
	env, _, stderr := testEnvironment(t, map[string]string{config.EnvAddr: addr}, Commands{Serve: builder})
	if code := run(context.Background(), nil, env); code != exitFailure {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(stderr.String(), "SESAME_ADDR") || !strings.Contains(stderr.String(), addr) {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestServeClosesResourcesWhenTheAddressIsOccupied(t *testing.T) {
	addr := freeAddr(t)
	blocker, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Close()
	var closed atomic.Bool
	builder := ApplicationBuilderFunc(func(context.Context, BuildInput) (*Application, error) {
		return &Application{Handler: okHandler(), Close: func() error { closed.Store(true); return nil }}, nil
	})
	env, _, _ := testEnvironment(t, map[string]string{config.EnvAddr: addr}, Commands{Serve: builder})
	run(context.Background(), nil, env)
	if !closed.Load() {
		t.Fatal("application resources were leaked")
	}
}

func TestServeRejectsBuilderFailureAndMissingHandler(t *testing.T) {
	failing := ApplicationBuilderFunc(func(context.Context, BuildInput) (*Application, error) {
		return nil, io.ErrUnexpectedEOF
	})
	env, _, stderr := testEnvironment(t, map[string]string{config.EnvAddr: freeAddr(t)}, Commands{Serve: failing})
	if code := run(context.Background(), nil, env); code != exitFailure || !strings.Contains(stderr.String(), "unexpected EOF") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	empty := ApplicationBuilderFunc(func(context.Context, BuildInput) (*Application, error) { return &Application{}, nil })
	env, _, stderr = testEnvironment(t, map[string]string{config.EnvAddr: freeAddr(t)}, Commands{Serve: empty})
	if code := run(context.Background(), nil, env); code != exitFailure || !strings.Contains(stderr.String(), "no HTTP handler") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}

func TestShutdownLetsAnInFlightRequestFinish(t *testing.T) {
	started := make(chan struct{})
	builder := ApplicationBuilderFunc(func(context.Context, BuildInput) (*Application, error) {
		return &Application{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/slow" {
				close(started)
				time.Sleep(300 * time.Millisecond)
				w.Write([]byte("finished"))
			}
		})}, nil
	})
	harness := startServe(t, nil, builder, nil)
	harness.waitReady(t)
	result := make(chan string, 1)
	go func() {
		response, err := plainClient.Get("http://" + harness.addr + "/slow")
		if err != nil {
			result <- "error: " + err.Error()
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		result <- string(body)
	}()
	<-started
	if code := harness.stop(t); code != exitOK {
		t.Fatalf("exit %d", code)
	}
	if got := <-result; got != "finished" {
		t.Fatalf("in-flight response = %q", got)
	}
}

func TestServeHTTPGivesUpAtTheShutdownDeadline(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	entered := make(chan struct{})
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
	})}
	ctx, cancel := context.WithCancel(context.Background())
	var jobs sync.WaitGroup
	jobCtx, stopJobs := context.WithCancel(context.Background())
	jobs.Add(1)
	go func() { defer jobs.Done(); <-jobCtx.Done() }()
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- serveHTTP(ctx, server, listener, stopJobs, &jobs, 150*time.Millisecond, slog.New(slog.NewTextHandler(logs, nil)))
	}()
	go plainClient.Get("http://" + listener.Addr().String())
	<-entered
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serveHTTP = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveHTTP ignored the shutdown deadline")
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("shutdown returned after %s, before the in-flight request had a chance", elapsed)
	}
	if !strings.Contains(logs.String(), "shutdown failed") {
		t.Fatalf("logs = %q", logs.String())
	}
}

func TestWaitForBackgroundJobsReturnsWhenJobsStop(t *testing.T) {
	var jobs sync.WaitGroup
	jobs.Add(1)
	go func() {
		defer jobs.Done()
		time.Sleep(10 * time.Millisecond)
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if !waitForBackgroundJobs(ctx, &jobs) {
		t.Fatal("waitForBackgroundJobs gave up while the jobs were still stopping")
	}
}

func TestWaitForBackgroundJobsStopsAtTheDeadline(t *testing.T) {
	var jobs sync.WaitGroup
	jobs.Add(1)
	defer jobs.Done()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if waitForBackgroundJobs(ctx, &jobs) {
		t.Fatal("waitForBackgroundJobs reported completion while a job was still running")
	}
}

func TestShutdownTimeoutMatchesTheHostedAPI(t *testing.T) {
	if shutdownTimeout != 8*time.Second {
		t.Fatalf("shutdownTimeout = %s", shutdownTimeout)
	}
}
