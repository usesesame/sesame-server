package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"usesesame.app/backend/internal/releases"
)

const maxExtensionPublicationEvidenceBytes = 4096

// One record per store artifact identity: the digest is the reviewed package,
// so a changed package is a different record, never an update of this one.
var extensionPublicationTransitions = map[string]string{
	"built":     "uploaded",
	"uploaded":  "submitted",
	"submitted": "approved",
	"approved":  "published",
	"published": "withdrawn",
}

func extensionPublicationTarget(state string) bool {
	switch state {
	case "uploaded", "submitted", "approved", "published", "withdrawn":
		return true
	}
	return false
}

func extensionPublicationEvidence(evidence map[string]any) ([]byte, error) {
	if evidence == nil {
		return []byte("{}"), nil
	}
	if len(evidence) > 16 {
		return nil, ErrNotAllowed
	}
	for key := range evidence {
		if key == "" || len(key) > 60 {
			return nil, ErrNotAllowed
		}
	}
	encoded, err := json.Marshal(evidence)
	if err != nil || len(encoded) > maxExtensionPublicationEvidenceBytes {
		return nil, ErrNotAllowed
	}
	return encoded, nil
}

func (s *Store) ExtensionPublications(ctx context.Context) ([]ExtensionPublication, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, store, version, package_sha256, package_bytes, filename, status, evidence, state_revision, created_at, updated_at
		FROM sesame_extension_publications
		ORDER BY created_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	publications := []ExtensionPublication{}
	for rows.Next() {
		publication, err := scanExtensionPublication(rows)
		if err != nil {
			return nil, err
		}
		publications = append(publications, publication)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range publications {
		audit, err := s.extensionPublicationAudit(ctx, publications[index].ID)
		if err != nil {
			return nil, err
		}
		publications[index].Audit = audit
	}
	return publications, nil
}

func scanExtensionPublication(scan interface{ Scan(...any) error }) (ExtensionPublication, error) {
	var publication ExtensionPublication
	var evidence []byte
	if err := scan.Scan(&publication.ID, &publication.Store, &publication.Version, &publication.PackageSHA256, &publication.PackageBytes, &publication.Filename, &publication.Status, &evidence, &publication.StateRevision, &publication.CreatedAt, &publication.UpdatedAt); err != nil {
		return ExtensionPublication{}, err
	}
	publication.Evidence = map[string]any{}
	if len(evidence) > 0 && json.Unmarshal(evidence, &publication.Evidence) != nil {
		return ExtensionPublication{}, errors.New("extension publication evidence is invalid")
	}
	return publication, nil
}

func (s *Store) extensionPublicationAudit(ctx context.Context, publicationID string) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, admin_id, admin_email, action, target_type, target_id, detail, created_at FROM sesame_admin_audit_log WHERE target_type = 'extension_publication' AND target_id = $1 ORDER BY created_at DESC, id DESC LIMIT 8`, publicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	audit := []AuditEntry{}
	for rows.Next() {
		var entry AuditEntry
		var detail []byte
		if err := rows.Scan(&entry.ID, &entry.AdminID, &entry.AdminEmail, &entry.Action, &entry.TargetType, &entry.TargetID, &detail, &entry.CreatedAt); err != nil {
			return nil, err
		}
		if json.Unmarshal(detail, &entry.Detail) != nil {
			return nil, errors.New("extension publication audit detail is invalid")
		}
		audit = append(audit, entry)
	}
	return audit, rows.Err()
}

func (s *Store) AcceptExtensionPublication(ctx context.Context, actor Account, candidate ExtensionPublicationCandidate, ipHash string) (ExtensionPublication, error) {
	if !extensionStoreSupported(candidate.Store) {
		return ExtensionPublication{}, ErrNotAllowed
	}
	if _, err := releases.ParseVersion(candidate.Version); err != nil {
		return ExtensionPublication{}, ErrNotAllowed
	}
	if !sha256Hex(candidate.PackageSHA256) || candidate.PackageBytes < 1 || candidate.PackageBytes > 68574592 {
		return ExtensionPublication{}, ErrNotAllowed
	}
	if candidate.Filename == "" || len(candidate.Filename) > 200 || containsPathSeparator(candidate.Filename) {
		return ExtensionPublication{}, ErrNotAllowed
	}
	builtEvidence, err := extensionPublicationEvidence(candidate.Evidence)
	if err != nil {
		return ExtensionPublication{}, err
	}
	var normalized map[string]any
	if err := json.Unmarshal(builtEvidence, &normalized); err != nil {
		return ExtensionPublication{}, err
	}
	encodedEvidence, err := json.Marshal(map[string]any{"built": normalized})
	if err != nil {
		return ExtensionPublication{}, err
	}
	publicationID, err := newID()
	if err != nil {
		return ExtensionPublication{}, err
	}
	publication := ExtensionPublication{
		ID: publicationID, Store: candidate.Store, Version: candidate.Version,
		PackageSHA256: candidate.PackageSHA256, PackageBytes: candidate.PackageBytes,
		Filename: candidate.Filename, Status: "built", StateRevision: 1,
		Evidence: map[string]any{"built": normalized},
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExtensionPublication{}, err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `INSERT INTO sesame_extension_publications (id, store, version, package_sha256, package_bytes, filename, evidence, status, state_revision)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'built', 1)
		ON CONFLICT (store, version, package_sha256) DO NOTHING
		RETURNING id, store, version, package_sha256, package_bytes, filename, status, evidence, state_revision, created_at, updated_at`,
		publication.ID, publication.Store, publication.Version, publication.PackageSHA256, publication.PackageBytes, publication.Filename, encodedEvidence).
		Scan(&publication.ID, &publication.Store, &publication.Version, &publication.PackageSHA256, &publication.PackageBytes, &publication.Filename, &publication.Status, &encodedEvidence, &publication.StateRevision, &publication.CreatedAt, &publication.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		existing, exactReplay, lookupErr := acceptedExtensionPublication(ctx, tx, candidate)
		if lookupErr != nil {
			return ExtensionPublication{}, lookupErr
		}
		if !exactReplay {
			return ExtensionPublication{}, ErrExtensionPublicationConflict
		}
		if err := tx.Commit(); err != nil {
			return ExtensionPublication{}, err
		}
		return existing, nil
	}
	if err != nil {
		return ExtensionPublication{}, err
	}
	if err := insertAudit(ctx, tx, actor, "extension_publication.built", "extension_publication", publication.ID, map[string]any{"store": publication.Store, "version": publication.Version, "packageSha256": publication.PackageSHA256, "filename": publication.Filename}, ipHash); err != nil {
		return ExtensionPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExtensionPublication{}, err
	}
	return publication, nil
}

func acceptedExtensionPublication(ctx context.Context, tx *sql.Tx, candidate ExtensionPublicationCandidate) (ExtensionPublication, bool, error) {
	rows := tx.QueryRowContext(ctx, `SELECT id, store, version, package_sha256, package_bytes, filename, status, evidence, state_revision, created_at, updated_at
		FROM sesame_extension_publications
		WHERE store = $1 AND version = $2 AND package_sha256 = $3`, candidate.Store, candidate.Version, candidate.PackageSHA256)
	existing, err := scanExtensionPublication(rows)
	if errors.Is(err, sql.ErrNoRows) {
		return ExtensionPublication{}, false, ErrNotFound
	}
	if err != nil {
		return ExtensionPublication{}, false, err
	}
	built, _ := existing.Evidence["built"].(map[string]any)
	exactReplay := existing.Filename == candidate.Filename && existing.PackageBytes == candidate.PackageBytes && sameEvidence(built, candidate.Evidence)
	return existing, exactReplay, nil
}

func sameEvidence(stored, incoming map[string]any) bool {
	if stored == nil {
		stored = map[string]any{}
	}
	if incoming == nil {
		incoming = map[string]any{}
	}
	encodedStored, errStored := json.Marshal(stored)
	encodedIncoming, errIncoming := json.Marshal(incoming)
	return errStored == nil && errIncoming == nil && string(encodedStored) == string(encodedIncoming)
}

func (s *Store) TransitionExtensionPublication(ctx context.Context, actor Account, publicationID string, input ExtensionTransitionInput, ipHash string) (ExtensionPublication, error) {
	if publicationID == "" || input.ExpectedStateRevision < 1 || input.To == "" {
		return ExtensionPublication{}, ErrNotAllowed
	}
	if !extensionPublicationTarget(input.To) {
		return ExtensionPublication{}, ErrNotAllowed
	}
	if _, err := extensionPublicationEvidence(input.Evidence); err != nil {
		return ExtensionPublication{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ExtensionPublication{}, err
	}
	defer tx.Rollback()
	var publication ExtensionPublication
	var evidence []byte
	err = tx.QueryRowContext(ctx, `SELECT id, store, version, package_sha256, package_bytes, filename, status, evidence, state_revision, created_at, updated_at
		FROM sesame_extension_publications WHERE id = $1 FOR UPDATE`, publicationID).
		Scan(&publication.ID, &publication.Store, &publication.Version, &publication.PackageSHA256, &publication.PackageBytes, &publication.Filename, &publication.Status, &evidence, &publication.StateRevision, &publication.CreatedAt, &publication.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ExtensionPublication{}, ErrNotFound
	}
	if err != nil {
		return ExtensionPublication{}, err
	}
	if err := json.Unmarshal(evidence, &publication.Evidence); err != nil {
		return ExtensionPublication{}, errors.New("extension publication evidence is invalid")
	}
	if publication.Status == input.To {
		storedState, _ := publication.Evidence[input.To].(map[string]any)
		if !sameEvidence(storedState, input.Evidence) {
			return ExtensionPublication{}, ErrExtensionPublicationConflict
		}
		if err := tx.Commit(); err != nil {
			return ExtensionPublication{}, err
		}
		return publication, nil
	}
	if publication.StateRevision != input.ExpectedStateRevision {
		return ExtensionPublication{}, ErrManifestRevisionConflict
	}
	if extensionPublicationTransitions[publication.Status] != input.To {
		return ExtensionPublication{}, ErrNotAllowed
	}
	publication.Evidence[input.To] = mergedEvidence(publication.Evidence, input.To, input.Evidence)
	nextRevision := publication.StateRevision + 1
	merged, err := json.Marshal(publication.Evidence)
	if err != nil {
		return ExtensionPublication{}, err
	}
	var updatedAt time.Time
	if err := tx.QueryRowContext(ctx, `UPDATE sesame_extension_publications SET status = $2, evidence = $3, state_revision = $4, updated_at = NOW() WHERE id = $1 AND state_revision = $5 RETURNING updated_at`,
		publicationID, input.To, merged, nextRevision, publication.StateRevision).Scan(&updatedAt); err != nil {
		return ExtensionPublication{}, err
	}
	if err := insertAudit(ctx, tx, actor, fmt.Sprintf("extension_publication.%s", input.To), "extension_publication", publication.ID, map[string]any{"store": publication.Store, "version": publication.Version, "from": publication.Status, "to": input.To}, ipHash); err != nil {
		return ExtensionPublication{}, err
	}
	publication.Status = input.To
	publication.StateRevision = nextRevision
	publication.UpdatedAt = updatedAt
	if err := tx.Commit(); err != nil {
		return ExtensionPublication{}, err
	}
	return publication, nil
}

func mergedEvidence(existing map[string]any, state string, incoming map[string]any) map[string]any {
	merged := map[string]any{}
	for key, value := range existing {
		merged[key] = value
	}
	if incoming == nil {
		merged[state] = map[string]any{}
		return merged
	}
	merged[state] = incoming
	return merged
}

func extensionStoreSupported(store string) bool {
	return store == "chrome" || store == "edge" || store == "firefox"
}

func sha256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func containsPathSeparator(value string) bool {
	for _, char := range value {
		if char == '/' || char == '\\' {
			return true
		}
	}
	return false
}
