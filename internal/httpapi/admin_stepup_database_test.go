package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
)

const adminStepUpPassword = "fictional-admin-password-123"

func newAdminStepUpEnv(t *testing.T) *supportTestEnv {
	t.Helper()
	return newSupportTestEnv(t, func(config *Config) {
		config.AdminStepUpTTL = time.Minute
		config.DeploymentProfile = DeploymentProfileProject
	})
}

func seedStepUpAdmin(t *testing.T, env *supportTestEnv, id, email string, role adminstore.Role) adminstore.Account {
	t.Helper()
	passwordHash, err := accounts.HashPassword(adminStepUpPassword)
	if err != nil {
		t.Fatalf("hash admin password: %v", err)
	}
	if _, err := env.db.ExecContext(context.Background(), `DELETE FROM sesame_admin_accounts WHERE id = $1`, id); err != nil {
		t.Fatalf("clear admin %s: %v", id, err)
	}
	if _, err := env.db.ExecContext(context.Background(), `
		INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified)
		VALUES ($1, $2, $3, $4, TRUE)
	`, id, email, passwordHash, string(role)); err != nil {
		t.Fatalf("seed admin %s: %v", id, err)
	}
	t.Cleanup(func() {
		if _, err := env.db.ExecContext(context.Background(), `DELETE FROM sesame_admin_accounts WHERE id = $1`, id); err != nil {
			t.Error(err)
		}
	})
	return adminstore.Account{ID: id, Email: email, Role: role}
}

func seedTOTPAdmin(t *testing.T, env *supportTestEnv, email string) (adminstore.Account, string) {
	t.Helper()
	ctx := context.Background()
	token, err := env.adminStore.BootstrapSuper(ctx, email, time.Now().UTC().Add(time.Hour))
	if err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	account, secret, err := env.adminStore.SetupDetails(ctx, adminstore.HashToken(token), time.Now().UTC())
	if err != nil {
		t.Fatalf("read setup secret: %v", err)
	}
	passwordHash, err := accounts.HashPassword(adminStepUpPassword)
	if err != nil {
		t.Fatalf("hash admin password: %v", err)
	}
	account, err = env.adminStore.CompleteSetup(ctx, adminstore.HashToken(token), passwordHash, time.Now().UTC(), "", 1)
	if err != nil {
		t.Fatalf("complete admin setup: %v", err)
	}
	t.Cleanup(func() {
		if _, err := env.db.ExecContext(context.Background(), `DELETE FROM sesame_admin_accounts WHERE id = $1`, account.ID); err != nil {
			t.Error(err)
		}
	})
	return account, secret
}

func stepUpSession(t *testing.T, env *supportTestEnv, actor adminstore.Account, authenticatedAt time.Time, totpCounter int64) string {
	t.Helper()
	token := fmt.Sprintf("step-up-session-%s-%d", actor.ID, time.Now().UnixNano())
	if err := env.adminStore.CreateSession(context.Background(), actor, adminstore.HashToken(token), "", "step-up-test", time.Now().UTC().Add(time.Hour), totpCounter); err != nil {
		t.Fatalf("create admin session: %v", err)
	}
	if _, err := env.db.ExecContext(context.Background(), `
		UPDATE sesame_admin_sessions SET authenticated_at = $2 WHERE token_hash = $1
	`, adminstore.HashToken(token), authenticatedAt); err != nil {
		t.Fatalf("age admin session: %v", err)
	}
	return token
}

func adminSessionRequest(t *testing.T, env *supportTestEnv, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal admin request: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}
	var request *http.Request
	if reader == nil {
		request = httptest.NewRequest(method, path, nil)
	} else {
		request = httptest.NewRequest(method, path, reader)
		request.Header.Set("Content-Type", "application/json")
	}
	request.RemoteAddr = env.nextIP() + ":40000"
	request.Header.Set("Origin", supportTestAdminOrigin)
	request.Header.Set("X-Sesame-CSRF", "test-csrf")
	request.AddCookie(&http.Cookie{Name: adminSessionCookie, Value: token})
	request.AddCookie(&http.Cookie{Name: adminCSRFCookie, Value: "test-csrf"})
	response := httptest.NewRecorder()
	env.handler.ServeHTTP(response, request)
	return response
}

