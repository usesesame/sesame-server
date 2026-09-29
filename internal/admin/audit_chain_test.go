package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"usesesame.app/backend/internal/accounts"
)

func newAuditChainTest(t *testing.T) (*Store, *sql.DB, string) {
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
	if _, err := accountStore.DB().ExecContext(context.Background(), `TRUNCATE sesame_admin_audit_log, sesame_admin_audit_checkpoints RESTART IDENTITY`); err != nil {
		t.Fatalf("clear audit tables: %v", err)
	}
	store, err := Open(context.Background(), databaseURL, bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, accountStore.DB(), databaseURL
}

func appendAuditRows(t *testing.T, store *Store, db *sql.DB, count int) []int64 {
	t.Helper()
	ctx := context.Background()
	actor := Account{ID: "audit-chain-admin", Email: "audit-chain-admin@example.invalid", Role: RoleSuper}
	ids := make([]int64, 0, count)
	for index := 0; index < count; index++ {
		targetID := fmt.Sprintf("fictional-target-%d", index)
		detail := map[string]any{
			"index": index,
			"nested": map[string]any{
				"label": fmt.Sprintf("fictional-\u00e9-%d", index),
				"flags": []any{true, false, nil, 1.5},
			},
		}
		err := store.mutate(ctx, actor, "test.chain.append", "test", targetID, "fictional-ip", detail, func(tx *sql.Tx) error { return nil })
		if err != nil {
			t.Fatalf("append audit row %d: %v", index, err)
		}
		var id int64
		if err := db.QueryRowContext(ctx, `SELECT id FROM sesame_admin_audit_log WHERE target_id = $1 ORDER BY id DESC LIMIT 1`, targetID).Scan(&id); err != nil {
			t.Fatalf("read audit row %d: %v", index, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func allowAuditMutations(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS sesame_admin_audit_log_append_only ON sesame_admin_audit_log`); err != nil {
		t.Fatalf("drop audit trigger: %v", err)
	}
	t.Cleanup(func() {
		if _, err := db.ExecContext(context.Background(), `
			CREATE TRIGGER sesame_admin_audit_log_append_only
			BEFORE UPDATE OR DELETE ON sesame_admin_audit_log
			FOR EACH ROW EXECUTE FUNCTION sesame_reject_admin_audit_mutation()`); err != nil {
			t.Fatalf("restore audit trigger: %v", err)
		}
	})
}

func TestAuditChainVerifies(t *testing.T) {
	store, db, _ := newAuditChainTest(t)
	ids := appendAuditRows(t, store, db, 5)
	report, err := VerifyAuditChain(context.Background(), db)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if !report.Verified || report.FirstBreak != nil {
		t.Fatalf("chain report = %#v, want a verified chain", report)
	}
	if report.Rows != 5 || report.HeadSeq != 5 || report.HeadID != ids[len(ids)-1] {
		t.Fatalf("chain report = %#v, want 5 rows ending at sequence 5 row %d", report, ids[len(ids)-1])
	}
	if len(report.HeadHash) != sha256.Size {
		t.Fatalf("head hash length = %d, want %d", len(report.HeadHash), sha256.Size)
	}
}

func TestAuditChainChainsInsertsWithoutChainColumns(t *testing.T) {
	store, db, _ := newAuditChainTest(t)
	appendAuditRows(t, store, db, 1)
	if _, err := db.ExecContext(context.Background(), `
		INSERT INTO sesame_admin_audit_log (admin_email, action, target_type, detail)
		VALUES ('legacy-admin@example.invalid', 'legacy.append', 'test', '{"fictional": true}'::jsonb)`); err != nil {
		t.Fatalf("insert legacy audit row: %v", err)
	}
	report, err := VerifyAuditChain(context.Background(), db)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if !report.Verified || report.Rows != 2 || report.HeadSeq != 2 {
		t.Fatalf("chain report = %#v, want a verified chain of 2 rows", report)
	}
}

func TestAuditChainHashMatchesTheDatabaseFunction(t *testing.T) {
	store, db, _ := newAuditChainTest(t)
	ids := appendAuditRows(t, store, db, 1)
	ctx := context.Background()
	var id int64
	var prevHash, rowHash []byte
	var canonical string
	if err := db.QueryRowContext(ctx, fmt.Sprintf(`
		SELECT audit.id, audit.prev_hash, audit.hash, %s
		FROM sesame_admin_audit_log audit WHERE audit.id = $1`, auditCanonicalRow("audit")), ids[0]).Scan(&id, &prevHash, &rowHash, &canonical); err != nil {
		t.Fatalf("read audit row: %v", err)
	}
	digest := sha256.New()
	digest.Write(prevHash)
	digest.Write([]byte(canonical))
	if !bytes.Equal(rowHash, digest.Sum(nil)) {
		t.Fatal("the stored hash does not match the Go canonical row")
	}
	var functionHash []byte
	if err := db.QueryRowContext(ctx, `
		SELECT sesame_admin_audit_row_hash(prev_hash, chain_seq, id, admin_id, admin_email, action, target_type, target_id, detail, ip_hash, created_at)
		FROM sesame_admin_audit_log WHERE id = $1`, id).Scan(&functionHash); err != nil {
		t.Fatalf("call the database hash function: %v", err)
	}
	if !bytes.Equal(rowHash, functionHash) {
		t.Fatal("the database hash function does not match the stored hash")
	}
}

func TestAuditChainDetectsAnEditedRow(t *testing.T) {
	store, db, _ := newAuditChainTest(t)
	ids := appendAuditRows(t, store, db, 3)
	allowAuditMutations(t, db)
	if _, err := db.ExecContext(context.Background(), `UPDATE sesame_admin_audit_log SET detail = '{"edited": true}'::jsonb WHERE id = $1`, ids[1]); err != nil {
		t.Fatalf("edit audit row: %v", err)
	}
	report, err := VerifyAuditChain(context.Background(), db)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if report.Verified {
		t.Fatal("an edited audit row verified")
	}
	if report.FirstBreak == nil || report.FirstBreak.ID != ids[1] || report.FirstBreak.Seq != 2 {
		t.Fatalf("first break = %#v, want sequence 2 row %d", report.FirstBreak, ids[1])
	}
	if !strings.Contains(report.FirstBreak.Reason, "contents") {
		t.Fatalf("first break reason = %q, want a content mismatch", report.FirstBreak.Reason)
	}
}

func TestAuditChainDetectsADeletedRow(t *testing.T) {
	store, db, _ := newAuditChainTest(t)
	ids := appendAuditRows(t, store, db, 3)
	allowAuditMutations(t, db)
	if _, err := db.ExecContext(context.Background(), `DELETE FROM sesame_admin_audit_log WHERE id = $1`, ids[1]); err != nil {
		t.Fatalf("delete audit row: %v", err)
	}
	report, err := VerifyAuditChain(context.Background(), db)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if report.Verified {
		t.Fatal("a deleted audit row verified")
	}
	if report.FirstBreak == nil || report.FirstBreak.ID != ids[2] || report.FirstBreak.Seq != 3 {
		t.Fatalf("first break = %#v, want sequence 3 row %d", report.FirstBreak, ids[2])
	}
	if !strings.Contains(report.FirstBreak.Reason, "link") {
		t.Fatalf("first break reason = %q, want a link mismatch", report.FirstBreak.Reason)
	}
}

func TestAuditChainStaysLinearUnderConcurrentAppends(t *testing.T) {
	store, db, _ := newAuditChainTest(t)
	const writers = 8
	var wait sync.WaitGroup
	errs := make([]error, writers)
	for index := 0; index < writers; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			actor := Account{
				ID:    fmt.Sprintf("audit-concurrent-%d", index),
				Email: fmt.Sprintf("audit-concurrent-%d@example.invalid", index),
				Role:  RoleSuper,
			}
			errs[index] = store.mutate(context.Background(), actor, "test.chain.concurrent", "test", fmt.Sprintf("fictional-concurrent-%d", index), "", map[string]any{"index": index}, func(tx *sql.Tx) error { return nil })
		}(index)
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("concurrent writer %d: %v", index, err)
		}
	}
	report, err := VerifyAuditChain(context.Background(), db)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if !report.Verified || report.Rows != writers || report.HeadSeq != writers {
		t.Fatalf("chain report = %#v, want a verified chain of %d rows", report, writers)
	}
	var rows, sequences, targets int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*), COUNT(DISTINCT chain_seq), COUNT(DISTINCT target_id) FROM sesame_admin_audit_log`).Scan(&rows, &sequences, &targets); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if rows != writers || sequences != writers || targets != writers {
		t.Fatalf("rows = %d sequences = %d targets = %d, want %d of each", rows, sequences, targets, writers)
	}
}

func TestAuditChainSurvivesRestart(t *testing.T) {
	store, db, databaseURL := newAuditChainTest(t)
	appendAuditRows(t, store, db, 3)
	if err := store.Close(); err != nil {
		t.Fatalf("close admin store: %v", err)
	}
	reopened, err := Open(context.Background(), databaseURL, bytes.Repeat([]byte{5}, 32))
	if err != nil {
		t.Fatalf("reopen admin store: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	appendAuditRows(t, reopened, db, 2)
	report, err := VerifyAuditChain(context.Background(), db)
	if err != nil {
		t.Fatalf("verify audit chain after restart: %v", err)
	}
	if !report.Verified || report.Rows != 5 || report.HeadSeq != 5 {
		t.Fatalf("chain report = %#v, want a verified chain of 5 rows", report)
	}
}

func TestAuditCheckpointsDetectRewrites(t *testing.T) {
	store, db, _ := newAuditChainTest(t)
	appendAuditRows(t, store, db, 3)
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate checkpoint key: %v", err)
	}
	ctx := context.Background()
	written, err := CheckpointAuditChain(ctx, db, privateKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("write audit checkpoint: %v", err)
	}
	if !written {
		t.Fatal("no audit checkpoint was written for a new chain")
	}
	report, err := VerifyAuditCheckpoints(ctx, db, publicKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("verify audit checkpoints: %v", err)
	}
	if !report.Verified || report.Checkpoints != 1 || report.Latest == nil || report.Latest.CoverSeq != 3 {
		t.Fatalf("checkpoint report = %#v, want one verified checkpoint covering sequence 3", report)
	}
	written, err = CheckpointAuditChain(ctx, db, privateKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("repeat audit checkpoint: %v", err)
	}
	if written {
		t.Fatal("an audit checkpoint was repeated without new rows")
	}
	ids := appendAuditRows(t, store, db, 1)
	written, err = CheckpointAuditChain(ctx, db, privateKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("advance audit checkpoint: %v", err)
	}
	if !written {
		t.Fatal("no audit checkpoint was written for an advanced chain")
	}
	report, err = VerifyAuditCheckpoints(ctx, db, publicKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("verify audit checkpoints: %v", err)
	}
	if !report.Verified || report.Checkpoints != 2 || report.Latest == nil || report.Latest.CoverSeq != 4 {
		t.Fatalf("checkpoint report = %#v, want the second checkpoint covering sequence 4", report)
	}
	allowAuditMutations(t, db)
	if _, err := db.ExecContext(ctx, `
		UPDATE sesame_admin_audit_log
		SET detail = '{"rewritten": true}'::jsonb,
		    hash = sha256(prev_hash || convert_to(jsonb_build_array(
		        chain_seq, id, admin_id, admin_email, action, target_type, target_id,
		        '{"rewritten": true}'::jsonb, ip_hash,
		        to_char(created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
		    )::text, 'UTF8'))
		WHERE id = $1`, ids[0]); err != nil {
		t.Fatalf("rewrite covered audit row: %v", err)
	}
	chainReport, err := VerifyAuditChain(ctx, db)
	if err != nil {
		t.Fatalf("verify rewritten audit chain: %v", err)
	}
	if !chainReport.Verified {
		t.Fatalf("rewritten chain did not verify on its own: %#v", chainReport)
	}
	report, err = VerifyAuditCheckpoints(ctx, db, publicKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("verify audit checkpoints: %v", err)
	}
	if report.Verified || !strings.Contains(report.FirstBreak, "does not match") {
		t.Fatalf("checkpoint report = %#v, want a detected rewritten row", report)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM sesame_admin_audit_log WHERE id = $1`, ids[0]); err != nil {
		t.Fatalf("delete covered audit row: %v", err)
	}
	report, err = VerifyAuditCheckpoints(ctx, db, publicKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("verify audit checkpoints: %v", err)
	}
	if report.Verified || report.FirstBreak == "" {
		t.Fatalf("checkpoint report = %#v, want a detected deleted row", report)
	}
}
