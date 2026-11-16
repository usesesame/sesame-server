package accounts

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"
)

const renewalTolerance = time.Minute

func ageDesktopToken(t *testing.T, db *sql.DB, tokenHash []byte, age time.Duration) time.Time {
	t.Helper()
	var expiresAt time.Time
	if err := db.QueryRowContext(context.Background(), `
		UPDATE sesame_desktop_connections
		SET created_at = NOW() - make_interval(secs => $2), expires_at = NOW() - make_interval(secs => $2) + make_interval(secs => $3)
		WHERE token_hash = $1
		RETURNING expires_at
	`, tokenHash, age.Seconds(), DesktopConnectionLifetime.Seconds()).Scan(&expiresAt); err != nil {
		t.Fatalf("age desktop token: %v", err)
	}
	return expiresAt
}

func storedDesktopExpiry(t *testing.T, db *sql.DB, tokenHash []byte) time.Time {
	t.Helper()
	var expiresAt time.Time
	if err := db.QueryRowContext(context.Background(), `SELECT expires_at FROM sesame_desktop_connections WHERE token_hash = $1`, tokenHash).Scan(&expiresAt); err != nil {
		t.Fatalf("read desktop token expiry: %v", err)
	}
	return expiresAt
}

func expectExpiryNear(t *testing.T, name string, got, want time.Time) {
	t.Helper()
	if difference := got.Sub(want); difference < -renewalTolerance || difference > renewalTolerance {
		t.Fatalf("%s expiry = %s, want about %s", name, got.UTC(), want.UTC())
	}
}

func TestDesktopTokenRenewsWhenUsedLate(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-desktop-renew-late", "fictional-hash")
	token := bytes.Repeat([]byte{41}, 32)
	seedDesktopToken(t, db, "acct-desktop-renew-late", token)
	ageDesktopToken(t, db, token, 40*24*time.Hour)

	usedAt := time.Now()
	connection, err := store.DesktopConnectionForToken(ctx, token)
	if err != nil {
		t.Fatalf("a valid token used on day 40 was refused: %v", err)
	}
	expectExpiryNear(t, "returned", connection.ExpiresAt, usedAt.Add(DesktopConnectionLifetime))
	expectExpiryNear(t, "stored", storedDesktopExpiry(t, db, token), usedAt.Add(DesktopConnectionLifetime))
}

func TestDesktopTokenIsNotRewrittenWhenUsedEarly(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-desktop-renew-early", "fictional-hash")
	token := bytes.Repeat([]byte{42}, 32)
	seedDesktopToken(t, db, "acct-desktop-renew-early", token)
	original := ageDesktopToken(t, db, token, 10*24*time.Hour)

	connection, err := store.DesktopConnectionForToken(ctx, token)
	if err != nil {
		t.Fatalf("a valid token used on day 10 was refused: %v", err)
	}
	if !connection.ExpiresAt.Equal(original) {
		t.Fatalf("returned expiry = %s, want the original %s", connection.ExpiresAt, original)
	}
	if stored := storedDesktopExpiry(t, db, token); !stored.Equal(original) {
		t.Fatalf("a token used on day 10 was rewritten from %s to %s", original, stored)
	}
}

func TestExpiredDesktopTokenIsRefusedAndNotRevived(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-desktop-renew-expired", "fictional-hash")
	token := bytes.Repeat([]byte{43}, 32)
	seedDesktopToken(t, db, "acct-desktop-renew-expired", token)
	expired := ageDesktopToken(t, db, token, DesktopConnectionLifetime+time.Hour)

	if _, err := store.DesktopConnectionForToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token error = %v, want ErrNotFound", err)
	}
	if _, err := store.HeartbeatDesktopConnection(ctx, token, DesktopHeartbeat{ProtocolVersion: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired token heartbeat error = %v, want ErrNotFound", err)
	}
	if stored := storedDesktopExpiry(t, db, token); !stored.Equal(expired) {
		t.Fatalf("an expired token was revived from %s to %s", expired, stored)
	}
}

