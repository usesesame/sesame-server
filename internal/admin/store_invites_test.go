package admin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

func inviteTestStores(t *testing.T) (*Store, *accounts.PostgresStore) {
	t.Helper()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	accountStore, err := accounts.Open(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open account store: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	lockReleaseTests(t, accountStore.DB())
	store, err := Open(context.Background(), databaseURL, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, accountStore
}

func registrationWithInvite(email, code string) accounts.Registration {
	now := time.Now().UTC()
	return accounts.Registration{
		Email:                 email,
		PasswordHash:          "fictional-unused-hash",
		SessionTokenHash:      accounts.HashSessionToken("fictional-session-" + email),
		SessionExpiresAt:      now.Add(time.Hour),
		SessionLabel:          "test",
		VerificationTokenHash: accounts.HashSessionToken("fictional-verify-" + email),
		VerificationExpiresAt: now.Add(time.Hour),
		InviteHash:            accounts.HashSessionToken(code),
		TermsAcceptedAt:       now,
		TermsVersion:          "2026-08-18",
		PrivacyAcknowledgedAt: now,
		PrivacyVersion:        "2026-08-18",
	}
}

func TestBetaInviteIsSingleUseExpiringAndAudited(t *testing.T) {
	store, accountStore := inviteTestStores(t)
	ctx := context.Background()
	db := accountStore.DB()
	actor := Account{
		ID:    fmt.Sprintf("invite-admin-%d", time.Now().UnixNano()),
		Email: "invite-admin@example.invalid",
		Role:  RoleSuper,
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sesame_admin_accounts (id, email, password_hash, role, totp_verified) VALUES ($1, $2, 'test', 'super', TRUE)`, actor.ID, actor.Email); err != nil {
		t.Fatalf("create admin: %v", err)
	}
	emails := []string{"invite-one@example.invalid", "invite-expired@example.invalid", "invite-bound@example.invalid", "invite-none@example.invalid"}
	t.Cleanup(func() {
		for _, email := range emails {
			_, _ = db.ExecContext(context.Background(), `DELETE FROM sesame_beta_invites WHERE email = $1`, email)
			_, _ = db.ExecContext(context.Background(), `DELETE FROM sesame_accounts WHERE email = $1`, email)
		}
		_, _ = db.ExecContext(context.Background(), `DELETE FROM sesame_admin_accounts WHERE id = $1`, actor.ID)
	})

	code, err := store.CreateBetaInvite(ctx, actor, emails[0], time.Now().UTC().Add(time.Hour), "fictional-ip")
	if err != nil {
		t.Fatalf("create invite: %v", err)
	}
	if code == "" {
		t.Fatal("create invite returned an empty code")
	}
	var email string
	var maxUses, uses int
	var expires time.Time
	if err := db.QueryRowContext(ctx, `SELECT email, max_uses, uses, expires_at FROM sesame_beta_invites WHERE code_hash = $1`, accounts.HashSessionToken(code)).Scan(&email, &maxUses, &uses, &expires); err != nil {
		t.Fatalf("read invite: %v", err)
	}
	if email != emails[0] || maxUses != 1 || uses != 0 || !expires.After(time.Now()) {
		t.Fatalf("invite = email %q maxUses %d uses %d expires %v", email, maxUses, uses, expires)
	}
	var audited int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sesame_admin_audit_log WHERE admin_id = $1 AND action = 'user.invite.create' AND target_type = 'invite'`, actor.ID).Scan(&audited); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if audited != 1 {
		t.Fatalf("audit rows = %d, want 1", audited)
	}

	if _, err := accountStore.RegisterEligible(ctx, registrationWithInvite(emails[0], code)); err != nil {
		t.Fatalf("register with a fresh invite: %v", err)
	}
	if err := db.QueryRowContext(ctx, `SELECT uses FROM sesame_beta_invites WHERE code_hash = $1`, accounts.HashSessionToken(code)).Scan(&uses); err != nil {
		t.Fatalf("read used invite: %v", err)
	}
	if uses != 1 {
		t.Fatalf("invite uses = %d, want 1", uses)
	}
	if _, err := accountStore.RegisterEligible(ctx, registrationWithInvite(emails[0], code)); err == nil {
		t.Fatal("a replayed invite code registered a second account")
	}

	expiredCode, err := store.CreateBetaInvite(ctx, actor, emails[1], time.Now().UTC().Add(time.Hour), "fictional-ip")
	if err != nil {
		t.Fatalf("create expiring invite: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sesame_beta_invites SET expires_at = NOW() - INTERVAL '1 minute' WHERE code_hash = $1`, accounts.HashSessionToken(expiredCode)); err != nil {
		t.Fatalf("expire invite: %v", err)
	}
	if _, err := accountStore.RegisterEligible(ctx, registrationWithInvite(emails[1], expiredCode)); err == nil {
		t.Fatal("an expired invite code registered an account")
	}

	boundCode, err := store.CreateBetaInvite(ctx, actor, emails[2], time.Now().UTC().Add(time.Hour), "fictional-ip")
	if err != nil {
		t.Fatalf("create bound invite: %v", err)
	}
	if _, err := accountStore.RegisterEligible(ctx, registrationWithInvite(emails[3], boundCode)); err == nil {
		t.Fatal("an invite bound to another email registered an account")
	}

	if _, err := accountStore.RegisterEligible(ctx, registrationWithInvite(emails[3], "")); err == nil {
		t.Fatal("registration without an invite registered an account in invite mode")
	}
}
