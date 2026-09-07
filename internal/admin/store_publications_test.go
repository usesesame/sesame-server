package admin

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"usesesame.app/backend/internal/accounts"
)

func publicationTestStore(t *testing.T) (*Store, *sql.DB) {
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
	if _, err := accountStore.DB().ExecContext(context.Background(), `TRUNCATE sesame_extension_publications, sesame_admin_audit_log RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("clear extension publication tables: %v", err)
	}
	store, err := Open(context.Background(), databaseURL, bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatalf("open admin store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, accountStore.DB()
}

const testPackageSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func publicationCandidate(storeName, version string) ExtensionPublicationCandidate {
	return ExtensionPublicationCandidate{
		Store:         storeName,
		Version:       version,
		PackageSHA256: testPackageSHA256,
		PackageBytes:  48213,
		Filename:      "sesame-extension-" + version + "-" + storeName + ".zip",
		Evidence:      map[string]any{"built": map[string]any{"runURL": "https://ci.test.invalid/run/1"}},
	}
}

func transitionInput(revision int64, to string, evidence map[string]any) ExtensionTransitionInput {
	return ExtensionTransitionInput{ExpectedStateRevision: revision, To: to, Evidence: evidence}
}

func TestAcceptExtensionPublicationReplay(t *testing.T) {
	store, _ := publicationTestStore(t)
	candidate := publicationCandidate("chrome", "0.1.0")
	first, err := store.AcceptExtensionPublication(context.Background(), Account{Email: "release-pipeline"}, candidate, "test-ip")
	if err != nil {
		t.Fatalf("accept first publication: %v", err)
	}
	second, err := store.AcceptExtensionPublication(context.Background(), Account{Email: "release-pipeline"}, candidate, "test-ip")
	if err != nil {
		t.Fatalf("replay publication: %v", err)
	}
	if second.ID != first.ID || second.Status != "built" || second.StateRevision != 1 {
		t.Fatalf("replay = id %q status %q revision %d, want id %q built 1", second.ID, second.Status, second.StateRevision, first.ID)
	}
}

func TestAcceptExtensionPublicationRejectsConflict(t *testing.T) {
	store, _ := publicationTestStore(t)
	if _, err := store.AcceptExtensionPublication(context.Background(), Account{Email: "release-pipeline"}, publicationCandidate("chrome", "0.1.0"), "test-ip"); err != nil {
		t.Fatalf("accept first publication: %v", err)
	}
	conflict := publicationCandidate("chrome", "0.1.0")
	conflict.PackageBytes = 48214
	if _, err := store.AcceptExtensionPublication(context.Background(), Account{Email: "release-pipeline"}, conflict, "test-ip"); !errors.Is(err, ErrExtensionPublicationConflict) {
		t.Fatalf("conflicting publication error = %v, want ErrExtensionPublicationConflict", err)
	}
}

func TestAcceptExtensionPublicationRejectsInvalidCandidates(t *testing.T) {
	store, _ := publicationTestStore(t)
	actor := Account{Email: "release-pipeline"}
	for name, mutate := range map[string]func(*ExtensionPublicationCandidate){
		"unknown store":    func(candidate *ExtensionPublicationCandidate) { candidate.Store = "safari" },
		"bad version":      func(candidate *ExtensionPublicationCandidate) { candidate.Version = "0.1" },
		"bad digest":       func(candidate *ExtensionPublicationCandidate) { candidate.PackageSHA256 = "A" },
		"zero bytes":       func(candidate *ExtensionPublicationCandidate) { candidate.PackageBytes = 0 },
		"path in filename": func(candidate *ExtensionPublicationCandidate) { candidate.Filename = "../escape.zip" },
		"oversized evidence": func(candidate *ExtensionPublicationCandidate) {
			candidate.Evidence = map[string]any{"built": map[string]any{"note": string(make([]byte, 8192))}}
		},
	} {
		candidate := publicationCandidate("chrome", "0.1.0")
		mutate(&candidate)
		if _, err := store.AcceptExtensionPublication(context.Background(), actor, candidate, "test-ip"); err == nil {
			t.Fatalf("%s: accept invalid publication succeeded", name)
		}
	}
}

func TestExtensionPublicationForwardWalk(t *testing.T) {
	store, db := publicationTestStore(t)
	actor := Account{Email: "release-pipeline"}
	publication, err := store.AcceptExtensionPublication(context.Background(), actor, publicationCandidate("chrome", "0.1.0"), "test-ip")
	if err != nil {
		t.Fatalf("accept publication: %v", err)
	}
	steps := []struct {
		to       string
		evidence map[string]any
	}{
		{"uploaded", map[string]any{"itemID": "ext-item-1"}},
		{"submitted", map[string]any{"submissionID": "sub-1"}},
		{"approved", map[string]any{"approvedAt": "2026-09-07"}},
		{"published", map[string]any{"storeURL": "https://store.test.invalid/ext"}},
	}
	revision := publication.StateRevision
	status := publication.Status
	for _, step := range steps {
		next, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, transitionInput(revision, step.to, step.evidence), "test-ip")
		if err != nil {
			t.Fatalf("transition to %s: %v", step.to, err)
		}
		if next.Status != step.to || next.StateRevision != revision+1 {
			t.Fatalf("transition to %s = status %q revision %d", step.to, next.Status, next.StateRevision)
		}
		status, revision = next.Status, next.StateRevision
	}
	if status != "published" {
		t.Fatalf("final status = %q, want published", status)
	}
	withdrawn, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, transitionInput(revision, "withdrawn", map[string]any{"reason": "store removal"}), "test-ip")
	if err != nil || withdrawn.Status != "withdrawn" {
		t.Fatalf("withdraw = %v, %q", err, withdrawn.Status)
	}
	var auditCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_admin_audit_log WHERE target_type = 'extension_publication' AND target_id = $1`, publication.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 6 {
		t.Fatalf("audit rows = %d, want 6", auditCount)
	}
}

func TestExtensionPublicationRejectsImpossibleMoves(t *testing.T) {
	store, _ := publicationTestStore(t)
	actor := Account{Email: "release-pipeline"}
	publication, err := store.AcceptExtensionPublication(context.Background(), actor, publicationCandidate("chrome", "0.1.0"), "test-ip")
	if err != nil {
		t.Fatalf("accept publication: %v", err)
	}
	for _, to := range []string{"built", "submitted", "approved", "published", "withdrawn"} {
		if _, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, transitionInput(1, to, nil), "test-ip"); !errors.Is(err, ErrNotAllowed) {
			t.Fatalf("move built to %s error = %v, want ErrNotAllowed", to, err)
		}
	}
}

