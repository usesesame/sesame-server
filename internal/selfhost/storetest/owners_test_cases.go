package storetest

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"usesesame.app/backend/internal/authkit"
	"usesesame.app/backend/internal/selfhost"
)

func testSetup(t *testing.T, h Harness) {
	each(t, h, "first owner setup is single use", func(f *fixture) {
		required, err := f.store.SetupRequired(f.ctx)
		f.must(err)
		if !required {
			t.Fatal("fresh store must require setup")
		}
		issue, err := f.store.StartFirstSetup(f.ctx)
		f.must(err)
		if issue.Token == "" || !issue.ExpiresAt.Equal(f.clock.Now().Add(selfhost.SetupTokenTTL)) {
			t.Fatalf("unexpected issue %+v", issue)
		}
		first, err := f.store.SetupDetails(f.ctx, issue.Token)
		f.must(err)
		second, err := f.store.SetupDetails(f.ctx, issue.Token)
		f.must(err)
		if first.TOTPSecret == "" || first.TOTPSecret != second.TOTPSecret || !first.FirstOwner {
			t.Fatalf("setup details must be stable: %+v %+v", first, second)
		}
		f.secrets["avery"] = first.TOTPSecret
		login, err := f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "Avery", Password: testPassword, Code: f.code("Avery")})
		f.must(err)
		if login.Token == "" || login.Session.Owner.Name != "Avery" || login.Session.CSRFToken == "" {
			t.Fatalf("unexpected login %+v", login)
		}
		required, err = f.store.SetupRequired(f.ctx)
		f.must(err)
		if required {
			t.Fatal("setup must be complete")
		}
		_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "Mallory", Password: testPassword, Code: f.code("Avery")})
		f.expect(err, selfhost.ErrSetupTokenInvalid)
		_, err = f.store.SetupDetails(f.ctx, issue.Token)
		f.expect(err, selfhost.ErrSetupTokenInvalid)
		_, err = f.store.StartFirstSetup(f.ctx)
		f.expect(err, selfhost.ErrConflict)
	})
	each(t, h, "wrong code and weak password do not consume the token", func(f *fixture) {
		issue, err := f.store.StartFirstSetup(f.ctx)
		f.must(err)
		details, err := f.store.SetupDetails(f.ctx, issue.Token)
		f.must(err)
		f.secrets["avery"] = details.TOTPSecret
		_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "Avery", Password: testPassword, Code: "000000"})
		f.expect(err, selfhost.ErrInvalidCredentials)
		_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "Avery", Password: "short", Code: f.code("Avery")})
		f.expect(err, selfhost.ErrInvalidInput)
		_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "  ", Password: testPassword, Code: f.code("Avery")})
		f.expect(err, selfhost.ErrInvalidInput)
		_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "Avery", Password: testPassword, Code: f.code("Avery")})
		f.must(err)
	})
	each(t, h, "setup without details cannot complete", func(f *fixture) {
		issue, err := f.store.StartFirstSetup(f.ctx)
		f.must(err)
		_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "Avery", Password: testPassword, Code: "123456"})
		f.expect(err, selfhost.ErrInvalidCredentials)
	})
	each(t, h, "expired and malformed setup tokens are refused", func(f *fixture) {
		issue, err := f.store.StartFirstSetup(f.ctx)
		f.must(err)
		for _, token := range []string{"", "not-a-token", strings.Repeat("a", 300), issue.Token + "x"} {
			_, err = f.store.SetupDetails(f.ctx, token)
			f.expect(err, selfhost.ErrSetupTokenInvalid)
			_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: token, Name: "Avery", Password: testPassword, Code: "123456"})
			f.expect(err, selfhost.ErrSetupTokenInvalid)
		}
		f.clock.Advance(selfhost.SetupTokenTTL + time.Second)
		_, err = f.store.SetupDetails(f.ctx, issue.Token)
		f.expect(err, selfhost.ErrSetupTokenInvalid)
	})
	each(t, h, "a new first setup token replaces the old one", func(f *fixture) {
		old, err := f.store.StartFirstSetup(f.ctx)
		f.must(err)
		fresh, err := f.store.StartFirstSetup(f.ctx)
		f.must(err)
		_, err = f.store.SetupDetails(f.ctx, old.Token)
		f.expect(err, selfhost.ErrSetupTokenInvalid)
		_, err = f.store.SetupDetails(f.ctx, fresh.Token)
		f.must(err)
	})
	each(t, h, "setup tokens are stored only as hashes", func(f *fixture) {
		issue, err := f.store.StartFirstSetup(f.ctx)
		f.must(err)
		var count int
		f.must(f.raw().QueryRow(`SELECT COUNT(*) FROM setup_tokens WHERE token_hash = ?`, []byte(issue.Token)).Scan(&count))
		if count != 0 {
			t.Fatal("plaintext setup token is stored")
		}
		f.must(f.raw().QueryRow(`SELECT COUNT(*) FROM setup_tokens WHERE token_hash = ?`, authkit.HashToken(issue.Token)).Scan(&count))
		if count != 1 {
			t.Fatal("hashed setup token is missing")
		}
	})
	each(t, h, "concurrent first setups create one owner", func(f *fixture) {
		issue, err := f.store.StartFirstSetup(f.ctx)
		f.must(err)
		details, err := f.store.SetupDetails(f.ctx, issue.Token)
		f.must(err)
		f.secrets["avery"] = details.TOTPSecret
		code := f.code("Avery")
		var wg sync.WaitGroup
		results := make(chan error, 8)
		for index := 0; index < 8; index++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "Avery", Password: testPassword, Code: code})
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else if !errors.Is(err, selfhost.ErrSetupTokenInvalid) {
				t.Fatalf("unexpected error %v", err)
			}
		}
		if wins != 1 {
			t.Fatalf("%d setups succeeded", wins)
		}
		owners, err := f.store.ListOwners(f.ctx)
		f.must(err)
		if len(owners) != 1 {
			t.Fatalf("%d owners", len(owners))
		}
	})
}