func seedStepUpFlag(t *testing.T, env *supportTestEnv) {
	t.Helper()
	if _, err := env.db.ExecContext(context.Background(), `
		INSERT INTO sesame_feature_flags (key, value) VALUES ('registration_mode', 'invite')
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value
	`); err != nil {
		t.Fatalf("seed registration mode: %v", err)
	}
	t.Cleanup(func() {
		if _, err := env.db.ExecContext(context.Background(), `DELETE FROM sesame_feature_flags WHERE key = 'registration_mode'`); err != nil {
			t.Error(err)
		}
	})
}

func stepUpReleaseCandidate() adminstore.ReleaseCandidate {
	version := fmt.Sprintf("9.9.%d", time.Now().UnixNano()%1_000_000)
	digest := strings.Repeat("d", 64)
	return adminstore.ReleaseCandidate{
		SchemaVersion:         3,
		Version:               version,
		Channel:               "beta",
		Platform:              "windows",
		Architecture:          "x86_64",
		SupportedWindows:      "Windows 10",
		ReleaseNotesURL:       "https://example.invalid/releases/" + version,
		SetDigest:             digest,
		CandidateSigningKeyID: "fictional-step-up-key",
		CandidateSignature:    "fictional-step-up-signature",
		SigningPayload:        "fictional-step-up-payload-" + digest,
		Artifacts: []adminstore.ReleaseArtifact{{
			Format:               "nsis",
			Architecture:         "x86_64",
			URL:                  "https://downloads.example.invalid/" + version + "/Sesame.exe",
			ObjectKey:            "releases/" + version + "/Sesame.exe",
			SHA256:               digest,
			Bytes:                1,
			UpdaterCapable:       true,
			UpdaterSignature:     strings.Repeat("s", 64),
			UpdaterSigningKeyID:  "fictional-step-up-updater-key",
			DistributionClass:    "early_access",
			SigstoreEvidence:     map[string]any{"verified": true},
			SigstoreVerified:     true,
			SigstoreIssuer:       "https://token.actions.githubusercontent.com",
			SigstoreIdentity:     "fictional-step-up-identity",
			SigstoreBundleSHA256: strings.Repeat("c", 64),
		}},
	}
}

func stepUpTOTPCode(t *testing.T, secret string, at time.Time) string {
	t.Helper()
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(secret))
	if err != nil {
		t.Fatalf("decode TOTP secret: %v", err)
	}
	var message [8]byte
	binary.BigEndian.PutUint64(message[:], uint64(at.Unix()/30))
	mac := hmac.New(sha1.New, key)
	_, _ = mac.Write(message[:])
	digest := mac.Sum(nil)
	offset := digest[len(digest)-1] & 0x0f
	value := (uint32(digest[offset])&0x7f)<<24 | uint32(digest[offset+1])<<16 | uint32(digest[offset+2])<<8 | uint32(digest[offset+3])
	return fmt.Sprintf("%06d", value%1_000_000)
}

