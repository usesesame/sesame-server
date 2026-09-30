package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

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
	truncateAuditTables(t, accountStore.DB(), `TRUNCATE sesame_admin_audit_log, sesame_admin_audit_checkpoints RESTART IDENTITY`)
	store, err := Open(context.Background(), databaseURL, bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, accountStore.DB(), databaseURL
}

func truncateAuditTables(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `DROP TRIGGER IF EXISTS sesame_admin_audit_chain_truncate ON sesame_admin_audit_log`); err != nil {
		t.Fatalf("drop the audit truncate trigger: %v", err)
	}
	if _, err := db.ExecContext(ctx, statement); err != nil {
		t.Fatalf("truncate audit tables: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		INSERT INTO sesame_admin_audit_chain_head (singleton, head_seq, head_hash)
		VALUES (TRUE, 0, decode(repeat('00', 32), 'hex'))
		ON CONFLICT (singleton) DO UPDATE SET head_seq = 0, head_hash = decode(repeat('00', 32), 'hex')`); err != nil {
		t.Fatalf("reset the audit chain head: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER sesame_admin_audit_chain_truncate
		BEFORE TRUNCATE ON sesame_admin_audit_log
		FOR EACH STATEMENT EXECUTE FUNCTION sesame_reject_admin_audit_truncate()`); err != nil {
		t.Fatalf("restore the audit truncate trigger: %v", err)
	}
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
	checkpoint, err := CheckpointAuditChain(ctx, db, privateKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("write audit checkpoint: %v", err)
	}
	if checkpoint == nil {
		t.Fatal("no audit checkpoint was written for a new chain")
	}
	if checkpoint.CoverSeq != 3 || checkpoint.KeyID != "fictional-capability-key" || len(checkpoint.Signature) != ed25519.SignatureSize {
		t.Fatalf("checkpoint = %#v, want a signed checkpoint covering sequence 3", checkpoint)
	}
	report, err := VerifyAuditCheckpoints(ctx, db, publicKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("verify audit checkpoints: %v", err)
	}
	if !report.Verified || report.Checkpoints != 1 || report.Latest == nil || report.Latest.CoverSeq != 3 {
		t.Fatalf("checkpoint report = %#v, want one verified checkpoint covering sequence 3", report)
	}
	checkpoint, err = CheckpointAuditChain(ctx, db, privateKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("repeat audit checkpoint: %v", err)
	}
	if checkpoint != nil {
		t.Fatal("an audit checkpoint was repeated without new rows")
	}
	ids := appendAuditRows(t, store, db, 1)
	checkpoint, err = CheckpointAuditChain(ctx, db, privateKey, "fictional-capability-key")
	if err != nil {
		t.Fatalf("advance audit checkpoint: %v", err)
	}
	if checkpoint == nil {
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

func insufficientPrivilege(err error) bool {
	var postgresError *pgconn.PgError
	return errors.As(err, &postgresError) && postgresError.Code == "42501"
}

func TestAuditChainRejectsTruncate(t *testing.T) {
	store, db, _ := newAuditChainTest(t)
	appendAuditRows(t, store, db, 2)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `TRUNCATE sesame_admin_audit_log`); err == nil {
		t.Fatal("the audit log was truncated")
	} else if !strings.Contains(err.Error(), "cannot be truncated") {
		t.Fatalf("truncate error = %v, want the truncate guard", err)
	}
	report, err := VerifyAuditChain(ctx, db)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if !report.Verified || report.Rows != 2 {
		t.Fatalf("chain report = %#v, want the two audit rows to survive", report)
	}
}

func ensureApplicationRole(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		DO $$
		BEGIN
			CREATE ROLE sesame_app NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
		EXCEPTION WHEN duplicate_object THEN
			NULL;
		END $$`); err != nil {
		t.Fatalf("ensure the application role: %v", err)
	}
}

func grantApplicationAuditAccess(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, statement := range []string{
		`GRANT USAGE ON SCHEMA public TO sesame_app`,
		`GRANT SELECT ON sesame_admin_audit_chain_head TO sesame_app`,
		`GRANT SELECT, INSERT ON sesame_admin_audit_log TO sesame_app`,
		`GRANT SELECT, INSERT ON sesame_admin_audit_checkpoints TO sesame_app`,
		`GRANT USAGE, SELECT ON SEQUENCE sesame_admin_audit_log_id_seq TO sesame_app`,
		`GRANT USAGE, SELECT ON SEQUENCE sesame_admin_audit_checkpoints_id_seq TO sesame_app`,
	} {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			t.Fatalf("%s: %v", statement, err)
		}
	}
}

func applicationRoleConnection(t *testing.T, db *sql.DB) *sql.Conn {
	t.Helper()
	ensureApplicationRole(t, db)
	grantApplicationAuditAccess(t, db)
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("reserve an application connection: %v", err)
	}
	t.Cleanup(func() {
		_, _ = conn.ExecContext(context.Background(), `RESET ROLE`)
		_ = conn.Close()
	})
	if _, err := conn.ExecContext(ctx, `SET ROLE sesame_app`); err != nil {
		t.Fatalf("assume the application role: %v", err)
	}
	return conn
}

func databaseURLWithName(t *testing.T, databaseURL, databaseName string) string {
	t.Helper()
	parsed, err := url.Parse(databaseURL)
	if err != nil {
		t.Fatalf("parse the test database URL: %v", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		t.Fatal("the test database URL must use the postgres:// form")
	}
	parsed.Path = "/" + databaseName
	return parsed.String()
}

func TestAuditChainDeniesTheApplicationRoleHeadWrites(t *testing.T) {
	ctx := context.Background()
	databaseURL := os.Getenv("SESAME_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("SESAME_TEST_DATABASE_URL is required")
	}
	accountStore, err := accounts.OpenWithoutMigrate(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open the test database: %v", err)
	}
	t.Cleanup(func() { _ = accountStore.Close() })
	ensureApplicationRole(t, accountStore.DB())

	databaseName := fmt.Sprintf("sesame_audit_chain_%d_%d", os.Getpid(), time.Now().UnixNano())
	if _, err := accountStore.DB().ExecContext(ctx, `CREATE DATABASE `+databaseName); err != nil {
		t.Fatalf("create a fresh audit chain test database: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext := context.Background()
		_, _ = accountStore.DB().ExecContext(cleanupContext, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1 AND pid <> pg_backend_pid()`, databaseName)
		if _, err := accountStore.DB().ExecContext(cleanupContext, `DROP DATABASE IF EXISTS `+databaseName); err != nil {
			t.Errorf("drop the fresh audit chain test database: %v", err)
		}
	})
	freshStore, err := accounts.OpenWithoutMigrate(ctx, databaseURLWithName(t, databaseURL, databaseName))
	if err != nil {
		t.Fatalf("open the fresh audit chain test database: %v", err)
	}
	t.Cleanup(func() { _ = freshStore.Close() })
	if _, err := freshStore.DB().ExecContext(ctx, `ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT UPDATE, DELETE, TRUNCATE ON TABLES TO sesame_app`); err != nil {
		t.Fatalf("grant the pre-revoke default privileges: %v", err)
	}
	if err := freshStore.Migrate(ctx); err != nil {
		t.Fatalf("migrate the fresh audit chain test database: %v", err)
	}
	if _, err := freshStore.DB().ExecContext(ctx, `REVOKE UPDATE, DELETE, TRUNCATE ON TABLE sesame_admin_audit_log FROM sesame_app`); err != nil {
		t.Fatalf("revoke the default-granted audit log privileges: %v", err)
	}
	grantApplicationAuditAccess(t, freshStore.DB())

	conn := applicationRoleConnection(t, freshStore.DB())
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO sesame_admin_audit_log (admin_email, action, target_type, detail)
		VALUES ('app-role@example.invalid', 'app.append', 'test', '{"fictional": true}'::jsonb)`); err != nil {
		t.Fatalf("the application role must append audit rows: %v", err)
	}
	var headSeq int64
	if err := freshStore.DB().QueryRowContext(ctx, `SELECT head_seq FROM public.sesame_admin_audit_chain_head WHERE singleton`).Scan(&headSeq); err != nil {
		t.Fatalf("read the audit chain head: %v", err)
	}
	if headSeq != 1 {
		t.Fatalf("head sequence = %d, want 1 after the application insert", headSeq)
	}
	report, err := VerifyAuditChain(ctx, freshStore.DB())
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if !report.Verified || report.Rows != 1 {
		t.Fatalf("chain report = %#v, want the application row to chain", report)
	}
	for _, statement := range []string{
		`UPDATE sesame_admin_audit_chain_head SET head_seq = 99 WHERE singleton`,
		`DELETE FROM sesame_admin_audit_chain_head WHERE singleton`,
		`DELETE FROM sesame_admin_audit_checkpoints`,
		`TRUNCATE sesame_admin_audit_log`,
	} {
		if _, err := conn.ExecContext(ctx, statement); err == nil {
			t.Fatalf("the application role ran %q", statement)
		} else if !insufficientPrivilege(err) {
			t.Fatalf("%q failed without a privilege error: %v", statement, err)
		}
	}
	var headRows int64
	var heldSeq int64
	if err := freshStore.DB().QueryRowContext(ctx, `SELECT COUNT(*), COALESCE(MAX(head_seq), -1) FROM public.sesame_admin_audit_chain_head`).Scan(&headRows, &heldSeq); err != nil {
		t.Fatalf("read the audit chain head after the denied writes: %v", err)
	}
	if headRows != 1 || heldSeq != 1 {
		t.Fatalf("chain head rows = %d sequence = %d, want a single row at sequence 1", headRows, heldSeq)
	}
}

func TestAuditChainIgnoresATemporaryTableShadowingTheHead(t *testing.T) {
	_, db, _ := newAuditChainTest(t)
	ctx := context.Background()
	conn := applicationRoleConnection(t, db)
	if _, err := conn.ExecContext(ctx, `
		CREATE TEMP TABLE sesame_admin_audit_chain_head (
			singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
			head_seq BIGINT NOT NULL,
			head_hash BYTEA NOT NULL
		)`); err != nil {
		t.Fatalf("create the shadowing temporary table: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO sesame_admin_audit_log (admin_email, action, target_type, detail)
		VALUES ('app-role@example.invalid', 'app.shadow', 'test', '{"fictional": true}'::jsonb)`); err != nil {
		t.Fatalf("the application role must append audit rows: %v", err)
	}
	var shadowRows int64
	if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM pg_temp.sesame_admin_audit_chain_head`).Scan(&shadowRows); err != nil {
		t.Fatalf("count the shadowing temporary table: %v", err)
	}
	if shadowRows != 0 {
		t.Fatalf("shadowing temporary table rows = %d, want 0", shadowRows)
	}
	var headSeq int64
	if err := db.QueryRowContext(ctx, `SELECT head_seq FROM public.sesame_admin_audit_chain_head WHERE singleton`).Scan(&headSeq); err != nil {
		t.Fatalf("read the real audit chain head: %v", err)
	}
	if headSeq != 1 {
		t.Fatalf("real head sequence = %d, want 1 after the application insert", headSeq)
	}
	report, err := VerifyAuditChain(ctx, db)
	if err != nil {
		t.Fatalf("verify audit chain: %v", err)
	}
	if !report.Verified || report.Rows != 1 {
		t.Fatalf("chain report = %#v, want the application row to chain", report)
	}
}

func readAuditChainMigration(t *testing.T) string {
	t.Helper()
	source, err := os.ReadFile(filepath.Join("..", "accounts", "migrations", "0044_admin_audit_hash_chain.sql"))
	if err != nil {
		t.Fatalf("read the audit hash chain migration: %v", err)
	}
	return string(source)
}

func TestAuditChainMigrationPinsTheDefinerFunction(t *testing.T) {
	migration := readAuditChainMigration(t)
	start := strings.Index(migration, "CREATE OR REPLACE FUNCTION sesame_chain_admin_audit_row()")
	if start < 0 {
		t.Fatal("the audit hash chain migration must define sesame_chain_admin_audit_row")
	}
	rest := migration[start:]
	end := strings.Index(rest, "$$ LANGUAGE plpgsql;")
	if end < 0 {
		t.Fatal("the sesame_chain_admin_audit_row definition must end with $$ LANGUAGE plpgsql;")
	}
	definition := rest[:end]
	if !strings.Contains(definition, "SET search_path = pg_catalog, public, pg_temp") {
		t.Fatal("sesame_chain_admin_audit_row must pin SET search_path = pg_catalog, public, pg_temp")
	}
	qualified := strings.ReplaceAll(definition, "public.sesame_admin_audit_chain_head", "")
	qualified = strings.ReplaceAll(qualified, "public.sesame_admin_audit_row_hash", "")
	for _, name := range []string{"sesame_admin_audit_chain_head", "sesame_admin_audit_row_hash"} {
		if strings.Contains(qualified, name) {
			t.Fatalf("sesame_chain_admin_audit_row must qualify every %s reference with public.", name)
		}
	}
}

func TestAuditChainMigrationRevokesTheApplicationRole(t *testing.T) {
	migration := readAuditChainMigration(t)
	for _, statement := range []string{
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE ON TABLE sesame_admin_audit_chain_head FROM sesame_app",
		"REVOKE UPDATE, DELETE ON TABLE sesame_admin_audit_checkpoints FROM sesame_app",
	} {
		if !strings.Contains(migration, statement) {
			t.Fatalf("the audit hash chain migration must keep %q", statement)
		}
	}
}
