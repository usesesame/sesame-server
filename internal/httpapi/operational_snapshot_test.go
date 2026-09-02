package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

func TestOperationalSnapshotProvider(t *testing.T) {
	maintenance := NewMaintenanceState()
	maintenance.Record(OperationalReady, time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC))
	provider := newOperationalSnapshotProvider(Config{
		Version:                   "1.2.3",
		Commit:                    "abc123",
		ReleaseCandidatePublicKey: []byte("public-key-marker"),
		ReleaseCandidateKeyID:     "release-key",
		ReleaseCandidateTokenHash: []byte("token-marker"),
		ArtifactDelivery:          testArtifactDelivery{},
		OperationalOutbox:         testOperationalOutbox{summary: OperationalEmailOutbox{Status: OperationalDegraded, Pending: 1000, Failed: 1000}},
		Maintenance:               maintenance,
	})

	snapshot := provider.Snapshot(context.Background())
	if snapshot.Version != (OperationalVersion{Version: "1.2.3", Commit: "abc123"}) {
		t.Fatalf("version = %#v", snapshot.Version)
	}
	if snapshot.ReleasePipeline.Status != OperationalReady || snapshot.ArtifactDelivery.Status != OperationalReady {
		t.Fatalf("release state = %#v artifact state = %#v", snapshot.ReleasePipeline, snapshot.ArtifactDelivery)
	}
	if snapshot.EmailOutbox.Pending != 100 || snapshot.EmailOutbox.Failed != 100 {
		t.Fatalf("outbox = %#v", snapshot.EmailOutbox)
	}
	if snapshot.Maintenance.Status != OperationalReady || snapshot.Maintenance.LastRunAt == nil {
		t.Fatalf("maintenance = %#v", snapshot.Maintenance)
	}
	body, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if bytes.Contains(body, []byte("public-key-marker")) || bytes.Contains(body, []byte("token-marker")) || bytes.Contains(body, []byte("release-key")) {
		t.Fatalf("snapshot exposed configuration: %s", body)
	}
}

func TestOperationalSnapshotProviderDegradesUnavailableOutbox(t *testing.T) {
	snapshot := newOperationalSnapshotProvider(Config{OperationalOutbox: testOperationalOutbox{err: errors.New("unavailable")}}).Snapshot(context.Background())
	if snapshot.Database.Status != OperationalUnavailable || snapshot.EmailOutbox.Status != OperationalUnavailable {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

type testArtifactDelivery struct{}

func (testArtifactDelivery) SignedURL(context.Context, string, time.Time) (string, error) {
	return "", nil
}

type testOperationalOutbox struct {
	summary OperationalEmailOutbox
	err     error
}

func (o testOperationalOutbox) OperationalSummary(context.Context) (OperationalEmailOutbox, error) {
	return o.summary, o.err
}

func TestOperationalSnapshotRoute(t *testing.T) {
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	ctx := context.Background()
	accountStore, err := accounts.Open(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	lockDatabaseTests(t, accountStore.DB())
	if _, err := accountStore.DB().ExecContext(ctx, `TRUNCATE sesame_admin_sessions, sesame_admin_accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("clear admin tables: %v", err)
	}
	adminService, err := adminstore.Open(ctx, databaseURL, bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = adminService.Close() })
	if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified) VALUES ('ops-snapshot', 'ops@example.invalid', 'test', 'ops', TRUE)`); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	actor := adminstore.Account{ID: "ops-snapshot", Email: "ops@example.invalid", Role: adminstore.RoleOps}

	t.Run("returns a healthy snapshot", func(t *testing.T) {
		maintenance := NewMaintenanceState()
		maintenance.Record(OperationalReady, time.Now().UTC())
		handler := New(Config{
			Admin:                     adminService,
			AdminOrigin:               "https://admin.example.invalid",
			Version:                   "1.2.3",
			Commit:                    "abc123",
			ReleaseCandidatePublicKey: []byte("public-key"),
			ReleaseCandidateKeyID:     "release-key",
			ReleaseCandidateTokenHash: []byte("token"),
			ArtifactDelivery:          testArtifactDelivery{},
			OperationalOutbox:         testOperationalOutbox{summary: OperationalEmailOutbox{Status: OperationalReady, Pending: 2}},
			Maintenance:               maintenance,
		})
		response := operationalSnapshotRequest(t, handler, adminService, actor)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"commit":"abc123"`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"database":{"status":"ready","timedOut":false}`)) || !bytes.Contains(response.Body.Bytes(), []byte(`"schema":{"status":"ready"`)) {
			t.Fatalf("healthy snapshot = %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("returns a degraded snapshot", func(t *testing.T) {
		handler := New(Config{Admin: adminService, AdminOrigin: "https://admin.example.invalid", OperationalSnapshot: testSnapshotProvider{snapshot: OperationalSnapshot{
			Database:    OperationalDatabase{Status: OperationalUnavailable},
			EmailOutbox: OperationalEmailOutbox{Status: OperationalDegraded, Failed: 1},
		}}})
		response := operationalSnapshotRequest(t, handler, adminService, actor)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"status":"degraded"`)) {
			t.Fatalf("degraded snapshot = %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("rejects an unauthenticated request", func(t *testing.T) {
		handler := New(Config{Admin: adminService, AdminOrigin: "https://admin.example.invalid", OperationalSnapshot: testSnapshotProvider{}})
		request := httptest.NewRequest(http.MethodGet, "/v1/admin/system/health", nil)
		request.Header.Set("Origin", "https://admin.example.invalid")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated status = %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("bounds dependency waits", func(t *testing.T) {
		handler := New(Config{Admin: adminService, AdminOrigin: "https://admin.example.invalid", OperationalSnapshot: testSnapshotProvider{waitForDeadline: true}})
		response := operationalSnapshotRequest(t, handler, adminService, actor)
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), []byte(`"timedOut":true`)) {
			t.Fatalf("timeout snapshot = %d: %s", response.Code, response.Body.String())
		}
	})
}

type testSnapshotProvider struct {
	snapshot        OperationalSnapshot
	waitForDeadline bool
}

func (p testSnapshotProvider) Snapshot(ctx context.Context) OperationalSnapshot {
	if p.waitForDeadline {
		_, bounded := ctx.Deadline()
		return OperationalSnapshot{Database: OperationalDatabase{Status: OperationalUnavailable, TimedOut: bounded}}
	}
	return p.snapshot
}

func operationalSnapshotRequest(t *testing.T, handler http.Handler, store *adminstore.Store, actor adminstore.Account) *httptest.ResponseRecorder {
	t.Helper()
	token := "snapshot-session-" + actor.ID + "-" + time.Now().UTC().Format("20060102150405.000000000")
	if err := store.CreateSession(context.Background(), actor, adminstore.HashToken(token), "", "test", time.Now().UTC().Add(time.Hour), time.Now().UTC().UnixNano()); err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/v1/admin/system/health", nil)
	request.Header.Set("Origin", "https://admin.example.invalid")
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