func TestExtensionPublicationRejectsStaleRevision(t *testing.T) {
	store, _ := publicationTestStore(t)
	actor := Account{Email: "release-pipeline"}
	publication, err := store.AcceptExtensionPublication(context.Background(), actor, publicationCandidate("chrome", "0.1.0"), "test-ip")
	if err != nil {
		t.Fatalf("accept publication: %v", err)
	}
	if _, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, transitionInput(1, "uploaded", map[string]any{"itemID": "ext-item-1"}), "test-ip"); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	if _, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, transitionInput(1, "submitted", nil), "test-ip"); !errors.Is(err, ErrManifestRevisionConflict) {
		t.Fatalf("stale transition error = %v, want ErrManifestRevisionConflict", err)
	}
}

func TestExtensionPublicationReplayReconcilesWithoutNewAudit(t *testing.T) {
	store, db := publicationTestStore(t)
	actor := Account{Email: "release-pipeline"}
	publication, err := store.AcceptExtensionPublication(context.Background(), actor, publicationCandidate("chrome", "0.1.0"), "test-ip")
	if err != nil {
		t.Fatalf("accept publication: %v", err)
	}
	input := transitionInput(1, "uploaded", map[string]any{"itemID": "ext-item-1"})
	first, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, input, "test-ip")
	if err != nil {
		t.Fatalf("first transition: %v", err)
	}
	replayed, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, input, "test-ip")
	if err != nil {
		t.Fatalf("replayed transition: %v", err)
	}
	if replayed.Status != first.Status || replayed.StateRevision != first.StateRevision {
		t.Fatalf("replay changed state: status %q revision %d, want %q %d", replayed.Status, replayed.StateRevision, first.Status, first.StateRevision)
	}
	var auditCount int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM sesame_admin_audit_log WHERE target_type = 'extension_publication' AND target_id = $1 AND action = 'extension_publication.uploaded'`, publication.ID).Scan(&auditCount); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("uploaded audit rows = %d, want 1", auditCount)
	}
}

func TestExtensionPublicationConflictOnDivergentEvidence(t *testing.T) {
	store, _ := publicationTestStore(t)
	actor := Account{Email: "release-pipeline"}
	publication, err := store.AcceptExtensionPublication(context.Background(), actor, publicationCandidate("chrome", "0.1.0"), "test-ip")
	if err != nil {
		t.Fatalf("accept publication: %v", err)
	}
	if _, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, transitionInput(1, "uploaded", map[string]any{"itemID": "ext-item-1"}), "test-ip"); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	divergent := transitionInput(1, "uploaded", map[string]any{"itemID": "ext-item-other"})
	if _, err := store.TransitionExtensionPublication(context.Background(), actor, publication.ID, divergent, "test-ip"); !errors.Is(err, ErrExtensionPublicationConflict) {
		t.Fatalf("divergent replay error = %v, want ErrExtensionPublicationConflict", err)
	}
}
