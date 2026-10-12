package storetest

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"crypto/sha256"

	"usesesame.app/backend/internal/selfhost"
)

func testAudit(t *testing.T, h Harness) {
	each(t, h, "mutations append to a verifiable chain", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		device := f.pair(by, memberHolder(member), "Laptop")
		f.must(f.store.RevokeDevice(f.ctx, by, device.Device.ID))
		_, err := f.store.SetFlag(f.ctx, by, "sync", true)
		f.must(err)
		page, err := f.store.ListAudit(f.ctx, 0, 100)
		f.must(err)
		var actions []string
		for index := len(page.Entries) - 1; index >= 0; index-- {
			actions = append(actions, page.Entries[index].Action)
		}
		want := []string{"setup.started", "owner.setup_completed", "member.created", "pairing.created", "device.paired", "device.revoked", "flag.updated"}
		if strings.Join(actions, ",") != strings.Join(want, ",") {
			t.Fatalf("actions %v", actions)
		}
		report, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !report.OK || report.Rows != int64(len(want)) || report.HeadSeq != int64(len(want)) || report.HeadHash != page.Entries[0].Hash {
			t.Fatalf("unexpected report %+v", report)
		}
		for _, entry := range page.Entries {
			if strings.Contains(strings.Join(mapValues(entry.Detail), " "), device.Token) {
				t.Fatal("device token leaked into the audit log")
			}
		}
	})
	each(t, h, "the stored hash follows the documented form", func(f *fixture) {
		owner := f.firstOwner("Avery")
		f.member(actorOf(owner), "Sam")
		rows, err := f.raw().Query(`SELECT seq, actor, action, target, detail, at, prev_hash, hash FROM audit_log ORDER BY seq`)
		f.must(err)
		defer rows.Close()
		previous := make([]byte, 32)
		count := 0
		for rows.Next() {
			var seq, at int64
			var actor, action, target, detail string
			var prev, hash []byte
			f.must(rows.Scan(&seq, &actor, &action, &target, &detail, &at, &prev, &hash))
			var parsed map[string]string
			f.must(json.Unmarshal([]byte(detail), &parsed))
			canonical, err := json.Marshal(map[string]any{
				"actor": actor, "action": action, "target": target, "detail": parsed, "at": time.Unix(at, 0).UTC().Format(time.RFC3339),
			})
			f.must(err)
			digest := sha256.New()
			digest.Write([]byte("sesame-selfhost-audit-v1\n"))
			digest.Write(previous)
			digest.Write([]byte("\n"))
			digest.Write(canonical)
			if hex.EncodeToString(digest.Sum(nil)) != hex.EncodeToString(hash) || hex.EncodeToString(prev) != hex.EncodeToString(previous) {
				t.Fatalf("row %d does not follow the documented chain form", seq)
			}
			previous = hash
			count++
		}
		f.must(rows.Err())
		if count < 3 {
			t.Fatalf("only %d rows", count)
		}
	})
	each(t, h, "append only triggers reject edits and deletes", func(f *fixture) {
		f.firstOwner("Avery")
		db := f.raw()
		_, err := db.Exec(`UPDATE audit_log SET action = 'forged' WHERE seq = 1`)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("update was not rejected: %v", err)
		}
		_, err = db.Exec(`DELETE FROM audit_log WHERE seq = 1`)
		if err == nil || !strings.Contains(err.Error(), "append-only") {
			t.Fatalf("delete was not rejected: %v", err)
		}
		report, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !report.OK {
			t.Fatalf("rejected edits changed the chain: %+v", report)
		}
	})
	tamper := func(name string, statements []string, breakSeq int64) {
		each(t, h, "tampering is detected: "+name, func(f *fixture) {
			owner := f.firstOwner("Avery")
			by := actorOf(owner)
			for _, member := range []string{"Sam", "Robin", "Jo"} {
				f.member(by, member)
			}
			before, err := f.store.VerifyAudit(f.ctx)
			f.must(err)
			if !before.OK || before.Rows < 5 {
				t.Fatalf("unexpected baseline %+v", before)
			}
			db := f.raw()
			for _, statement := range statements {
				if _, err := db.Exec(statement); err != nil {
					t.Fatalf("%s: %v", statement, err)
				}
			}
			after, err := f.store.VerifyAudit(f.ctx)
			f.must(err)
			if after.OK || after.FirstBreak == nil || after.FirstBreak.Seq != breakSeq || after.FirstBreak.Reason == "" {
				t.Fatalf("tampering not detected at %d: %+v", breakSeq, after)
			}
			check, err := f.store.Check(f.ctx)
			f.must(err)
			if check.OK() || check.Audit.OK {
				t.Fatalf("check must fail after tampering: %+v", check)
			}
		})
	}
	tamper("edited content", []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET target = 'forged' WHERE seq = 3`}, 3)
	tamper("edited actor", []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET actor = 'owner:other' WHERE seq = 4`}, 4)
	tamper("edited time", []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET at = at + 1 WHERE seq = 2`}, 2)
	tamper("edited detail", []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET detail = '{"name":"forged"}' WHERE seq = 3`}, 3)
	tamper("malformed detail", []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET detail = 'not json' WHERE seq = 3`}, 3)
	tamper("deleted row", []string{`DROP TRIGGER audit_log_no_delete`, `DELETE FROM audit_log WHERE seq = 3`}, 4)
	tamper("deleted first row", []string{`DROP TRIGGER audit_log_no_delete`, `DELETE FROM audit_log WHERE seq = 1`}, 2)
	tamper("swapped hash", []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET hash = X'00' WHERE seq = 3`}, 3)
	tamper("row rehashed by the attacker", []string{
		`DROP TRIGGER audit_log_no_update`,
		`UPDATE audit_log SET prev_hash = (SELECT hash FROM audit_log WHERE seq = 1) WHERE seq = 3`,
	}, 3)
	tamper("reordered sequence", []string{
		`DROP TRIGGER audit_log_no_update`,
		`UPDATE audit_log SET seq = 100 WHERE seq = 2`,
	}, 3)
	each(t, h, "the incremental check agrees with the full check as the log grows", func(f *fixture) {
		empty, err := f.store.VerifyAuditIncremental(f.ctx)
		f.must(err)
		if !empty.OK || empty.Rows != 0 || empty.HeadSeq != 0 {
			t.Fatalf("empty log %+v", empty)
		}
		by := actorOf(f.firstOwner("Avery"))
		for round := 0; round < 3; round++ {
			f.member(by, "Member "+string(rune('A'+round)))
			incremental, err := f.store.VerifyAuditIncremental(f.ctx)
			f.must(err)
			full, err := f.store.VerifyAudit(f.ctx)
			f.must(err)
			if !incremental.OK || incremental != full {
				t.Fatalf("round %d incremental %+v, full %+v", round, incremental, full)
			}
		}
		again, err := f.store.VerifyAuditIncremental(f.ctx)
		f.must(err)
		page, err := f.store.ListAudit(f.ctx, 0, 1)
		f.must(err)
		if !again.OK || again.HeadHash != page.Entries[0].Hash || again.HeadSeq != page.Entries[0].Seq {
			t.Fatalf("no new rows %+v, newest %+v", again, page.Entries[0])
		}
	})
	each(t, h, "a tampered new row is found by the incremental check", func(f *fixture) {
		by := actorOf(f.firstOwner("Avery"))
		f.member(by, "Sam")
		baseline, err := f.store.VerifyAuditIncremental(f.ctx)
		f.must(err)
		if !baseline.OK {
			t.Fatalf("baseline %+v", baseline)
		}
		f.member(by, "Robin")
		f.member(by, "Jo")
		db := f.raw()
		for _, statement := range []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET target = 'forged' WHERE seq = ` + strconv.FormatInt(baseline.HeadSeq+2, 10)} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		report, err := f.store.VerifyAuditIncremental(f.ctx)
		f.must(err)
		if report.OK || report.FirstBreak == nil || report.FirstBreak.Seq != baseline.HeadSeq+2 {
			t.Fatalf("new row tampering not detected: %+v", report)
		}
	})
	each(t, h, "the incremental check breaks when the verified head row changes or disappears", func(f *fixture) {
		by := actorOf(f.firstOwner("Avery"))
		f.member(by, "Sam")
		baseline, err := f.store.VerifyAuditIncremental(f.ctx)
		f.must(err)
		db := f.raw()
		if _, err := db.Exec(`DROP TRIGGER audit_log_no_delete`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`DELETE FROM audit_log WHERE seq = ?`, baseline.HeadSeq); err != nil {
			t.Fatal(err)
		}
		report, err := f.store.VerifyAuditIncremental(f.ctx)
		f.must(err)
		if report.OK || report.FirstBreak == nil || report.FirstBreak.Seq != baseline.HeadSeq {
			t.Fatalf("missing head row not detected: %+v", report)
		}
	})
	each(t, h, "an old row edit is found by the full check and then stays reported", func(f *fixture) {
		by := actorOf(f.firstOwner("Avery"))
		f.member(by, "Sam")
		f.member(by, "Robin")
		baseline, err := f.store.VerifyAuditIncremental(f.ctx)
		f.must(err)
		if !baseline.OK || baseline.Rows < 4 {
			t.Fatalf("baseline %+v", baseline)
		}
		db := f.raw()
		for _, statement := range []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET target = 'forged' WHERE seq = 2`} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		f.member(by, "Jo")
		full, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if full.OK || full.FirstBreak == nil || full.FirstBreak.Seq != 2 {
			t.Fatalf("full check missed the old row edit: %+v", full)
		}
		after, err := f.store.VerifyAuditIncremental(f.ctx)
		f.must(err)
		if after.OK || after.FirstBreak == nil || after.FirstBreak.Seq != 2 {
			t.Fatalf("incremental check forgot the break: %+v", after)
		}
	})
	each(t, h, "a full check after the incremental one catches an old row edit", func(f *fixture) {
		by := actorOf(f.firstOwner("Avery"))
		f.member(by, "Sam")
		f.member(by, "Robin")
		if report, err := f.store.VerifyAuditIncremental(f.ctx); err != nil || !report.OK {
			t.Fatalf("baseline %+v %v", report, err)
		}
		db := f.raw()
		for _, statement := range []string{`DROP TRIGGER audit_log_no_update`, `UPDATE audit_log SET actor = 'owner:other' WHERE seq = 3`} {
			if _, err := db.Exec(statement); err != nil {
				t.Fatal(err)
			}
		}
		full, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if full.OK || full.FirstBreak == nil || full.FirstBreak.Seq != 3 {
			t.Fatalf("full check missed the old row edit: %+v", full)
		}
	})
	each(t, h, "pages are ordered and cover every entry once", func(f *fixture) {
		f.firstOwner("Avery")
		for index := 0; index < 7; index++ {
			_, err := f.store.AppendAudit(f.ctx, selfhost.AuditInput{Actor: selfhost.SystemActor, Action: "backup.created", Target: "file", Detail: map[string]string{"n": string(rune('a' + index))}})
			f.must(err)
		}
		seen := map[int64]bool{}
		cursor := int64(0)
		last := int64(1 << 60)
		pages := 0
		for {
			page, err := f.store.ListAudit(f.ctx, cursor, 3)
			f.must(err)
			pages++
			for _, entry := range page.Entries {
				if seen[entry.Seq] || entry.Seq >= last {
					t.Fatalf("entry %d repeated or out of order", entry.Seq)
				}
				seen[entry.Seq] = true
				last = entry.Seq
			}
			if page.NextCursor == 0 {
				break
			}
			cursor = page.NextCursor
		}
		if len(seen) != 9 || pages != 3 {
			t.Fatalf("saw %d entries in %d pages", len(seen), pages)
		}
	})
	each(t, h, "invalid audit input is refused", func(f *fixture) {
		big := map[string]string{}
		for index := 0; index <= selfhost.MaxAuditDetailEntries; index++ {
			big[string(rune('a'+index))] = "v"
		}
		for name, input := range map[string]selfhost.AuditInput{
			"no action":    {Actor: selfhost.SystemActor},
			"bad actor":    {Actor: selfhost.Actor{Kind: "robot"}, Action: "x"},
			"long action":  {Actor: selfhost.SystemActor, Action: strings.Repeat("a", 65)},
			"long target":  {Actor: selfhost.SystemActor, Action: "x", Target: strings.Repeat("a", 257)},
			"many details": {Actor: selfhost.SystemActor, Action: "x", Detail: big},
			"long value":   {Actor: selfhost.SystemActor, Action: "x", Detail: map[string]string{"k": strings.Repeat("v", selfhost.MaxAuditDetailValueBytes+1)}},
			"empty key":    {Actor: selfhost.SystemActor, Action: "x", Detail: map[string]string{"": "v"}},
		} {
			if _, err := f.store.AppendAudit(f.ctx, input); !errors.Is(err, selfhost.ErrInvalidInput) {
				t.Fatalf("%s: got %v", name, err)
			}
		}
		report, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !report.OK || report.Rows != 0 || report.HeadHash != strings.Repeat("0", 64) {
			t.Fatalf("unexpected empty report %+v", report)
		}
	})
	each(t, h, "concurrent appends keep one chain", func(f *fixture) {
		var wg sync.WaitGroup
		for index := 0; index < 20; index++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := f.store.AppendAudit(f.ctx, selfhost.AuditInput{Actor: selfhost.SystemActor, Action: "tick", Target: "t"}); err != nil {
					t.Error(err)
				}
			}()
		}
		wg.Wait()
		report, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !report.OK || report.Rows != 20 {
			t.Fatalf("unexpected report %+v", report)
		}
	})
}

