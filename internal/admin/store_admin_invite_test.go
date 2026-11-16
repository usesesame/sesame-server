package admin

import (
	"context"
	"errors"
	"testing"
	"time"

	"usesesame.app/backend/internal/accounts"
)

func TestInvitingAnAdminWithBusyPasswordWorkCommitsNothing(t *testing.T) {
	store, accountStore := inviteTestStores(t)
	db := accountStore.DB()
	const email = "admin-invite-busy@example.invalid"
	t.Cleanup(func() {
		_, _ = db.ExecContext(context.Background(), `DELETE FROM sesame_admin_accounts WHERE email = $1`, email)
	})
	actor := Account{ID: "admin-invite-busy-actor", Email: "admin-invite-busy-actor@example.invalid", Role: RoleSuper}
	ended, cancel := context.WithCancel(context.Background())
	cancel()

	created, token, err := store.InviteAdmin(ended, actor, email, RoleSupport, time.Now().UTC().Add(time.Hour), "fictional-ip")
	if !errors.Is(err, accounts.ErrPasswordWorkBusy) || created.ID != "" || token != "" {
		t.Fatalf("invite with an ended request = %+v, %q, %v, want a busy error", created, token, err)
	}
	var stored int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_admin_accounts WHERE email = $1`, email).Scan(&stored); err != nil {
		t.Fatalf("count administrators: %v", err)
	}
	if stored != 0 {
		t.Fatalf("administrators stored after a busy invite = %d, want 0", stored)
	}
}