func testLogin(t *testing.T, h Harness) {
	each(t, h, "login succeeds and rejects bad credentials alike", func(f *fixture) {
		f.firstOwner("Avery")
		f.step()
		login, err := f.login("avery", testPassword)
		f.must(err)
		if login.Session.Owner.Name != "Avery" || login.Session.Owner.LastLoginAt == nil {
			t.Fatalf("unexpected session %+v", login.Session)
		}
		f.step()
		_, err = f.login("Avery", "wrong password entirely")
		f.expect(err, selfhost.ErrInvalidCredentials)
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: testPassword, Code: "000000"})
		f.expect(err, selfhost.ErrInvalidCredentials)
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Nobody", Password: testPassword, Code: "000000"})
		f.expect(err, selfhost.ErrInvalidCredentials)
		for _, name := range []string{"", strings.Repeat("a", 500), "Avery\x00"} {
			_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: name, Password: testPassword, Code: "000000"})
			f.expect(err, selfhost.ErrInvalidCredentials)
		}
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: strings.Repeat("p", selfhost.MaxPasswordLength+1), Code: "000000"})
		f.expect(err, selfhost.ErrInvalidCredentials)
	})
	each(t, h, "an accepted code cannot be replayed", func(f *fixture) {
		f.firstOwner("Avery")
		f.step()
		code := f.code("Avery")
		_, err := f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: testPassword, Code: code})
		f.must(err)
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: testPassword, Code: code})
		f.expect(err, selfhost.ErrReplayedCode)
		f.clock.Advance(20 * time.Second)
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: testPassword, Code: code})
		f.expect(err, selfhost.ErrReplayedCode)
		f.step()
		_, err = f.login("Avery", testPassword)
		f.must(err)
	})
	each(t, h, "the setup code cannot be replayed at login", func(f *fixture) {
		login := f.firstOwner("Avery")
		_ = login
		_, err := f.login("Avery", testPassword)
		f.expect(err, selfhost.ErrReplayedCode)
	})
	each(t, h, "a wrong password does not consume the code", func(f *fixture) {
		f.firstOwner("Avery")
		f.step()
		code := f.code("Avery")
		_, err := f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: "wrong password entirely", Code: code})
		f.expect(err, selfhost.ErrInvalidCredentials)
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: testPassword, Code: code})
		f.must(err)
	})
	each(t, h, "concurrent logins with one code open one session", func(f *fixture) {
		f.firstOwner("Avery")
		f.step()
		code := f.code("Avery")
		var wg sync.WaitGroup
		results := make(chan error, 6)
		for index := 0; index < 6; index++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: testPassword, Code: code})
				results <- err
			}()
		}
		wg.Wait()
		close(results)
		wins := 0
		for err := range results {
			if err == nil {
				wins++
			} else if !errors.Is(err, selfhost.ErrReplayedCode) {
				t.Fatalf("unexpected error %v", err)
			}
		}
		if wins != 1 {
			t.Fatalf("%d logins accepted the same code", wins)
		}
	})
	each(t, h, "passwords are stored as argon2id and secrets are encrypted", func(f *fixture) {
		f.firstOwner("Avery")
		var hash string
		var secret []byte
		f.must(f.raw().QueryRow(`SELECT password_hash, totp_secret FROM owners`).Scan(&hash, &secret))
		if !strings.HasPrefix(hash, "$argon2id$") || strings.Contains(hash, testPassword) {
			t.Fatalf("unexpected hash %q", hash)
		}
		if strings.Contains(string(secret), f.secrets["avery"]) || f.secrets["avery"] == "" {
			t.Fatal("totp secret is stored in clear")
		}
	})
	each(t, h, "a different admin key cannot sign anyone in", func(f *fixture) {
		f.firstOwner("Avery")
		f.step()
		code := f.code("Avery")
		f.must(f.store.Close())
		config := f.config()
		config.AdminKey = make([]byte, 32)
		other := f.h.Open(t, config)
		_, err := other.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: testPassword, Code: code})
		if err == nil || errors.Is(err, selfhost.ErrInvalidCredentials) {
			t.Fatalf("expected a decryption failure, got %v", err)
		}
	})
	each(t, h, "inputs that hold sql are treated as data", func(f *fixture) {
		f.firstOwner("Avery")
		f.step()
		_, err := f.store.Login(f.ctx, selfhost.LoginInput{Name: "x' OR '1'='1", Password: testPassword, Code: "000000"})
		f.expect(err, selfhost.ErrInvalidCredentials)
		member := f.member(selfhost.Actor{Kind: selfhost.ActorSystem}, "Robert'); DROP TABLE members;--")
		members, err := f.store.ListMembers(f.ctx)
		f.must(err)
		if len(members) != 1 || members[0].ID != member.ID {
			t.Fatalf("unexpected members %+v", members)
		}
	})
}

