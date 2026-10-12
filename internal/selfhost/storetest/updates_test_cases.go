package storetest

import (
	"strings"
	"testing"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

func boolPointer(value bool) *bool       { return &value }
func stringPointer(value string) *string { return &value }

func (f *fixture) updateSettings() selfhost.UpdateSettings {
	f.t.Helper()
	settings, err := f.store.UpdateSettings(f.ctx)
	f.must(err)
	return settings
}

func (f *fixture) auditActions() []selfhost.AuditEntry {
	f.t.Helper()
	page, err := f.store.ListAudit(f.ctx, 0, 200)
	f.must(err)
	return page.Entries
}

func countEntries(entries []selfhost.AuditEntry, action string) int {
	count := 0
	for _, entry := range entries {
		if entry.Action == action {
			count++
		}
	}
	return count
}

func testUpdates(t *testing.T, h Harness) {
	each(t, h, "a new database has checks unset on the stable channel", func(f *fixture) {
		settings := f.updateSettings()
		if settings.Choice != selfhost.UpdatesUnset || settings.Channel != "stable" || settings.Sequences["k"] != 0 || len(settings.Feed) != 0 || settings.CheckedAt != nil || settings.LastError != "" {
			t.Fatalf("settings = %+v", settings)
		}
	})
	each(t, h, "settings change with an audit entry and no-ops leave none", func(f *fixture) {
		login := f.firstOwner("Avery")
		by := actorOf(login)
		before := len(f.auditActions())
		settings, err := f.store.ConfigureUpdates(f.ctx, by, selfhost.UpdateChange{Enabled: boolPointer(true)})
		f.must(err)
		if settings.Choice != selfhost.UpdatesOn || settings.Channel != "stable" {
			t.Fatalf("settings = %+v", settings)
		}
		entries := f.auditActions()
		if len(entries) != before+1 || entries[0].Action != "updates.updated" || entries[0].Actor != by.String() || entries[0].Detail["enabled"] != "on" || len(entries[0].Detail) != 1 {
			t.Fatalf("audit entry = %+v", entries[0])
		}
		_, err = f.store.ConfigureUpdates(f.ctx, by, selfhost.UpdateChange{Enabled: boolPointer(true), Channel: stringPointer("stable")})
		f.must(err)
		if len(f.auditActions()) != before+1 {
			t.Fatal("a no-op change was audited")
		}
		settings, err = f.store.ConfigureUpdates(f.ctx, by, selfhost.UpdateChange{Enabled: boolPointer(false), Channel: stringPointer("beta")})
		f.must(err)
		if settings.Choice != selfhost.UpdatesOff || settings.Channel != "beta" {
			t.Fatalf("settings = %+v", settings)
		}
		last := f.auditActions()[0]
		if last.Detail["enabled"] != "off" || last.Detail["channel"] != "beta" {
			t.Fatalf("audit detail = %v", last.Detail)
		}
		report, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !report.OK {
			t.Fatalf("audit chain = %+v", report)
		}
	})
	each(t, h, "a bad channel is refused and changes nothing", func(f *fixture) {
		login := f.firstOwner("Avery")
		for _, channel := range []string{"", "Beta", "bad channel", "1stable", "-x", strings.Repeat("a", 33), "a/b"} {
			_, err := f.store.ConfigureUpdates(f.ctx, actorOf(login), selfhost.UpdateChange{Enabled: boolPointer(true), Channel: stringPointer(channel)})
			f.expect(err, selfhost.ErrInvalidInput)
		}
		if settings := f.updateSettings(); settings.Choice != selfhost.UpdatesUnset || settings.Channel != "stable" {
			t.Fatalf("a refused change was applied: %+v", settings)
		}
	})
	each(t, h, "the feed sequence only moves forward", func(f *fixture) {
		at := f.clock.Now()
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: []byte(`{"n":5}`), Sequence: 5, CheckedAt: at}))
		settings := f.updateSettings()
		if settings.Sequences["k"] != 5 || string(settings.Feed) != `{"n":5}` || settings.CheckedAt == nil || !settings.CheckedAt.Equal(at) {
			t.Fatalf("settings = %+v", settings)
		}
		f.clock.Advance(time.Hour)
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: []byte(`{"n":"5b"}`), Sequence: 5, CheckedAt: f.clock.Now()}))
		if string(f.updateSettings().Feed) != `{"n":"5b"}` {
			t.Fatal("the same sequence did not replace the stored feed")
		}
		f.expect(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: []byte(`{"n":4}`), Sequence: 4, CheckedAt: f.clock.Now()}), selfhost.ErrConflict)
		settings = f.updateSettings()
		if settings.Sequences["k"] != 5 || string(settings.Feed) != `{"n":"5b"}` {
			t.Fatalf("a lower sequence changed the stored feed: %+v", settings)
		}
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: []byte(`{"n":6}`), Sequence: 6, CheckedAt: f.clock.Now()}))
		f.expect(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: []byte(`{}`), Sequence: 0, CheckedAt: f.clock.Now()}), selfhost.ErrInvalidInput)
		f.expect(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: nil, Sequence: 9, CheckedAt: f.clock.Now()}), selfhost.ErrInvalidInput)
		if f.updateSettings().Sequences["k"] != 6 {
			t.Fatal("an invalid record changed the sequence")
		}
	})
	each(t, h, "sequences are kept per key id", func(f *fixture) {
		at := f.clock.Now()
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "old", Raw: []byte(`{}`), Sequence: 900, CheckedAt: at}))
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "new", Raw: []byte(`{}`), Sequence: 3, CheckedAt: at}))
		f.expect(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "old", Raw: []byte(`{}`), Sequence: 899, CheckedAt: at}), selfhost.ErrConflict)
		f.expect(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "new", Raw: []byte(`{}`), Sequence: 2, CheckedAt: at}), selfhost.ErrConflict)
		f.expect(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "", Raw: []byte(`{}`), Sequence: 2, CheckedAt: at}), selfhost.ErrInvalidInput)
		sequences := f.updateSettings().Sequences
		if len(sequences) != 2 || sequences["old"] != 900 || sequences["new"] != 3 {
			t.Fatalf("sequences = %v", sequences)
		}
	})
	each(t, h, "resetting sequences forgets every key, clears a rollback error and is audited", func(f *fixture) {
		login := f.firstOwner("Avery")
		at := f.clock.Now()
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "a", Raw: []byte(`{"n":1}`), Sequence: 70, CheckedAt: at}))
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "b", Raw: []byte(`{"n":1}`), Sequence: 80, CheckedAt: at}))
		f.must(f.store.RecordUpdateFailure(f.ctx, selfhost.UpdateErrorRollback, at))
		cleared, err := f.store.ResetUpdateSequences(f.ctx, actorOf(login))
		f.must(err)
		settings := f.updateSettings()
		if cleared != 2 || len(settings.Sequences) != 0 || settings.LastError != "" || string(settings.Feed) != `{"n":1}` {
			t.Fatalf("cleared %d, settings %+v", cleared, settings)
		}
		entry := f.auditActions()[0]
		if entry.Action != "updates.sequences_reset" || entry.Actor != actorOf(login).String() || entry.Detail["keys"] != "2" {
			t.Fatalf("audit entry = %+v", entry)
		}
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "a", Raw: []byte(`{"n":2}`), Sequence: 1, CheckedAt: at}))
		cleared, err = f.store.ResetUpdateSequences(f.ctx, selfhost.SystemActor)
		f.must(err)
		if cleared != 1 {
			t.Fatalf("second reset cleared %d", cleared)
		}
		report, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !report.OK {
			t.Fatalf("audit chain = %+v", report)
		}
	})
	each(t, h, "failures keep the last good feed and rollbacks are audited once", func(f *fixture) {
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: []byte(`{"n":5}`), Sequence: 5, CheckedAt: f.clock.Now()}))
		f.must(f.store.RecordUpdateFailure(f.ctx, selfhost.UpdateErrorUnreachable, f.clock.Now()))
		settings := f.updateSettings()
		if settings.LastError != "feed_unreachable" || string(settings.Feed) != `{"n":5}` || settings.Sequences["k"] != 5 || settings.CheckedAt == nil {
			t.Fatalf("settings = %+v", settings)
		}
		before := countEntries(f.auditActions(), "updates.rollback_rejected")
		for round := 0; round < 3; round++ {
			f.must(f.store.RecordUpdateFailure(f.ctx, selfhost.UpdateErrorRollback, f.clock.Now()))
		}
		entries := f.auditActions()
		if countEntries(entries, "updates.rollback_rejected") != before+1 {
			t.Fatalf("rollback audited %d times", countEntries(entries, "updates.rollback_rejected")-before)
		}
		if entries[0].Action != "updates.rollback_rejected" || entries[0].Actor != selfhost.SystemActor.String() || entries[0].Detail["highestSequence"] != "5" {
			t.Fatalf("audit entry = %+v", entries[0])
		}
		f.must(f.store.RecordUpdateFailure(f.ctx, selfhost.UpdateErrorInvalid, f.clock.Now()))
		f.must(f.store.RecordUpdateFailure(f.ctx, selfhost.UpdateErrorRollback, f.clock.Now()))
		if countEntries(f.auditActions(), "updates.rollback_rejected") != before+2 {
			t.Fatal("a rollback after another error was not audited")
		}
		for _, code := range []string{"", "not_configured", "boom", strings.Repeat("x", 40)} {
			f.expect(f.store.RecordUpdateFailure(f.ctx, code, f.clock.Now()), selfhost.ErrInvalidInput)
		}
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: []byte(`{"n":7}`), Sequence: 7, CheckedAt: f.clock.Now()}))
		if f.updateSettings().LastError != "" {
			t.Fatal("a good feed did not clear the error")
		}
		report, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !report.OK {
			t.Fatalf("audit chain = %+v", report)
		}
	})
	each(t, h, "update settings survive a restart", func(f *fixture) {
		login := f.firstOwner("Avery")
		_, err := f.store.ConfigureUpdates(f.ctx, actorOf(login), selfhost.UpdateChange{Enabled: boolPointer(true), Channel: stringPointer("beta")})
		f.must(err)
		f.must(f.store.RecordUpdateFeed(f.ctx, selfhost.UpdateFeed{KeyID: "k", Raw: []byte(`{"n":3}`), Sequence: 3, CheckedAt: f.clock.Now()}))
		f.must(f.store.RecordUpdateFailure(f.ctx, selfhost.UpdateErrorExpired, f.clock.Now()))
		f.reopen()
		settings := f.updateSettings()
		if settings.Choice != selfhost.UpdatesOn || settings.Channel != "beta" || settings.Sequences["k"] != 3 || string(settings.Feed) != `{"n":3}` || settings.LastError != "feed_expired" {
			t.Fatalf("settings = %+v", settings)
		}
	})
	each(t, h, "first setup records the owner's choice and invited setups cannot", func(f *fixture) {
		login := f.firstOwnerChoosing("Avery", boolPointer(true))
		if f.updateSettings().Choice != selfhost.UpdatesOn {
			t.Fatalf("settings = %+v", f.updateSettings())
		}
		setup := f.auditActions()[0]
		if setup.Action != "updates.updated" || setup.Detail["enabled"] != "on" || setup.Detail["source"] != "setup" || setup.Actor != actorOf(login).String() {
			t.Fatalf("audit entry = %+v", setup)
		}
		invited := f.invitedOwnerChoosing(actorOf(login), "Blake", boolPointer(false))
		if invited.Session.Owner.Name != "Blake" || f.updateSettings().Choice != selfhost.UpdatesOn {
			t.Fatalf("an invited owner changed the update choice: %+v", f.updateSettings())
		}
	})
	each(t, h, "first setup can turn checks off", func(f *fixture) {
		f.firstOwnerChoosing("Avery", boolPointer(false))
		if f.updateSettings().Choice != selfhost.UpdatesOff {
			t.Fatalf("settings = %+v", f.updateSettings())
		}
	})
	each(t, h, "first setup without a choice leaves checks unset", func(f *fixture) {
		f.firstOwnerChoosing("Avery", nil)
		if f.updateSettings().Choice != selfhost.UpdatesUnset || countEntries(f.auditActions(), "updates.updated") != 0 {
			t.Fatalf("settings = %+v", f.updateSettings())
		}
	})
}

func (f *fixture) invitedOwnerChoosing(by selfhost.Actor, name string, updateChecks *bool) selfhost.OwnerLogin {
	f.t.Helper()
	issue, err := f.store.InviteOwner(f.ctx, by, name)
	f.must(err)
	details, err := f.store.SetupDetails(f.ctx, issue.Token)
	f.must(err)
	f.secrets[strings.ToLower(name)] = details.TOTPSecret
	login, err := f.store.CompleteSetup(f.ctx, selfhost.CompleteSetupInput{Token: issue.Token, Password: otherTestPassword, Code: f.code(name), UpdateChecks: updateChecks})
	f.must(err)
	return login
}