func countAdminAudit(t *testing.T, env *supportTestEnv, action, adminID string) int {
	t.Helper()
	var count int
	if err := env.db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM sesame_admin_audit_log WHERE action = $1 AND admin_id = $2
	`, action, adminID).Scan(&count); err != nil {
		t.Fatalf("count %s audit rows: %v", action, err)
	}
	return count
}

func TestAdminDestructiveActionsNeedFreshStepUp(t *testing.T) {
	env := newAdminStepUpEnv(t)
	ctx := context.Background()
	actor := seedStepUpAdmin(t, env, "admin-step-up-actions", "admin-step-up-actions@example.invalid", adminstore.RoleSuper)
	env.seedAccount(t, "acct-step-up-actions", "step-up-actions@example.invalid")
	seedStepUpFlag(t, env)

	release, err := env.adminStore.AcceptReleaseCandidate(ctx, adminstore.Account{Email: "release-pipeline"}, stepUpReleaseCandidate(), "fictional-ip")
	if err != nil {
		t.Fatalf("accept release candidate: %v", err)
	}
	t.Cleanup(func() {
		if _, err := env.db.ExecContext(context.Background(), `TRUNCATE sesame_releases CASCADE`); err != nil {
			t.Error(err)
		}
	})

	token := stepUpSession(t, env, actor, time.Now().UTC().Add(-10*time.Minute), time.Now().UTC().UnixNano())
	flagPath := "/v1/admin/flags/registration_mode"

	response := adminSessionRequest(t, env, token, http.MethodDelete, "/v1/admin/users/acct-step-up-actions", nil)
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("user delete without a step-up = %d %q", response.Code, errorCode(t, response))
	}
	response = adminSessionRequest(t, env, token, http.MethodPatch, flagPath, map[string]any{"value": "public"})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("flag change without a step-up = %d %q", response.Code, errorCode(t, response))
	}
	response = adminSessionRequest(t, env, token, http.MethodPost, "/v1/admin/releases/"+release.ID+"/publish", map[string]any{"expectedManifestRevision": 1})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("release publish without a step-up = %d %q", response.Code, errorCode(t, response))
	}

	var users int
	if err := env.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_accounts WHERE id = 'acct-step-up-actions'`).Scan(&users); err != nil {
		t.Fatalf("count account: %v", err)
	}
	var mode string
	if err := env.db.QueryRowContext(ctx, `SELECT value FROM sesame_feature_flags WHERE key = 'registration_mode'`).Scan(&mode); err != nil {
		t.Fatalf("read registration mode: %v", err)
	}
	var status string
	if err := env.db.QueryRowContext(ctx, `SELECT status FROM sesame_releases WHERE id = $1`, release.ID).Scan(&status); err != nil {
		t.Fatalf("read release status: %v", err)
	}
	if users != 1 || mode == "public" || status != "draft" {
		t.Fatalf("refused actions changed state: users %d mode %q status %q", users, mode, status)
	}
	for _, action := range []string{"user.delete", "flag.update", "release.publish"} {
		if count := countAdminAudit(t, env, action, actor.ID); count != 0 {
			t.Fatalf("%s audit rows after refusals = %d, want 0", action, count)
		}
	}

	response = adminSessionRequest(t, env, token, http.MethodPost, "/v1/admin/auth/step-up", map[string]any{"password": adminStepUpPassword})
	if response.Code != http.StatusOK {
		t.Fatalf("password step-up = %d: %s", response.Code, response.Body.String())
	}
	var receipt struct {
		StepUpExpiresAt time.Time `json:"stepUpExpiresAt"`
		WindowSeconds   int       `json:"windowSeconds"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatalf("decode step-up receipt %q: %v", response.Body.String(), err)
	}
	if receipt.WindowSeconds != 60 || time.Until(receipt.StepUpExpiresAt) <= 0 {
		t.Fatalf("step-up receipt = %+v, want a 60 second window", receipt)
	}

	response = adminSessionRequest(t, env, token, http.MethodDelete, "/v1/admin/users/acct-step-up-actions", nil)
	if response.Code != http.StatusNoContent {
		t.Fatalf("user delete after a step-up = %d: %s", response.Code, response.Body.String())
	}
	response = adminSessionRequest(t, env, token, http.MethodPatch, flagPath, map[string]any{"value": "public"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("flag change after a step-up = %d: %s", response.Code, response.Body.String())
	}
	response = adminSessionRequest(t, env, token, http.MethodPost, "/v1/admin/releases/"+release.ID+"/publish", map[string]any{"expectedManifestRevision": 1})
	if response.Code != http.StatusNoContent {
		t.Fatalf("release publish after a step-up = %d: %s", response.Code, response.Body.String())
	}

	if err := env.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_accounts WHERE id = 'acct-step-up-actions'`).Scan(&users); err != nil {
		t.Fatalf("count account after delete: %v", err)
	}
	if err := env.db.QueryRowContext(ctx, `SELECT value FROM sesame_feature_flags WHERE key = 'registration_mode'`).Scan(&mode); err != nil {
		t.Fatalf("read registration mode after update: %v", err)
	}
	if err := env.db.QueryRowContext(ctx, `SELECT status FROM sesame_releases WHERE id = $1`, release.ID).Scan(&status); err != nil {
		t.Fatalf("read release status after publish: %v", err)
	}
	if users != 0 || mode != "public" || status != "published" {
		t.Fatalf("allowed actions state = users %d mode %q status %q", users, mode, status)
	}
	for _, action := range []string{"user.delete", "flag.update", "release.publish"} {
		if count := countAdminAudit(t, env, action, actor.ID); count != 1 {
			t.Fatalf("%s audit rows after the step-up = %d, want 1", action, count)
		}
	}
	if count := countAdminAudit(t, env, "admin.step_up", actor.ID); count != 1 {
		t.Fatalf("admin.step_up audit rows = %d, want 1", count)
	}
	var method string
	if err := env.db.QueryRowContext(ctx, `SELECT detail->>'method' FROM sesame_admin_audit_log WHERE action = 'admin.step_up' AND admin_id = $1`, actor.ID).Scan(&method); err != nil {
		t.Fatalf("read step-up audit detail: %v", err)
	}
	if method != "password" {
		t.Fatalf("step-up audit method = %q, want password", method)
	}
}

