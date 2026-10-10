package sqlitestore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"usesesame.app/backend/internal/selfhost"
)

func openAuditStore(tb testing.TB) *Store {
	tb.Helper()
	key := make([]byte, adminKeyLength)
	if _, err := rand.Read(key); err != nil {
		tb.Fatal(err)
	}
	store, err := Open(context.Background(), Options{Path: filepath.Join(tb.TempDir(), "audit.db"), AdminKey: key})
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = store.Close() })
	return store
}

func seedAudit(tb testing.TB, store *Store, rows int) {
	tb.Helper()
	ctx := context.Background()
	for done := 0; done < rows; done += 10000 {
		batch := min(10000, rows-done)
		err := store.write(ctx, func(tx *sql.Tx) error {
			for index := 0; index < batch; index++ {
				if err := store.audit(ctx, tx, selfhost.SystemActor, "seed.row", fmt.Sprint(done+index), map[string]string{"k": "v"}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			tb.Fatal(err)
		}
	}
}

func TestIncrementalCheckRejectsAForgedLinkFromTheCachedHead(t *testing.T) {
	store := openAuditStore(t)
	ctx := context.Background()
	seedAudit(t, store, 5)
	baseline, err := store.VerifyAuditIncremental(ctx)
	if err != nil || !baseline.OK || baseline.Rows != 5 {
		t.Fatalf("baseline %+v %v", baseline, err)
	}
	forgedPrevious := auditHash(auditGenesisHash, []byte("unrelated"))
	canonical, err := auditCanonical(selfhost.SystemActor.String(), "forged.row", "x", map[string]string{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO audit_log (seq, actor, action, target, detail, at, prev_hash, hash) VALUES (6, ?, 'forged.row', 'x', '{}', 1, ?, ?)`,
		selfhost.SystemActor.String(), forgedPrevious, auditHash(forgedPrevious, canonical))
	if err != nil {
		t.Fatal(err)
	}
	report, err := store.VerifyAuditIncremental(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || report.FirstBreak == nil || report.FirstBreak.Seq != 6 || report.FirstBreak.Reason != "previous hash does not match the preceding row" {
		t.Fatalf("forged link not detected: %+v", report)
	}
}

func TestIncrementalCheckReadsOnlyRowsAfterTheCachedHead(t *testing.T) {
	store := openAuditStore(t)
	ctx := context.Background()
	seedAudit(t, store, 50)
	if report, err := store.VerifyAuditIncremental(ctx); err != nil || !report.OK {
		t.Fatalf("baseline %+v %v", report, err)
	}
	if _, err := store.db.ExecContext(ctx, `DROP TRIGGER audit_log_no_update`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE audit_log SET target = 'forged' WHERE seq = 10`); err != nil {
		t.Fatal(err)
	}
	seedAudit(t, store, 3)
	incremental, err := store.VerifyAuditIncremental(ctx)
	if err != nil || !incremental.OK || incremental.Rows != 53 {
		t.Fatalf("incremental %+v %v", incremental, err)
	}
	full, err := store.VerifyAudit(ctx)
	if err != nil || full.OK || full.FirstBreak == nil || full.FirstBreak.Seq != 10 {
		t.Fatalf("full %+v %v", full, err)
	}
}

func BenchmarkVerifyAuditLargeLog(b *testing.B) {
	store := openAuditStore(b)
	ctx := context.Background()
	seedAudit(b, store, 200000)
	b.Run("full", func(b *testing.B) {
		for range b.N {
			if report, err := store.VerifyAudit(ctx); err != nil || !report.OK {
				b.Fatalf("%+v %v", report, err)
			}
		}
	})
	b.Run("incremental", func(b *testing.B) {
		for range b.N {
			if report, err := store.VerifyAuditIncremental(ctx); err != nil || !report.OK {
				b.Fatalf("%+v %v", report, err)
			}
		}
	})
}