func mapValues(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return out
}

func testFlagsAndInstance(t *testing.T, h Harness) {
	each(t, h, "flags have defaults, change, and persist", func(f *fixture) {
		flags, err := f.store.Flags(f.ctx)
		f.must(err)
		if len(flags) != 2 || flags[0].Key != "metrics" || !flags[0].Enabled || flags[1].Key != "sync" || flags[1].Enabled || flags[1].Description != "Sync" {
			t.Fatalf("unexpected flags %+v", flags)
		}
		by := selfhost.SystemActor
		flag, err := f.store.SetFlag(f.ctx, by, "sync", true)
		f.must(err)
		if !flag.Enabled {
			t.Fatal("flag was not enabled")
		}
		before, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		_, err = f.store.SetFlag(f.ctx, by, "sync", true)
		f.must(err)
		after, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if after.Rows != before.Rows {
			t.Fatal("an unchanged flag must not write audit rows")
		}
		_, err = f.store.SetFlag(f.ctx, by, "unknown", true)
		f.expect(err, selfhost.ErrNotFound)
		_, err = f.store.SetFlag(f.ctx, by, "", true)
		f.expect(err, selfhost.ErrNotFound)
		f.reopen()
		flags, err = f.store.Flags(f.ctx)
		f.must(err)
		if !flags[1].Enabled {
			t.Fatal("flag state was lost on restart")
		}
	})
	each(t, h, "instance settings validate and persist", func(f *fixture) {
		instance, err := f.store.Instance(f.ctx)
		f.must(err)
		if len(instance.ID) != 32 || instance.Name != "Sesame" || instance.PublicURL != "" {
			t.Fatalf("unexpected instance %+v", instance)
		}
		name := "Home server"
		url := "https://Sesame.Example.test/"
		updated, err := f.store.UpdateInstance(f.ctx, selfhost.SystemActor, selfhost.InstanceUpdate{Name: &name, PublicURL: &url})
		f.must(err)
		if updated.Name != name || updated.PublicURL != "https://sesame.example.test" || updated.ID != instance.ID {
			t.Fatalf("unexpected update %+v", updated)
		}
		for _, bad := range []string{"http://sesame.example.test", "ftp://example.test", "https://", "https://u:p@example.test", "https://example.test/path", "https://example.test?x=1", "https://example.test#frag", "javascript:alert(1)", "not a url"} {
			value := bad
			if _, err := f.store.UpdateInstance(f.ctx, selfhost.SystemActor, selfhost.InstanceUpdate{PublicURL: &value}); !errors.Is(err, selfhost.ErrInvalidInput) {
				t.Fatalf("%q: got %v", bad, err)
			}
		}
		for _, good := range []string{"http://localhost:8787", "http://127.0.0.1:8787/", "http://[::1]:8787", ""} {
			value := good
			if _, err := f.store.UpdateInstance(f.ctx, selfhost.SystemActor, selfhost.InstanceUpdate{PublicURL: &value}); err != nil {
				t.Fatalf("%q: %v", good, err)
			}
		}
		empty := " "
		_, err = f.store.UpdateInstance(f.ctx, selfhost.SystemActor, selfhost.InstanceUpdate{Name: &empty})
		f.expect(err, selfhost.ErrInvalidInput)
		f.reopen()
		again, err := f.store.Instance(f.ctx)
		f.must(err)
		if again.ID != instance.ID || again.Name != name {
			t.Fatalf("instance changed on restart: %+v", again)
		}
	})
}