func TestSuspendedAccountDesktopTokenIsNotRenewed(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-desktop-renew-suspended", "fictional-hash")
	token := bytes.Repeat([]byte{44}, 32)
	seedDesktopToken(t, db, "acct-desktop-renew-suspended", token)
	original := ageDesktopToken(t, db, token, 40*24*time.Hour)
	if _, err := db.ExecContext(ctx, `UPDATE sesame_accounts SET suspended_at = NOW() WHERE id = $1`, "acct-desktop-renew-suspended"); err != nil {
		t.Fatalf("suspend fixture account: %v", err)
	}

	connection, err := store.DesktopConnectionForToken(ctx, token)
	if err != nil {
		t.Fatalf("suspended account token lookup: %v", err)
	}
	if !connection.AccountSuspended {
		t.Fatal("the connection did not report the suspension")
	}
	if _, err := store.HeartbeatDesktopConnection(ctx, token, DesktopHeartbeat{ProtocolVersion: 1}); err != nil {
		t.Fatalf("suspended account heartbeat: %v", err)
	}
	if stored := storedDesktopExpiry(t, db, token); !stored.Equal(original) {
		t.Fatalf("a suspended account's token was renewed from %s to %s", original, stored)
	}
}

func TestDesktopHeartbeatRenewsOnlyWhenDue(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-desktop-renew-heartbeat", "fictional-hash")
	early := bytes.Repeat([]byte{45}, 32)
	late := bytes.Repeat([]byte{46}, 32)
	seedDesktopToken(t, db, "acct-desktop-renew-heartbeat", early)
	seedDesktopToken(t, db, "acct-desktop-renew-heartbeat", late)
	original := ageDesktopToken(t, db, early, 10*24*time.Hour)
	ageDesktopToken(t, db, late, 40*24*time.Hour)

	heartbeat := DesktopHeartbeat{AppVersion: "1.0.0", Platform: "linux", Architecture: "x86_64", UpdateChannel: "stable", ProtocolVersion: 1}
	connection, err := store.HeartbeatDesktopConnection(ctx, early, heartbeat)
	if err != nil {
		t.Fatalf("early heartbeat: %v", err)
	}
	if !connection.ExpiresAt.Equal(original) || !storedDesktopExpiry(t, db, early).Equal(original) {
		t.Fatalf("an early heartbeat moved the expiry from %s to %s", original, connection.ExpiresAt)
	}

	usedAt := time.Now()
	connection, err = store.HeartbeatDesktopConnection(ctx, late, heartbeat)
	if err != nil {
		t.Fatalf("late heartbeat: %v", err)
	}
	expectExpiryNear(t, "heartbeat returned", connection.ExpiresAt, usedAt.Add(DesktopConnectionLifetime))
	expectExpiryNear(t, "heartbeat stored", storedDesktopExpiry(t, db, late), usedAt.Add(DesktopConnectionLifetime))
}

func TestRevokedDesktopTokenStaysRevoked(t *testing.T) {
	store, db := lifecycleTestStore(t)
	ctx := context.Background()
	seedDesktopTokenAccount(t, db, "acct-desktop-renew-revoked", "fictional-hash")
	token := bytes.Repeat([]byte{47}, 32)
	seedDesktopToken(t, db, "acct-desktop-renew-revoked", token)
	ageDesktopToken(t, db, token, 40*24*time.Hour)
	if err := store.RevokeDesktopConnection(ctx, token); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := store.DesktopConnectionForToken(ctx, token); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token error = %v, want ErrNotFound", err)
	}
	if _, err := store.HeartbeatDesktopConnection(ctx, token, DesktopHeartbeat{ProtocolVersion: 1}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked token heartbeat error = %v, want ErrNotFound", err)
	}
	if count := countRows(t, db, `SELECT COUNT(*) FROM sesame_desktop_connections WHERE token_hash = $1`, token); count != 0 {
		t.Fatalf("revoked desktop connections = %d, want 0", count)
	}
}
