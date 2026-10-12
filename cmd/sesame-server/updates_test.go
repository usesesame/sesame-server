package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"

	"usesesame.app/backend/internal/selfhost/config"
	"usesesame.app/backend/internal/selfhost/secrets"
)

func TestInvalidUpdateSettingsStopTheServerAtStart(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"http feed address":     {config.EnvUpdateFeedURL: "http://updates.example.net/feed.json"},
		"feed address with key": {config.EnvUpdateFeedURL: "https://user:pass@updates.example.net/feed.json"},
		"bad public keys":       {config.EnvUpdatePublicKeys: "key-a:short"},
		"bad install kind":      {config.EnvInstallKind: "vm"},
	} {
		t.Run(name, func(t *testing.T) {
			values[config.EnvDataDir] = t.TempDir()
			code, _, stderr := command(t, values, "serve")
			if code != exitFailure || !strings.Contains(stderr, "configuration is invalid") {
				t.Fatalf("exit %d: %s", code, stderr)
			}
			for variable := range values {
				if variable != config.EnvDataDir && !strings.Contains(stderr, variable) {
					t.Fatalf("the message does not name %s: %s", variable, stderr)
				}
			}
		})
	}
}

func TestServerServesTheUpdateRoutes(t *testing.T) {
	run := startReal(t, t.TempDir())
	defer run.stop(t)
	_, port, err := net.SplitHostPort(run.addr)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range []struct{ method, path string }{
		{http.MethodGet, "/v1/owner/updates"},
		{http.MethodPatch, "/v1/owner/updates"},
		{http.MethodPost, "/v1/owner/updates/check"},
	} {
		request, err := http.NewRequest(call.method, "http://"+run.addr+call.path, nil)
		if err != nil {
			t.Fatal(err)
		}
		request.Host = "localhost:" + port
		request.Header.Set("Origin", "http://localhost:"+port)
		response, err := plainClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s %s = %d, want 401", call.method, call.path, response.StatusCode)
		}
	}
}

func TestBuiltApplicationHasTheUpdateJob(t *testing.T) {
	cfg, err := config.Load(lookupFrom(map[string]string{config.EnvDataDir: t.TempDir(), config.EnvAddr: freeAddr(t)}))
	if err != nil {
		t.Fatal(err)
	}
	loaded, _, err := secrets.LoadOrCreate(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	application, err := buildApplication(t.Context(), BuildInput{Config: cfg, Secrets: loaded, Logger: slog.New(slog.DiscardHandler), Version: "9.9.9-test"})
	if err != nil {
		t.Fatal(err)
	}
	defer application.Close()
	if len(application.Jobs) != 3 {
		t.Fatalf("jobs = %d, want the backup, maintenance and update jobs", len(application.Jobs))
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	application.Jobs[2](ctx)
}