func testSession(t *testing.T, h Harness) {
	each(t, h, "sessions expire and log out", func(f *fixture) {
		login := f.firstOwner("Avery")
		session, err := f.store.Session(f.ctx, login.Token)
		f.must(err)
		if session.ID != login.Session.ID || session.CSRFToken != login.Session.CSRFToken || session.Owner.ID != login.Session.Owner.ID {
			t.Fatalf("session mismatch %+v %+v", session, login.Session)
		}
		if !session.ExpiresAt.Equal(f.clock.Now().Add(selfhost.DefaultSessionTTL)) {
			t.Fatalf("unexpected expiry %v", session.ExpiresAt)
		}
		for _, token := range []string{"", "nope", login.Token + "x", strings.Repeat("a", 300)} {
			_, err = f.store.Session(f.ctx, token)
			f.expect(err, selfhost.ErrSessionInvalid)
		}
		f.clock.Advance(selfhost.DefaultSessionTTL - time.Second)
		_, err = f.store.Session(f.ctx, login.Token)
		f.must(err)
		f.clock.Advance(time.Second)
		_, err = f.store.Session(f.ctx, login.Token)
		f.expect(err, selfhost.ErrSessionInvalid)
	})
	each(t, h, "logout ends only that session", func(f *fixture) {
		first := f.firstOwner("Avery")
		f.step()
		second, err := f.login("Avery", testPassword)
		f.must(err)
		f.must(f.store.Logout(f.ctx, first.Token))
		_, err = f.store.Session(f.ctx, first.Token)
		f.expect(err, selfhost.ErrSessionInvalid)
		_, err = f.store.Session(f.ctx, second.Token)
		f.must(err)
		f.must(f.store.Logout(f.ctx, "unknown"))
	})
	each(t, h, "session tokens are stored only as hashes", func(f *fixture) {
		login := f.firstOwner("Avery")
		var count int
		f.must(f.raw().QueryRow(`SELECT COUNT(*) FROM sessions WHERE token_hash = ?`, []byte(login.Token)).Scan(&count))
		if count != 0 {
			t.Fatal("plaintext session token is stored")
		}
	})
	each(t, h, "sessions survive a restart", func(f *fixture) {
		login := f.firstOwner("Avery")
		f.reopen()
		_, err := f.store.Session(f.ctx, login.Token)
		f.must(err)
	})
}

