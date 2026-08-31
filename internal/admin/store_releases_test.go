package admin

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"usesesame.app/backend/internal/accounts"
)

func TestAcceptReleaseCandidateReplay(t *testing.T) {
	store, db := releaseTestStore(t)
	candidate := releaseTestCandidate("0.2.3", "a")
	first, err := store.AcceptReleaseCandidate(context.Background(), Account{Email: "release-pipeline"}, candidate, "test-ip")
	if err != nil {
		t.Fatalf("accept first candidate: %v", err)
	}
	second, err := store.AcceptReleaseCandidate(context.Background(), Account{Email: "release-pipeline"}, candidate, "test-ip")
	if err != nil {
		t.Fatalf("replay candidate: %v", err)
	}
	if second.ID != first.ID {
		t.Fatalf("replay release ID = %q, want %q", second.ID, first.ID)
	}
	assertReleaseCounts(t, db, 1, 1, 1)
}

func TestAcceptReleaseCandidateRejectsConflict(t *testing.T) {
	store, db := releaseTestStore(t)
	first := releaseTestCandidate("0.2.3", "a")
	if _, err := store.AcceptReleaseCandidate(context.Background(), Account{Email: "release-pipeline"}, first, "test-ip"); err != nil {
		t.Fatalf("accept first candidate: %v", err)
	}
	conflict := releaseTestCandidate("0.2.3", "b")
	if _, err := store.AcceptReleaseCandidate(context.Background(), Account{Email: "release-pipeline"}, conflict, "test-ip"); !errors.Is(err, ErrReleaseCandidateConflict) {
		t.Fatalf("conflicting candidate error = %v, want ErrReleaseCandidateConflict", err)
	}
	assertReleaseCounts(t, db, 1, 1, 1)
}

func TestAcceptReleaseCandidateRollsBackPartialInsert(t *testing.T) {
	store, db := releaseTestStore(t)
	candidate := releaseTestCandidate("0.2.3", "a")
	candidate.Artifact.Bytes = 0
	if _, err := store.AcceptReleaseCandidate(context.Background(), Account{Email: "release-pipeline"}, candidate, "test-ip"); err == nil {
		t.Fatal("accept invalid candidate succeeded")
	}
	assertReleaseCounts(t, db, 0, 0, 0)
}

func TestAcceptReleaseCandidateConcurrentReplay(t *testing.T) {
	store, db := releaseTestStore(t)
	candidate := releaseTestCandidate("0.2.3", "a")
	start := make(chan struct{})
	results := make(chan struct {
		id  string
		err error
	}, 6)
	var group sync.WaitGroup
	for range 6 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			release, err := store.AcceptReleaseCandidate(context.Background(), Account{Email: "release-pipeline"}, candidate, "test-ip")
			results <- struct {
				id  string
				err error
			}{release.ID, err}
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var releaseID string
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent candidate acceptance: %v", result.err)
		}
		if releaseID == "" {
			releaseID = result.id
		} else if result.id != releaseID {
			t.Fatalf("concurrent release ID = %q, want %q", result.id, releaseID)
		}
	}
	assertReleaseCounts(t, db, 1, 1, 1)
}