func TestAdminStepUpWindowExpires(t *testing.T) {
	env := newAdminStepUpEnv(t)
	ctx := context.Background()
	actor := seedStepUpAdmin(t, env, "admin-step-up-window", "admin-step-up-window@example.invalid", adminstore.RoleSuper)
	seedStepUpFlag(t, env)
	flagPath := "/v1/admin/flags/registration_mode"
	token := stepUpSession(t, env, actor, time.Now().UTC().Add(-10*time.Minute), time.Now().UTC().UnixNano())

	response := adminSessionRequest(t, env, token, http.MethodPatch, flagPath, map[string]any{"value": "public"})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("expired step-up = %d %q", response.Code, errorCode(t, response))
	}
	response = adminSessionRequest(t, env, token, http.MethodPost, "/v1/admin/auth/step-up", map[string]any{"password": adminStepUpPassword})
	if response.Code != http.StatusOK {
		t.Fatalf("step-up = %d: %s", response.Code, response.Body.String())
	}
	response = adminSessionRequest(t, env, token, http.MethodPatch, flagPath, map[string]any{"value": "public"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("flag change inside the window = %d: %s", response.Code, response.Body.String())
	}

	if _, err := env.db.ExecContext(ctx, `
		UPDATE sesame_admin_sessions SET authenticated_at = NOW() - INTERVAL '10 minutes' WHERE token_hash = $1
	`, adminstore.HashToken(token)); err != nil {
		t.Fatalf("expire the step-up window: %v", err)
	}
	response = adminSessionRequest(t, env, token, http.MethodPatch, flagPath, map[string]any{"value": "invite"})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("action after the window expired = %d %q", response.Code, errorCode(t, response))
	}
	if count := countAdminAudit(t, env, "flag.update", actor.ID); count != 1 {
		t.Fatalf("flag.update audit rows = %d, want only the in-window change", count)
	}
	if count := countAdminAudit(t, env, "admin.step_up", actor.ID); count != 1 {
		t.Fatalf("admin.step_up audit rows = %d, want 1", count)
	}
}

func TestAdminStepUpDoesNotCarryAcrossSessions(t *testing.T) {
	env := newAdminStepUpEnv(t)
	actor := seedStepUpAdmin(t, env, "admin-step-up-sessions", "admin-step-up-sessions@example.invalid", adminstore.RoleSuper)
	seedStepUpFlag(t, env)
	flagPath := "/v1/admin/flags/registration_mode"
	staleToken := stepUpSession(t, env, actor, time.Now().UTC().Add(-10*time.Minute), time.Now().UTC().UnixNano())
	freshToken := stepUpSession(t, env, actor, time.Now().UTC(), time.Now().UTC().UnixNano())

	response := adminSessionRequest(t, env, staleToken, http.MethodPatch, flagPath, map[string]any{"value": "public"})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("action before any step-up = %d %q", response.Code, errorCode(t, response))
	}
	response = adminSessionRequest(t, env, freshToken, http.MethodPost, "/v1/admin/auth/step-up", map[string]any{"password": adminStepUpPassword})
	if response.Code != http.StatusOK {
		t.Fatalf("step-up on the second session = %d: %s", response.Code, response.Body.String())
	}
	response = adminSessionRequest(t, env, staleToken, http.MethodPatch, flagPath, map[string]any{"value": "public"})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("other session after a step-up = %d %q", response.Code, errorCode(t, response))
	}
	response = adminSessionRequest(t, env, freshToken, http.MethodPatch, flagPath, map[string]any{"value": "public"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("stepped-up session = %d: %s", response.Code, response.Body.String())
	}
	if count := countAdminAudit(t, env, "admin.step_up", actor.ID); count != 1 {
		t.Fatalf("admin.step_up audit rows = %d, want 1", count)
	}
}