func testRateLimits(t *testing.T, h Harness) {
	each(t, h, "counters block after the limit and recover with the window", func(f *fixture) {
		for attempt := 1; attempt <= 3; attempt++ {
			decision, err := f.store.RateLimit(f.ctx, "login:a", 3, time.Minute)
			f.must(err)
			if !decision.Allowed || decision.Remaining != 3-attempt {
				t.Fatalf("attempt %d: %+v", attempt, decision)
			}
		}
		decision, err := f.store.RateLimit(f.ctx, "login:a", 3, time.Minute)
		f.must(err)
		if decision.Allowed || decision.Remaining != 0 || decision.RetryAfter <= 0 || decision.RetryAfter > time.Minute {
			t.Fatalf("unexpected decision %+v", decision)
		}
		other, err := f.store.RateLimit(f.ctx, "login:b", 3, time.Minute)
		f.must(err)
		if !other.Allowed {
			t.Fatal("keys must be independent")
		}
		f.clock.Advance(time.Minute)
		decision, err = f.store.RateLimit(f.ctx, "login:a", 3, time.Minute)
		f.must(err)
		if !decision.Allowed || decision.Remaining != 2 {
			t.Fatalf("window did not reset: %+v", decision)
		}
		f.must(f.store.ResetRateLimit(f.ctx, "login:a"))
		decision, err = f.store.RateLimit(f.ctx, "login:a", 3, time.Minute)
		f.must(err)
		if decision.Remaining != 2 {
			t.Fatalf("reset did not clear: %+v", decision)
		}
	})
	each(t, h, "invalid rate limit input is refused", func(f *fixture) {
		for _, call := range []func() error{
			func() error { _, err := f.store.RateLimit(f.ctx, "", 1, time.Minute); return err },
			func() error { _, err := f.store.RateLimit(f.ctx, strings.Repeat("k", 257), 1, time.Minute); return err },
			func() error { _, err := f.store.RateLimit(f.ctx, "k", 0, time.Minute); return err },
			func() error { _, err := f.store.RateLimit(f.ctx, "k", 1, 0); return err },
		} {
			f.expect(call(), selfhost.ErrInvalidInput)
		}
	})
	each(t, h, "concurrent hits are counted exactly", func(f *fixture) {
		var wg sync.WaitGroup
		var mu sync.Mutex
		allowed := 0
		for index := 0; index < 30; index++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				decision, err := f.store.RateLimit(f.ctx, "shared", 10, time.Hour)
				if err != nil {
					t.Error(err)
					return
				}
				if decision.Allowed {
					mu.Lock()
					allowed++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if allowed != 10 {
			t.Fatalf("%d hits allowed", allowed)
		}
	})
}

func testMaintenance(t *testing.T, h Harness) {
	each(t, h, "cleanup removes expired rows and keeps live data and audit", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		gone := f.member(by, "Gone")
		old := f.pair(by, memberHolder(gone), "Old laptop")
		_, err := f.store.DeleteMember(f.ctx, by, gone.ID)
		f.must(err)
		_, err = f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: ownerHolder(owner)})
		f.must(err)
		_, err = f.store.RateLimit(f.ctx, "k", 5, time.Minute)
		f.must(err)
		f.clock.Advance(31 * 24 * time.Hour)
		keeper := f.member(by, "Keeper")
		fresh := f.pair(by, memberHolder(keeper), "Fresh laptop")
		session, err := f.login("Avery", testPassword)
		f.must(err)
		before, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		report, err := f.store.Maintain(f.ctx)
		f.must(err)
		if report.Sessions < 1 || report.SetupTokens < 1 || report.Pairings < 2 || report.Devices != 1 || report.RateLimits != 1 || report.Holders != 1 {
			t.Fatalf("unexpected report %+v", report)
		}
		_, err = f.store.AuthenticateDevice(f.ctx, old.Token)
		f.expect(err, selfhost.ErrDeviceInvalid)
		_, err = f.store.AuthenticateDevice(f.ctx, fresh.Token)
		f.must(err)
		_, err = f.store.Session(f.ctx, session.Token)
		f.must(err)
		_, err = f.store.Session(f.ctx, owner.Token)
		f.expect(err, selfhost.ErrSessionInvalid)
		after, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !after.OK || after.Rows != before.Rows || after.HeadHash != before.HeadHash {
			t.Fatalf("maintenance touched the audit log: %+v %+v", before, after)
		}
		check, err := f.store.Check(f.ctx)
		f.must(err)
		if !check.OK() {
			t.Fatalf("check failed after maintenance: %+v", check)
		}
		again, err := f.store.Maintain(f.ctx)
		f.must(err)
		if again != (selfhost.MaintenanceReport{}) {
			t.Fatalf("second run removed more: %+v", again)
		}
		members, err := f.store.ListMembers(f.ctx)
		f.must(err)
		if len(members) != 1 || members[0].Name != "Keeper" {
			t.Fatalf("unexpected members %+v", members)
		}
	})
}