func testStepUp(t *testing.T, h Harness) {
	each(t, h, "step up refreshes the recent sign in window", func(f *fixture) {
		login := f.firstOwner("Avery")
		if !login.Session.RecentAuth(f.clock.Now()) {
			t.Fatal("a fresh sign in is recent")
		}
		f.clock.Advance(selfhost.RecentAuthWindow)
		session, err := f.store.Session(f.ctx, login.Token)
		f.must(err)
		if session.RecentAuth(f.clock.Now()) {
			t.Fatal("recent window must close after ten minutes")
		}
		if !session.RecentAuthUntil().Equal(login.Session.RecentAuthAt.Add(selfhost.RecentAuthWindow)) {
			t.Fatal("recent window end is wrong")
		}
		stepped, err := f.store.StepUp(f.ctx, selfhost.StepUpInput{SessionToken: login.Token, Password: testPassword, Code: f.code("Avery")})
		f.must(err)
		if !stepped.RecentAuth(f.clock.Now()) {
			t.Fatal("step up must refresh the window")
		}
		again, err := f.store.Session(f.ctx, login.Token)
		f.must(err)
		if !again.RecentAuthAt.Equal(stepped.RecentAuthAt) {
			t.Fatal("step up was not stored")
		}
	})
	each(t, h, "step up needs the password, a fresh code and a live session", func(f *fixture) {
		login := f.firstOwner("Avery")
		f.step()
		_, err := f.store.StepUp(f.ctx, selfhost.StepUpInput{SessionToken: login.Token, Password: "wrong password entirely", Code: f.code("Avery")})
		f.expect(err, selfhost.ErrInvalidCredentials)
		_, err = f.store.StepUp(f.ctx, selfhost.StepUpInput{SessionToken: login.Token, Password: testPassword, Code: "000000"})
		f.expect(err, selfhost.ErrInvalidCredentials)
		_, err = f.store.StepUp(f.ctx, selfhost.StepUpInput{SessionToken: "unknown", Password: testPassword, Code: f.code("Avery")})
		f.expect(err, selfhost.ErrSessionInvalid)
		code := f.code("Avery")
		_, err = f.store.StepUp(f.ctx, selfhost.StepUpInput{SessionToken: login.Token, Password: testPassword, Code: code})
		f.must(err)
		_, err = f.store.StepUp(f.ctx, selfhost.StepUpInput{SessionToken: login.Token, Password: testPassword, Code: code})
		f.expect(err, selfhost.ErrReplayedCode)
		f.step()
		f.must(f.store.Logout(f.ctx, login.Token))
		_, err = f.store.StepUp(f.ctx, selfhost.StepUpInput{SessionToken: login.Token, Password: testPassword, Code: f.code("Avery")})
		f.expect(err, selfhost.ErrSessionInvalid)
	})
	each(t, h, "a code used at sign in cannot be reused for step up", func(f *fixture) {
		f.firstOwner("Avery")
		f.step()
		code := f.code("Avery")
		login, err := f.store.Login(f.ctx, selfhost.LoginInput{Name: "Avery", Password: testPassword, Code: code})
		f.must(err)
		_, err = f.store.StepUp(f.ctx, selfhost.StepUpInput{SessionToken: login.Token, Password: testPassword, Code: code})
		f.expect(err, selfhost.ErrReplayedCode)
	})
}