func TestAdminStepUpAcceptsTOTPAndRejectsReplay(t *testing.T) {
	env := newAdminStepUpEnv(t)
	actor, secret := seedTOTPAdmin(t, env, "admin-step-up-totp@example.invalid")
	seedStepUpFlag(t, env)
	token := stepUpSession(t, env, actor, time.Now().UTC().Add(-10*time.Minute), 2)

	response := adminSessionRequest(t, env, token, http.MethodPatch, "/v1/admin/flags/registration_mode", map[string]any{"value": "public"})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("flag change without a TOTP step-up = %d %q", response.Code, errorCode(t, response))
	}
	code := stepUpTOTPCode(t, secret, time.Now().UTC())
	response = adminSessionRequest(t, env, token, http.MethodPost, "/v1/admin/auth/step-up", map[string]any{"code": code})
	if response.Code != http.StatusOK {
		t.Fatalf("TOTP step-up = %d: %s", response.Code, response.Body.String())
	}
	response = adminSessionRequest(t, env, token, http.MethodPatch, "/v1/admin/flags/registration_mode", map[string]any{"value": "public"})
	if response.Code != http.StatusNoContent {
		t.Fatalf("flag change after a TOTP step-up = %d: %s", response.Code, response.Body.String())
	}
	response = adminSessionRequest(t, env, token, http.MethodPost, "/v1/admin/auth/step-up", map[string]any{"code": code})
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "invalid_admin_credentials" {
		t.Fatalf("replayed TOTP code = %d %q", response.Code, errorCode(t, response))
	}
	if count := countAdminAudit(t, env, "admin.step_up", actor.ID); count != 1 {
		t.Fatalf("admin.step_up audit rows = %d, want 1", count)
	}
	var method string
	if err := env.db.QueryRowContext(context.Background(), `SELECT detail->>'method' FROM sesame_admin_audit_log WHERE action = 'admin.step_up' AND admin_id = $1`, actor.ID).Scan(&method); err != nil {
		t.Fatalf("read step-up audit detail: %v", err)
	}
	if method != "totp" {
		t.Fatalf("step-up audit method = %q, want totp", method)
	}
}

func TestAdminStepUpRejectsBadCredentials(t *testing.T) {
	env := newAdminStepUpEnv(t)
	actor := seedStepUpAdmin(t, env, "admin-step-up-bad", "admin-step-up-bad@example.invalid", adminstore.RoleSuper)
	seedStepUpFlag(t, env)
	token := stepUpSession(t, env, actor, time.Now().UTC().Add(-10*time.Minute), time.Now().UTC().UnixNano())

	response := adminSessionRequest(t, env, token, http.MethodPost, "/v1/admin/auth/step-up", map[string]any{"password": "fictional-wrong-password"})
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "invalid_admin_credentials" {
		t.Fatalf("wrong password step-up = %d %q", response.Code, errorCode(t, response))
	}
	response = adminSessionRequest(t, env, token, http.MethodPost, "/v1/admin/auth/step-up", map[string]any{"password": adminStepUpPassword, "code": "000000"})
	if response.Code != http.StatusBadRequest || errorCode(t, response) != "invalid_admin_step_up" {
		t.Fatalf("password and code step-up = %d %q", response.Code, errorCode(t, response))
	}
	response = adminSessionRequest(t, env, "fictional-unknown-session", http.MethodPost, "/v1/admin/auth/step-up", map[string]any{"password": adminStepUpPassword})
	if response.Code != http.StatusUnauthorized || errorCode(t, response) != "admin_not_authenticated" {
		t.Fatalf("step-up without a session = %d %q", response.Code, errorCode(t, response))
	}
	response = adminSessionRequest(t, env, token, http.MethodPatch, "/v1/admin/flags/registration_mode", map[string]any{"value": "public"})
	if response.Code != http.StatusForbidden || errorCode(t, response) != "admin_step_up_required" {
		t.Fatalf("action after refused step-ups = %d %q", response.Code, errorCode(t, response))
	}
	if count := countAdminAudit(t, env, "admin.step_up", actor.ID); count != 0 {
		t.Fatalf("admin.step_up audit rows after refusals = %d, want 0", count)
	}
}

