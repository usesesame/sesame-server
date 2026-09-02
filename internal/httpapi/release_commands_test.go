package httpapi

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

func TestReleaseCommandRoutes(t *testing.T) {
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
	if _, err := accountStore.DB().ExecContext(ctx, `TRUNCATE sesame_releases, sesame_admin_audit_log, sesame_admin_sessions, sesame_admin_accounts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("clear release tables: %v", err)
	}
	adminStore, err := adminstore.Open(ctx, databaseURL, bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = adminStore.Close() })
	if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified) VALUES ('ops-test', 'ops@example.invalid', 'test', 'ops', TRUE), ('support-test', 'support@example.invalid', 'test', 'support', TRUE)`); err != nil {
		t.Fatalf("create admins: %v", err)
	}
	if _, err := accountStore.DB().ExecContext(ctx, `INSERT INTO sesame_releases (id, channel, platform, architecture, version, download_url, artifact_object_key, sha256, signature, signing_key_id, supported_windows, release_notes_url, rollback_notice, status, rollout_percent, update_enabled, kill_switch, published_at) VALUES ('release-test', 'beta', 'windows', 'x86_64', '0.2.3', 'https://downloads.example.invalid/Sesame.exe', 'releases/0.2.3/Sesame.exe', repeat('a', 64), repeat('s', 64), 'test-key', 'Windows 10', 'https://example.invalid/releases/0.2.3', '', 'published', 100, TRUE, FALSE, NOW())`); err != nil {
		t.Fatalf("create release: %v", err)
	}
	handler := New(Config{Admin: adminStore, AdminOrigin: "https://admin.example.invalid"})

	t.Run("checks revisions", func(t *testing.T) {
		response := releaseCommandRequest(t, handler, adminStore, adminstore.Account{ID: "ops-test", Email: "ops@example.invalid", Role: adminstore.RoleOps}, "/v1/admin/releases/release-test/rollout", map[string]any{"expectedManifestRevision": 1, "rolloutPercent": 25})
		if response.Code != 204 {
			t.Fatalf("rollout status = %d: %s", response.Code, response.Body.String())
		}
		response = releaseCommandRequest(t, handler, adminStore, adminstore.Account{ID: "ops-test", Email: "ops@example.invalid", Role: adminstore.RoleOps}, "/v1/admin/releases/release-test/rollout", map[string]any{"expectedManifestRevision": 1, "rolloutPercent": 50})
		if response.Code != 409 {
			t.Fatalf("stale rollout status = %d: %s", response.Code, response.Body.String())
		}
	})

	t.Run("requires release permission", func(t *testing.T) {
		response := releaseCommandRequest(t, handler, adminStore, adminstore.Account{ID: "support-test", Email: "support@example.invalid", Role: adminstore.RoleSupport}, "/v1/admin/releases/release-test/emergency-stop", map[string]any{"expectedManifestRevision": 2})
		if response.Code != 403 {
			t.Fatalf("support role status = %d: %s", response.Code, response.Body.String())
		}
	})
}

func lockDatabaseTests(t *testing.T, db *sql.DB) {
	t.Helper()
	const lockID int64 = 762374923
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("reserve test database connection: %v", err)
	}
	if _, err := conn.ExecContext(context.Background(), `SELECT pg_advisory_lock($1)`, lockID); err != nil {
		_ = conn.Close()
		t.Fatalf("lock test database: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, lockID)
		_ = conn.Close()
	})
}

func releaseCommandRequest(t *testing.T, handler http.Handler, store *adminstore.Store, actor adminstore.Account, path string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	token := fmt.Sprintf("test-admin-session-%s-%d", actor.ID, time.Now().UnixNano())
	if err := store.CreateSession(context.Background(), actor, adminstore.HashToken(token), "", "test", time.Now().UTC().Add(time.Hour), time.Now().UTC().UnixNano()); err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	request := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(encoded))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://admin.example.invalid")
	request.Header.Set("X-Sesame-CSRF", "test-csrf")
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
	request.AddCookie(&http.Cookie{Name: adminCSRFCookie, Value: "test-csrf"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