func TestReleaseCommandsUseCurrentRevision(t *testing.T) {
	store, db := releaseTestStore(t)
	candidate := releaseTestCandidate("0.2.3", "a")
	release, err := store.AcceptReleaseCandidate(context.Background(), Account{Email: "release-pipeline"}, candidate, "test-ip")
	if err != nil {
		t.Fatalf("accept candidate: %v", err)
	}
	actor := Account{Email: "operator@example.invalid"}
	if err := store.PublishRelease(context.Background(), actor, release.ID, PublishReleaseInput{ExpectedManifestRevision: 1}, "test-ip"); err != nil {
		t.Fatalf("publish release: %v", err)
	}
	if err := store.SetReleaseRollout(context.Background(), actor, release.ID, RolloutReleaseInput{ExpectedManifestRevision: 1, RolloutPercent: 25}, "test-ip"); !errors.Is(err, ErrManifestRevisionConflict) {
		t.Fatalf("stale rollout error = %v, want ErrManifestRevisionConflict", err)
	}
	if err := store.EmergencyStopRelease(context.Background(), actor, release.ID, EmergencyStopReleaseInput{ExpectedManifestRevision: 2}, "test-ip"); err != nil {
		t.Fatalf("emergency stop: %v", err)
	}
	if err := store.EmergencyStopRelease(context.Background(), actor, release.ID, EmergencyStopReleaseInput{ExpectedManifestRevision: 3}, "test-ip"); err != nil {
		t.Fatalf("repeated emergency stop: %v", err)
	}
	if err := store.WithdrawRelease(context.Background(), actor, release.ID, WithdrawReleaseInput{ExpectedManifestRevision: 3}, "test-ip"); err != nil {
		t.Fatalf("withdraw release: %v", err)
	}
	if err := store.PublishRelease(context.Background(), actor, release.ID, PublishReleaseInput{ExpectedManifestRevision: 4}, "test-ip"); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("republish withdrawn release error = %v, want ErrNotAllowed", err)
	}
	var status string
	var revision int64
	var enabled, stopped bool
	if err := db.QueryRowContext(context.Background(), `SELECT status, manifest_revision, update_enabled, kill_switch FROM sesame_releases WHERE id = $1`, release.ID).Scan(&status, &revision, &enabled, &stopped); err != nil {
		t.Fatalf("read release: %v", err)
	}
	if status != "withdrawn" || revision != 4 || enabled || !stopped {
		t.Fatalf("release = status:%s revision:%d enabled:%t stopped:%t", status, revision, enabled, stopped)
	}
	var auditCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_admin_audit_log WHERE target_id = $1`, release.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count release audit rows: %v", err)
	}
	if auditCount != 5 {
		t.Fatalf("release audit rows = %d, want 5", auditCount)
	}
}

func releaseTestStore(t *testing.T) (*Store, *sql.DB) {
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
	if _, err := accountStore.DB().ExecContext(context.Background(), `TRUNCATE sesame_releases, sesame_admin_audit_log RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("clear release tables: %v", err)
	}
	store, err := Open(context.Background(), databaseURL, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, accountStore.DB()
}

func releaseTestCandidate(version, digestCharacter string) ReleaseCandidate {
	digest := strings.Repeat(digestCharacter, 64)
	return ReleaseCandidate{
		Version:               version,
		Channel:               "beta",
		Platform:              "windows",
		Architecture:          "x86_64",
		SupportedWindows:      "Windows 10",
		ReleaseNotesURL:       "https://example.invalid/releases/" + version,
		CandidateSigningKeyID: "test-key",
		CandidateSignature:    "test-signature",
		SigningPayload:        "signed-payload-" + digest,
		Artifact: ReleaseArtifact{
			URL:                  "https://downloads.example.invalid/" + version + ".exe",
			ObjectKey:            "releases/" + version + "/Sesame.exe",
			SHA256:               digest,
			Bytes:                1,
			UpdaterSignature:     strings.Repeat("s", 64),
			UpdaterSigningKeyID:  "test-updater-key",
			DistributionClass:    "early_access",
			SigstoreEvidence:     map[string]any{"verified": true},
			SigstoreVerified:     true,
			SigstoreIssuer:       "https://token.actions.githubusercontent.com",
			SigstoreIdentity:     "test-identity",
			SigstoreBundleSHA256: strings.Repeat("c", 64),
		},
	}
}

func assertReleaseCounts(t *testing.T, db *sql.DB, releases, artifacts, audit int) {
	t.Helper()
	var actualReleases, actualArtifacts, actualAudit int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_releases`).Scan(&actualReleases); err != nil {
		t.Fatalf("count releases: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_release_artifacts`).Scan(&actualArtifacts); err != nil {
		t.Fatalf("count artifacts: %v", err)
	}
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_admin_audit_log WHERE action = 'release.candidate.accept'`).Scan(&actualAudit); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if actualReleases != releases || actualArtifacts != artifacts || actualAudit != audit {
		t.Fatalf("counts = releases:%d artifacts:%d audit:%d, want releases:%d artifacts:%d audit:%d", actualReleases, actualArtifacts, actualAudit, releases, artifacts, audit)
	}
}