func TestAdminStepUpMigrationOnFreshDatabase(t *testing.T) {
	conn := newSupportMigrationSchema(t, supportEmailDatabase(t))
	applySupportMigrationsThrough(t, conn, "0043")
	ctx := context.Background()

	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role) VALUES ('admin-fresh', 'admin-fresh@example.invalid', 'fictional-unused-hash', 'super')`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_admin_sessions (token_hash, admin_id, expires_at) VALUES ($1, 'admin-fresh', NOW() + INTERVAL '1 hour')`, bytes.Repeat([]byte{5}, 32)); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	var authenticatedAt time.Time
	if err := conn.QueryRowContext(ctx, `SELECT authenticated_at FROM sesame_admin_sessions WHERE token_hash = $1`, bytes.Repeat([]byte{5}, 32)).Scan(&authenticatedAt); err != nil {
		t.Fatalf("read session authentication: %v", err)
	}
	if since := time.Since(authenticatedAt); since < 0 || since > time.Minute {
		t.Fatalf("fresh session authenticated_at = %v, want the insert time", authenticatedAt)
	}
}

func TestAdminStepUpMigrationUpgradesAnExistingSchema(t *testing.T) {
	conn := newSupportMigrationSchema(t, supportEmailDatabase(t))
	applySupportMigrationsThrough(t, conn, "0041")
	ctx := context.Background()

	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role) VALUES ('admin-upgrade', 'admin-upgrade@example.invalid', 'fictional-unused-hash', 'super')`); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_admin_sessions (token_hash, admin_id, expires_at) VALUES ($1, 'admin-upgrade', NOW() + INTERVAL '1 hour')`, bytes.Repeat([]byte{6}, 32)); err != nil {
		t.Fatalf("seed session before the upgrade: %v", err)
	}
	body, err := os.ReadFile(filepath.Join("..", "accounts", "migrations", "0043_admin_session_step_up.sql"))
	if err != nil {
		t.Fatalf("read migration 0043: %v", err)
	}
	if _, err := conn.ExecContext(ctx, string(body)); err != nil {
		t.Fatalf("apply migration 0043 to an existing schema: %v", err)
	}

	var authenticatedAt time.Time
	if err := conn.QueryRowContext(ctx, `SELECT authenticated_at FROM sesame_admin_sessions WHERE token_hash = $1`, bytes.Repeat([]byte{6}, 32)).Scan(&authenticatedAt); err != nil {
		t.Fatalf("read existing session authentication: %v", err)
	}
	if authenticatedAt.After(time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("existing session authenticated_at = %v, want the fail-closed epoch value", authenticatedAt)
	}
	if _, err := conn.ExecContext(ctx, `INSERT INTO sesame_admin_sessions (token_hash, admin_id, expires_at) VALUES ($1, 'admin-upgrade', NOW() + INTERVAL '1 hour')`, bytes.Repeat([]byte{7}, 32)); err != nil {
		t.Fatalf("seed session after the upgrade: %v", err)
	}
	if err := conn.QueryRowContext(ctx, `SELECT authenticated_at FROM sesame_admin_sessions WHERE token_hash = $1`, bytes.Repeat([]byte{7}, 32)).Scan(&authenticatedAt); err != nil {
		t.Fatalf("read new session authentication: %v", err)
	}
	if since := time.Since(authenticatedAt); since < 0 || since > time.Minute {
		t.Fatalf("new session authenticated_at = %v, want the insert time", authenticatedAt)
	}
}