func testStorage(t *testing.T, h Harness) {
	each(t, h, "data survives a restart", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		device := f.pair(by, memberHolder(member), "Laptop")
		f.reopen()
		known, err := f.store.AuthenticateDevice(f.ctx, device.Token)
		f.must(err)
		if known.Holder.Name != "Sam" {
			t.Fatalf("unexpected holder %+v", known.Holder)
		}
		f.step()
		_, err = f.login("Avery", testPassword)
		f.must(err)
		report, err := f.store.VerifyAudit(f.ctx)
		f.must(err)
		if !report.OK {
			t.Fatalf("chain broke across restart: %+v", report)
		}
	})
	each(t, h, "backups are complete, private and openable", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		device := f.pair(by, memberHolder(member), "Laptop")
		destination := f.path + ".backup"
		f.must(f.store.Backup(f.ctx, destination))
		info, err := os.Stat(destination)
		f.must(err)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("backup mode %v", info.Mode().Perm())
		}
		if _, err := os.Stat(destination + ".partial"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("partial backup file was left behind")
		}
		f.expect(f.store.Backup(f.ctx, destination), selfhost.ErrConflict)
		f.member(by, "After backup")
		config := f.config()
		config.Path = destination
		copyStore := f.h.Open(t, config)
		known, err := copyStore.AuthenticateDevice(f.ctx, device.Token)
		f.must(err)
		if known.ID != device.Device.ID {
			t.Fatal("backup is missing the device")
		}
		_, err = copyStore.Session(f.ctx, owner.Token)
		f.must(err)
		members, err := copyStore.ListMembers(f.ctx)
		f.must(err)
		if len(members) != 1 {
			t.Fatalf("backup holds %d members", len(members))
		}
		check, err := copyStore.Check(f.ctx)
		f.must(err)
		if !check.OK() {
			t.Fatalf("backup failed its check: %+v", check)
		}
		f.must(copyStore.Close())
		config.ReadOnly = true
		readOnly := f.h.Open(t, config)
		_, err = readOnly.ListMembers(f.ctx)
		f.must(err)
		if _, err := readOnly.CreateMember(f.ctx, by, "Nope"); err == nil {
			t.Fatal("a read only store accepted a write")
		}
	})
	each(t, h, "system information reports counts and the last backup", func(f *fixture) {
		owner := f.firstOwner("Avery")
		by := actorOf(owner)
		member := f.member(by, "Sam")
		f.pair(by, memberHolder(member), "Laptop")
		_, err := f.store.CreatePairing(f.ctx, by, selfhost.PairingInput{Holder: ownerHolder(owner)})
		f.must(err)
		info, err := f.store.System(f.ctx)
		f.must(err)
		if info.SchemaVersion < 1 || info.DatabaseBytes <= 0 || info.ActiveOwners != 1 || info.Members != 1 || info.ActiveDevices != 1 || info.PendingPairing != 1 || info.LastBackupAt != nil {
			t.Fatalf("unexpected info %+v", info)
		}
		when := f.clock.Now().Add(time.Hour)
		f.must(f.store.RecordBackup(f.ctx, when))
		info, err = f.store.System(f.ctx)
		f.must(err)
		if info.LastBackupAt == nil || !info.LastBackupAt.Equal(when) {
			t.Fatalf("unexpected backup time %+v", info.LastBackupAt)
		}
		check, err := f.store.Check(f.ctx)
		f.must(err)
		if !check.OK() || check.SchemaVersion != info.SchemaVersion {
			t.Fatalf("unexpected check %+v", check)
		}
	})
	each(t, h, "invalid options and closed stores are refused", func(f *fixture) {
		config := f.config()
		config.Path = ""
		if _, err := f.h.TryOpen(config); err == nil {
			t.Fatal("empty path accepted")
		}
		config = f.config()
		config.AdminKey = []byte("short")
		if _, err := f.h.TryOpen(config); err == nil {
			t.Fatal("short admin key accepted")
		}
		config = f.config()
		config.Flags = []selfhost.FlagDefinition{{Key: "Bad Key"}}
		if _, err := f.h.TryOpen(config); err == nil {
			t.Fatal("invalid flag key accepted")
		}
		config = f.config()
		config.Path = f.path + ".missing"
		config.ReadOnly = true
		if _, err := f.h.TryOpen(config); err == nil {
			t.Fatal("read only open of a missing file accepted")
		}
		f.must(f.store.Close())
		f.must(f.store.Close())
		_, err := f.store.Instance(f.ctx)
		f.expect(err, selfhost.ErrClosed)
		_, err = f.store.CreateMember(f.ctx, selfhost.SystemActor, "Sam")
		f.expect(err, selfhost.ErrClosed)
		f.expect(f.store.Ping(f.ctx), selfhost.ErrClosed)
	})
	each(t, h, "the database file is private", func(f *fixture) {
		f.firstOwner("Avery")
		info, err := os.Stat(f.path)
		f.must(err)
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("database mode %v", info.Mode().Perm())
		}
	})
}