func testOwners(t *testing.T, h Harness) {
	each(t, h, "an invited owner completes setup with the reserved name", func(f *fixture) {
		first := f.firstOwner("Avery")
		issue, err := f.store.InviteOwner(f.ctx, actorOf(first), "Blake")
		f.must(err)
		if !issue.Owner.SetupPending || issue.Owner.Name != "Blake" {
			t.Fatalf("unexpected owner %+v", issue.Owner)
		}
		_, err = f.store.InviteOwner(f.ctx, actorOf(first), "blake")
		f.expect(err, selfhost.ErrConflict)
		_, err = f.store.InviteOwner(f.ctx, actorOf(first), "avery")
		f.expect(err, selfhost.ErrConflict)
		_, err = f.store.InviteOwner(f.ctx, actorOf(first), " ")
		f.expect(err, selfhost.ErrInvalidInput)
		details, err := f.store.SetupDetails(f.ctx, issue.Token)
		f.must(err)
		if details.FirstOwner || details.OwnerName != "Blake" {
			t.Fatalf("unexpected details %+v", details)
		}
		f.secrets["blake"] = details.TOTPSecret
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Blake", Password: otherTestPassword, Code: f.code("Blake")})
		f.expect(err, selfhost.ErrInvalidCredentials)
		_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Name: "Someone Else", Password: otherTestPassword, Code: f.code("Blake")})
		f.expect(err, selfhost.ErrInvalidInput)
		login, err := f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Password: otherTestPassword, Code: f.code("Blake")})
		f.must(err)
		if login.Session.Owner.Name != "Blake" || login.Session.Owner.ID != issue.Owner.ID {
			t.Fatalf("unexpected login %+v", login)
		}
		owners, err := f.store.ListOwners(f.ctx)
		f.must(err)
		if len(owners) != 2 {
			t.Fatalf("%d owners", len(owners))
		}
		_, err = f.store.StartFirstSetup(f.ctx)
		f.expect(err, selfhost.ErrConflict)
	})
	each(t, h, "the last active owner cannot be removed", func(f *fixture) {
		first := f.firstOwner("Avery")
		err := f.store.RemoveOwner(f.ctx, actorOf(first), first.Session.Owner.ID)
		f.expect(err, selfhost.ErrLastOwner)
		pending, err := f.store.InviteOwner(f.ctx, actorOf(first), "Blake")
		f.must(err)
		err = f.store.RemoveOwner(f.ctx, actorOf(first), first.Session.Owner.ID)
		f.expect(err, selfhost.ErrLastOwner)
		f.must(f.store.RemoveOwner(f.ctx, actorOf(first), pending.Owner.ID))
		err = f.store.RemoveOwner(f.ctx, actorOf(first), pending.Owner.ID)
		f.expect(err, selfhost.ErrNotFound)
		_, err = f.store.SetupDetails(f.ctx, pending.Token)
		f.expect(err, selfhost.ErrSetupTokenInvalid)
		err = f.store.RemoveOwner(f.ctx, actorOf(first), "missing")
		f.expect(err, selfhost.ErrNotFound)
	})
	each(t, h, "removing an owner ends sessions and revokes devices", func(f *fixture) {
		first := f.firstOwner("Avery")
		f.step()
		second := f.invitedOwner(actorOf(first), "Blake")
		device := f.pair(actorOf(first), ownerHolder(second), "Blake laptop")
		f.must(f.store.RemoveOwner(f.ctx, actorOf(first), second.Session.Owner.ID))
		_, err := f.store.Session(f.ctx, second.Token)
		f.expect(err, selfhost.ErrSessionInvalid)
		_, err = f.store.AuthenticateDevice(f.ctx, device.Token)
		f.expect(err, selfhost.ErrDeviceInvalid)
		f.step()
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Blake", Password: otherTestPassword, Code: f.code("Blake")})
		f.expect(err, selfhost.ErrInvalidCredentials)
		_, err = f.store.InviteOwner(f.ctx, actorOf(first), "Blake")
		f.must(err)
	})
	each(t, h, "two owners cannot remove each other at once", func(f *fixture) {
		first := f.firstOwner("Avery")
		f.step()
		second := f.invitedOwner(actorOf(first), "Blake")
		var wg sync.WaitGroup
		results := make(chan error, 2)
		for _, target := range []string{first.Session.Owner.ID, second.Session.Owner.ID} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results <- f.store.RemoveOwner(f.ctx, actorOf(first), target)
			}()
		}
		wg.Wait()
		close(results)
		removed, refused := 0, 0
		for err := range results {
			switch {
			case err == nil:
				removed++
			case errors.Is(err, selfhost.ErrLastOwner):
				refused++
			default:
				t.Fatalf("unexpected error %v", err)
			}
		}
		if removed != 1 || refused != 1 {
			t.Fatalf("removed %d refused %d", removed, refused)
		}
		required, err := f.store.SetupRequired(f.ctx)
		f.must(err)
		if required {
			t.Fatal("one owner must remain")
		}
	})
	each(t, h, "reset ends sessions, clears credentials and needs a new setup", func(f *fixture) {
		first := f.firstOwner("Avery")
		f.step()
		second := f.invitedOwner(actorOf(first), "Blake")
		f.step()
		issue, err := f.store.ResetOwner(f.ctx, selfhost.SystemActor, "blake")
		f.must(err)
		if !issue.Owner.SetupPending {
			t.Fatal("a reset owner must be pending")
		}
		_, err = f.store.Session(f.ctx, second.Token)
		f.expect(err, selfhost.ErrSessionInvalid)
		_, err = f.store.Login(f.ctx, selfhost.LoginInput{Name: "Blake", Password: otherTestPassword, Code: f.code("Blake")})
		f.expect(err, selfhost.ErrInvalidCredentials)
		details, err := f.store.SetupDetails(f.ctx, issue.Token)
		f.must(err)
		if details.TOTPSecret == f.secrets["blake"] {
			t.Fatal("reset must issue a new totp secret")
		}
		f.secrets["blake"] = details.TOTPSecret
		login, err := f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Password: testPassword, Code: f.code("Blake")})
		f.must(err)
		if login.Session.Owner.ID != second.Session.Owner.ID {
			t.Fatal("reset must keep the owner identity")
		}
		_, err = f.store.ResetOwner(f.ctx, selfhost.SystemActor, "nobody")
		f.expect(err, selfhost.ErrNotFound)
	})
	each(t, h, "reset revokes the owner's devices and cancels their pairings", func(f *fixture) {
		first := f.firstOwner("Avery")
		f.step()
		second := f.invitedOwner(actorOf(first), "Blake")
		device := f.pair(actorOf(first), ownerHolder(second), "Blake laptop")
		pending, err := f.store.CreatePairing(f.ctx, actorOf(first), selfhost.PairingInput{Holder: ownerHolder(second)})
		f.must(err)
		other := f.pair(actorOf(first), ownerHolder(first), "Avery laptop")
		_, err = f.store.ResetOwner(f.ctx, selfhost.SystemActor, "Blake")
		f.must(err)
		_, err = f.store.AuthenticateDevice(f.ctx, device.Token)
		f.expect(err, selfhost.ErrDeviceInvalid)
		_, err = f.store.RedeemPairing(f.ctx, selfhost.RedeemInput{Code: pending.Code, DeviceName: "late"})
		f.expect(err, selfhost.ErrPairingInvalid)
		_, err = f.store.AuthenticateDevice(f.ctx, other.Token)
		f.must(err)
		page, err := f.store.ListAudit(f.ctx, 0, 1)
		f.must(err)
		if len(page.Entries) != 1 || page.Entries[0].Action != "owner.reset" || page.Entries[0].Detail["revokedDevices"] != "1" {
			t.Fatalf("reset audit entry %+v", page.Entries)
		}
	})
	each(t, h, "resetting the only owner returns the instance to setup", func(f *fixture) {
		first := f.firstOwner("Avery")
		issue, err := f.store.ResetOwner(f.ctx, selfhost.SystemActor, "Avery")
		f.must(err)
		_, err = f.store.Session(f.ctx, first.Token)
		f.expect(err, selfhost.ErrSessionInvalid)
		required, err := f.store.SetupRequired(f.ctx)
		f.must(err)
		if !required {
			t.Fatal("setup must be required again")
		}
		details, err := f.store.SetupDetails(f.ctx, issue.Token)
		f.must(err)
		f.secrets["avery"] = details.TOTPSecret
		f.step()
		_, err = f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Password: otherTestPassword, Code: f.code("Avery")})
		f.must(err)
		f.step()
		_, err = f.login("Avery", otherTestPassword)
		f.must(err)
		_, err = f.login("Avery", testPassword)
		f.expect(err, selfhost.ErrInvalidCredentials)
	})
}