func testSchema(t *testing.T, h Harness) {
	each(t, h, "a schema newer than the binary is refused untouched", func(f *fixture) {
		f.firstOwner("Avery")
		f.must(f.store.Close())
		db := f.raw()
		if _, err := db.Exec(`INSERT INTO schema_migrations (version, name, applied_at) VALUES (9999, 'from_the_future', 0)`); err != nil {
			t.Fatal(err)
		}
		_, err := f.h.TryOpen(f.config())
		if !errors.Is(err, selfhost.ErrSchemaTooNew) {
			t.Fatalf("got %v", err)
		}
		var newer selfhost.SchemaTooNewError
		if !errors.As(err, &newer) || newer.Stored != 9999 {
			t.Fatalf("unexpected error detail %v", err)
		}
		readOnly := f.config()
		readOnly.ReadOnly = true
		if _, err := f.h.TryOpen(readOnly); !errors.Is(err, selfhost.ErrSchemaTooNew) {
			t.Fatalf("read only open got %v", err)
		}
		var version int
		f.must(db.QueryRow(`SELECT MAX(version) FROM schema_migrations`).Scan(&version))
		var owners int
		f.must(db.QueryRow(`SELECT COUNT(*) FROM owners`).Scan(&owners))
		if version != 9999 || owners != 1 {
			t.Fatalf("a refused open changed the database: version %d owners %d", version, owners)
		}
	})
	each(t, h, "concurrent starts migrate once", func(f *fixture) {
		f.must(f.store.Close())
		config := f.config()
		config.Path = f.path + ".fresh"
		var wg sync.WaitGroup
		stores := make(chan selfhost.Store, 6)
		for index := 0; index < 6; index++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				store, err := f.h.TryOpen(config)
				if err != nil {
					t.Error(err)
					return
				}
				stores <- store
			}()
		}
		wg.Wait()
		close(stores)
		for store := range stores {
			t.Cleanup(func() { _ = store.Close() })
		}
		db := f.h.Raw(t, config.Path)
		var applied, versions, instances int
		f.must(db.QueryRow(`SELECT COUNT(*), COUNT(DISTINCT version) FROM schema_migrations`).Scan(&applied, &versions))
		f.must(db.QueryRow(`SELECT COUNT(*) FROM instance`).Scan(&instances))
		if applied != versions || instances != 1 {
			t.Fatalf("applied %d versions %d instances %d", applied, versions, instances)
		}
	})
	t.Run("an interrupted migration leaves the database usable", func(t *testing.T) {
		if h.InterruptedMigration == nil {
			t.Fatal("harness must provide an interrupted migration scenario")
		}
		h.InterruptedMigration(t, t.TempDir()+"/sesame.db", bytesOf(0x42))
	})
}

func bytesOf(value byte) []byte {
	out := make([]byte, 32)
	for index := range out {
		out[index] = value
	}
	return out
}
