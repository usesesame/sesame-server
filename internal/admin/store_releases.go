package admin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"sort"

	"usesesame.app/backend/internal/releases"
)

func (s *Store) Releases(ctx context.Context) ([]Release, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT release.id, release.channel, release.platform, release.architecture, release.version, release.download_url, release.artifact_object_key, release.sha256, release.signature, release.signing_key_id, release.supported_windows, release.release_notes_url, release.rollback_notice, release.status, release.rollout_percent, release.update_enabled, release.kill_switch, release.manifest_revision, release.published_at, release.release_set_digest, release.release_set_verified_at
	FROM sesame_releases release
	ORDER BY COALESCE(release.published_at, '0001-01-01'::timestamptz) DESC, release.version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	releases := []Release{}
	for rows.Next() {
		var release Release
		if err := rows.Scan(&release.ID, &release.Channel, &release.Platform, &release.Architecture, &release.Version, &release.URL, &release.ArtifactObjectKey, &release.SHA256, &release.Signature, &release.SigningKeyID, &release.SupportedWindows, &release.ReleaseNotesURL, &release.RollbackNotice, &release.Status, &release.RolloutPercent, &release.UpdateEnabled, &release.KillSwitch, &release.ManifestRevision, &release.PublishedAt, &release.ReleaseSetDigest, &release.ReleaseSetVerifiedAt); err != nil {
			return nil, err
		}
		releases = append(releases, release)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range releases {
		artifacts, allEligible, err := s.releaseArtifacts(ctx, releases[index].ID)
		if err != nil {
			return nil, err
		}
		releases[index].Artifacts = artifacts
		for artifactIndex := range artifacts {
			if artifacts[artifactIndex].UpdaterCapable {
				releases[index].Artifact = &artifacts[artifactIndex]
				break
			}
		}
		if releases[index].Artifact == nil && len(artifacts) > 0 {
			releases[index].Artifact = &artifacts[0]
		}
		releases[index].PublicationBlockers = publicationBlockers(releases[index], allEligible)
		audit, err := s.releaseAudit(ctx, releases[index].ID)
		if err != nil {
			return nil, err
		}
		releases[index].Audit = audit
	}
	return releases, nil
}

func (s *Store) releaseArtifacts(ctx context.Context, releaseID string) ([]ReleaseArtifact, bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, format, architecture, artifact_url, artifact_object_key, artifact_sha256, artifact_bytes, updater_capable, updater_signature, updater_signing_key_id, distribution_class, sigstore_evidence, sigstore_verified, sigstore_issuer, sigstore_identity, sigstore_bundle_sha256, authenticode_evidence, authenticode_verified, authenticode_subject, authenticode_thumbprint, verified_at, candidate_payload, candidate_signing_key_id, candidate_signature, eligible_for_distribution
		FROM sesame_release_artifacts WHERE release_id = $1 ORDER BY format, architecture`, releaseID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	artifacts := []ReleaseArtifact{}
	allEligible := true
	for rows.Next() {
		var artifact ReleaseArtifact
		var sigstoreEvidence, authenticodeEvidence []byte
		var eligible bool
		if err := rows.Scan(&artifact.ID, &artifact.Format, &artifact.Architecture, &artifact.URL, &artifact.ObjectKey, &artifact.SHA256, &artifact.Bytes, &artifact.UpdaterCapable, &artifact.UpdaterSignature, &artifact.UpdaterSigningKeyID, &artifact.DistributionClass, &sigstoreEvidence, &artifact.SigstoreVerified, &artifact.SigstoreIssuer, &artifact.SigstoreIdentity, &artifact.SigstoreBundleSHA256, &authenticodeEvidence, &artifact.AuthenticodeVerified, &artifact.AuthenticodeSubject, &artifact.AuthenticodeThumbprint, &artifact.VerifiedAt, &artifact.CandidatePayload, &artifact.CandidateSigningKeyID, &artifact.CandidateSignature, &eligible); err != nil {
			return nil, false, err
		}
		if len(sigstoreEvidence) > 0 && json.Unmarshal(sigstoreEvidence, &artifact.SigstoreEvidence) != nil {
			return nil, false, errors.New("release Sigstore evidence is invalid")
		}
		if len(authenticodeEvidence) > 0 && json.Unmarshal(authenticodeEvidence, &artifact.AuthenticodeEvidence) != nil {
			return nil, false, errors.New("release Authenticode evidence is invalid")
		}
		allEligible = allEligible && eligible
		artifacts = append(artifacts, artifact)
	}
	return artifacts, len(artifacts) > 0 && allEligible, rows.Err()
}

func (s *Store) releaseAudit(ctx context.Context, releaseID string) ([]AuditEntry, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, admin_id, admin_email, action, target_type, target_id, detail, created_at FROM sesame_admin_audit_log WHERE target_type = 'release' AND target_id = $1 ORDER BY created_at DESC, id DESC LIMIT 8`, releaseID)
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
			return nil, errors.New("release audit detail is invalid")
		}
		audit = append(audit, entry)
	}
	return audit, rows.Err()
}

func (s *Store) PublishRelease(ctx context.Context, actor Account, releaseID string, input PublishReleaseInput, ipHash string) error {
	return s.applyReleaseCommand(ctx, actor, releaseID, input.ExpectedManifestRevision, "publish", 0, ipHash)
}

func (s *Store) SetReleaseRollout(ctx context.Context, actor Account, releaseID string, input RolloutReleaseInput, ipHash string) error {
	if input.RolloutPercent < 0 || input.RolloutPercent > 100 {
		return ErrNotAllowed
	}
	return s.applyReleaseCommand(ctx, actor, releaseID, input.ExpectedManifestRevision, "rollout", input.RolloutPercent, ipHash)
}

func (s *Store) EmergencyStopRelease(ctx context.Context, actor Account, releaseID string, input EmergencyStopReleaseInput, ipHash string) error {
	return s.applyReleaseCommand(ctx, actor, releaseID, input.ExpectedManifestRevision, "emergency_stop", 0, ipHash)
}

func (s *Store) WithdrawRelease(ctx context.Context, actor Account, releaseID string, input WithdrawReleaseInput, ipHash string) error {
	return s.applyReleaseCommand(ctx, actor, releaseID, input.ExpectedManifestRevision, "withdraw", 0, ipHash)
}

func (s *Store) applyReleaseCommand(ctx context.Context, actor Account, releaseID string, expectedRevision int64, command string, rolloutPercent int, ipHash string) error {
	if releaseID == "" || expectedRevision < 1 {
		return ErrNotAllowed
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var status string
	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT status, manifest_revision FROM sesame_releases WHERE id = $1 FOR UPDATE`, releaseID).Scan(&status, &revision); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return err
	}
	if revision != expectedRevision {
		return ErrManifestRevisionConflict
	}
	var action string
	var detail map[string]any
	switch command {
	case "publish":
		eligible, eligibilityErr := releaseEligible(ctx, tx, releaseID)
		if eligibilityErr != nil {
			return eligibilityErr
		}
		if status != "draft" || !eligible {
			return ErrNotAllowed
		}
		action, detail = "release.publish", map[string]any{"expectedManifestRevision": expectedRevision}
		_, err = tx.ExecContext(ctx, `UPDATE sesame_releases SET status = 'published', published_at = NOW(), update_enabled = TRUE, kill_switch = FALSE WHERE id = $1 AND manifest_revision = $2`, releaseID, expectedRevision)
	case "rollout":
		if status != "published" {
			return ErrNotAllowed
		}
		action, detail = "release.rollout", map[string]any{"expectedManifestRevision": expectedRevision, "rolloutPercent": rolloutPercent}
		_, err = tx.ExecContext(ctx, `UPDATE sesame_releases SET rollout_percent = $3 WHERE id = $1 AND manifest_revision = $2`, releaseID, expectedRevision, rolloutPercent)
	case "emergency_stop":
		if status != "published" {
			return ErrNotAllowed
		}
		action, detail = "release.emergency_stop", map[string]any{"expectedManifestRevision": expectedRevision}
		_, err = tx.ExecContext(ctx, `UPDATE sesame_releases SET kill_switch = TRUE, update_enabled = FALSE WHERE id = $1 AND manifest_revision = $2`, releaseID, expectedRevision)
	case "withdraw":
		if status != "published" {
			return ErrNotAllowed
		}
		action, detail = "release.withdraw", map[string]any{"expectedManifestRevision": expectedRevision}
		_, err = tx.ExecContext(ctx, `UPDATE sesame_releases SET status = 'withdrawn', update_enabled = FALSE, kill_switch = TRUE WHERE id = $1 AND manifest_revision = $2`, releaseID, expectedRevision)
	default:
		return ErrNotAllowed
	}
	if err != nil {
		return err
	}
	if err := insertAudit(ctx, tx, actor, action, "release", releaseID, detail, ipHash); err != nil {
		return err
	}
	return tx.Commit()
}

func releaseEligible(ctx context.Context, tx *sql.Tx, releaseID string) (bool, error) {
	var eligible bool
	if err := tx.QueryRowContext(ctx, `SELECT release.release_set_verified_at IS NOT NULL
		AND release.release_set_digest ~ '^[0-9a-f]{64}$'
		AND COUNT(artifact.id) = CASE release.platform WHEN 'windows' THEN 1 WHEN 'linux' THEN 3 ELSE 0 END
		AND COALESCE(BOOL_AND(artifact.eligible_for_distribution), FALSE)
		FROM sesame_releases release
		LEFT JOIN sesame_release_artifacts artifact ON artifact.release_id = release.id
		WHERE release.id = $1
		GROUP BY release.id`, releaseID).Scan(&eligible); err != nil {
		return false, err
	}
	return eligible, nil
}

func publicationBlockers(release Release, artifactEligible bool) []string {
	if release.Status != "draft" {
		return []string{}
	}
	if release.ReleaseSetVerifiedAt == nil || release.ReleaseSetDigest == "" {
		return []string{"verified release set"}
	}
	expectedCount := 1
	if release.Platform == "linux" {
		expectedCount = 3
	}
	if !artifactEligible || len(release.Artifacts) != expectedCount {
		return []string{"complete eligible artifact set"}
	}
	return []string{}
}

func (s *Store) AcceptReleaseCandidate(ctx context.Context, actor Account, candidate ReleaseCandidate, ipHash string) (Release, error) {
	if candidate.SigningPayload == "" {
		return Release{}, errors.New("release candidate signing payload is required")
	}
	if len(candidate.Artifacts) == 0 {
		return Release{}, errors.New("release candidate artifacts are required")
	}
	identity := releaseCandidateIdentity(candidate.SigningPayload, candidate.SetDigest)
	primary := primaryReleaseArtifact(candidate.Artifacts)
	releaseID, err := newID()
	if err != nil {
		return Release{}, err
	}
	release := Release{ID: releaseID, Channel: candidate.Channel, Platform: candidate.Platform, Architecture: candidate.Architecture, Version: candidate.Version, URL: primary.URL, ArtifactObjectKey: primary.ObjectKey, SHA256: primary.SHA256, Signature: primary.UpdaterSignature, SigningKeyID: primary.UpdaterSigningKeyID, SupportedWindows: candidate.SupportedWindows, ReleaseNotesURL: candidate.ReleaseNotesURL, Status: "draft", RolloutPercent: 100, UpdateEnabled: true, ReleaseSetDigest: candidate.SetDigest, Artifacts: candidate.Artifacts}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Release{}, err
	}
	defer tx.Rollback()

	err = tx.QueryRowContext(ctx, `INSERT INTO sesame_releases (id, channel, platform, architecture, version, download_url, artifact_object_key, sha256, signature, signing_key_id, supported_windows, release_notes_url, rollback_notice, status, rollout_percent, update_enabled, kill_switch, release_set_digest, release_set_verified_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'','draft',100,TRUE,FALSE,$13,NOW()) ON CONFLICT (channel, platform, architecture, version) DO NOTHING RETURNING id, release_set_verified_at`, release.ID, release.Channel, release.Platform, release.Architecture, release.Version, release.URL, release.ArtifactObjectKey, release.SHA256, release.Signature, release.SigningKeyID, release.SupportedWindows, release.ReleaseNotesURL, release.ReleaseSetDigest).Scan(&release.ID, &release.ReleaseSetVerifiedAt)
	if errors.Is(err, sql.ErrNoRows) {
		existing, exactReplay, lookupErr := acceptedReleaseCandidate(ctx, tx, candidate, identity)
		if lookupErr != nil {
			return Release{}, lookupErr
		}
		if !exactReplay {
			return Release{}, ErrReleaseCandidateConflict
		}
		if err := tx.Commit(); err != nil {
			return Release{}, err
		}
		return existing, nil
	}
	if err != nil {
		return Release{}, err
	}
	for _, artifact := range candidate.Artifacts {
		artifactID, err := newID()
		if err != nil {
			return Release{}, err
		}
		sigstoreEvidence, err := json.Marshal(artifact.SigstoreEvidence)
		if err != nil {
			return Release{}, err
		}
		authenticodeEvidence, err := json.Marshal(artifact.AuthenticodeEvidence)
		if err != nil {
			return Release{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_release_artifacts (id, release_id, format, architecture, artifact_url, artifact_object_key, artifact_sha256, artifact_bytes, updater_capable, updater_signature, updater_signing_key_id, distribution_class, sigstore_evidence, sigstore_verified, sigstore_issuer, sigstore_identity, sigstore_bundle_sha256, authenticode_evidence, authenticode_verified, authenticode_subject, authenticode_thumbprint, candidate_payload, candidate_signing_key_id, candidate_signature, candidate_identity) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)`, artifactID, release.ID, artifact.Format, artifact.Architecture, artifact.URL, artifact.ObjectKey, artifact.SHA256, artifact.Bytes, artifact.UpdaterCapable, artifact.UpdaterSignature, artifact.UpdaterSigningKeyID, artifact.DistributionClass, sigstoreEvidence, artifact.SigstoreVerified, artifact.SigstoreIssuer, artifact.SigstoreIdentity, artifact.SigstoreBundleSHA256, authenticodeEvidence, artifact.AuthenticodeVerified, artifact.AuthenticodeSubject, artifact.AuthenticodeThumbprint, candidate.SigningPayload, candidate.CandidateSigningKeyID, candidate.CandidateSignature, identity); err != nil {
			return Release{}, err
		}
	}
	if err := insertAudit(ctx, tx, actor, "release.candidate.accept", "release", release.ID, map[string]any{"version": candidate.Version, "channel": candidate.Channel, "releaseSetDigest": candidate.SetDigest, "artifactCount": len(candidate.Artifacts), "candidateSigningKeyId": candidate.CandidateSigningKeyID}, ipHash); err != nil {
		return Release{}, err
	}
	if err := tx.Commit(); err != nil {
		return Release{}, err
	}
	return release, nil
}

func primaryReleaseArtifact(artifacts []ReleaseArtifact) ReleaseArtifact {
	for _, artifact := range artifacts {
		if artifact.UpdaterCapable {
			return artifact
		}
	}
	for _, format := range []string{"appimage", "deb", "rpm"} {
		for _, artifact := range artifacts {
			if artifact.Format == format {
				return artifact
			}
		}
	}
	return artifacts[0]
}

func releaseCandidateIdentity(payload, setDigest string) string {
	sum := sha256.Sum256([]byte(payload + "\n" + setDigest))
	return hex.EncodeToString(sum[:])
}

func acceptedReleaseCandidate(ctx context.Context, tx *sql.Tx, candidate ReleaseCandidate, identity string) (Release, bool, error) {
	var release Release
	var candidateIdentity, payload, keyID, signature string
	err := tx.QueryRowContext(ctx, `SELECT release.id, release.channel, release.platform, release.architecture, release.version, release.download_url, release.artifact_object_key, release.sha256, release.signature, release.signing_key_id, release.supported_windows, release.release_notes_url, release.status, release.rollout_percent, release.update_enabled, release.kill_switch, release.manifest_revision, release.release_set_digest, release.release_set_verified_at, COALESCE(artifact.candidate_identity, ''), COALESCE(artifact.candidate_payload, ''), COALESCE(artifact.candidate_signing_key_id, ''), COALESCE(artifact.candidate_signature, '')
		FROM sesame_releases release
		LEFT JOIN LATERAL (SELECT * FROM sesame_release_artifacts WHERE release_id = release.id ORDER BY format, architecture LIMIT 1) artifact ON TRUE
		WHERE release.channel = $1 AND release.platform = $2 AND release.architecture = $3 AND release.version = $4`, candidate.Channel, candidate.Platform, candidate.Architecture, candidate.Version).Scan(&release.ID, &release.Channel, &release.Platform, &release.Architecture, &release.Version, &release.URL, &release.ArtifactObjectKey, &release.SHA256, &release.Signature, &release.SigningKeyID, &release.SupportedWindows, &release.ReleaseNotesURL, &release.Status, &release.RolloutPercent, &release.UpdateEnabled, &release.KillSwitch, &release.ManifestRevision, &release.ReleaseSetDigest, &release.ReleaseSetVerifiedAt, &candidateIdentity, &payload, &keyID, &signature)
	if errors.Is(err, sql.ErrNoRows) {
		return Release{}, false, ErrNotFound
	}
	if err != nil {
		return Release{}, false, err
	}
	artifacts, artifactsMatch, err := acceptedReleaseArtifacts(ctx, tx, release.ID, candidate.Artifacts)
	if err != nil {
		return Release{}, false, err
	}
	release.Artifacts = artifacts
	exactReplay := candidateIdentity == identity && release.ReleaseSetDigest == candidate.SetDigest && payload == candidate.SigningPayload && keyID == candidate.CandidateSigningKeyID && signature == candidate.CandidateSignature && artifactsMatch
	return release, exactReplay, nil
}

func acceptedReleaseArtifacts(ctx context.Context, tx *sql.Tx, releaseID string, expected []ReleaseArtifact) ([]ReleaseArtifact, bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT format, architecture, artifact_url, artifact_object_key, artifact_sha256, artifact_bytes, updater_capable, updater_signature, updater_signing_key_id, distribution_class, sigstore_evidence, sigstore_verified, sigstore_issuer, sigstore_identity, sigstore_bundle_sha256, authenticode_evidence, authenticode_verified, authenticode_subject, authenticode_thumbprint FROM sesame_release_artifacts WHERE release_id = $1 ORDER BY format, architecture`, releaseID)
	if err != nil {
		return nil, false, err
	}
	defer rows.Close()
	actual := []ReleaseArtifact{}
	for rows.Next() {
		var artifact ReleaseArtifact
		var sigstoreEvidence, authenticodeEvidence []byte
		if err := rows.Scan(&artifact.Format, &artifact.Architecture, &artifact.URL, &artifact.ObjectKey, &artifact.SHA256, &artifact.Bytes, &artifact.UpdaterCapable, &artifact.UpdaterSignature, &artifact.UpdaterSigningKeyID, &artifact.DistributionClass, &sigstoreEvidence, &artifact.SigstoreVerified, &artifact.SigstoreIssuer, &artifact.SigstoreIdentity, &artifact.SigstoreBundleSHA256, &authenticodeEvidence, &artifact.AuthenticodeVerified, &artifact.AuthenticodeSubject, &artifact.AuthenticodeThumbprint); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(sigstoreEvidence, &artifact.SigstoreEvidence); err != nil {
			return nil, false, err
		}
		if len(authenticodeEvidence) > 0 && string(authenticodeEvidence) != "null" {
			if err := json.Unmarshal(authenticodeEvidence, &artifact.AuthenticodeEvidence); err != nil {
				return nil, false, err
			}
		}
		actual = append(actual, artifact)
	}
	if err := rows.Err(); err != nil {
		return nil, false, err
	}
	want := append([]ReleaseArtifact(nil), expected...)
	sort.Slice(want, func(i, j int) bool {
		return want[i].Format+":"+want[i].Architecture < want[j].Format+":"+want[j].Architecture
	})
	return actual, reflect.DeepEqual(actual, want), nil
}

func (s *Store) LatestPublishedRelease(ctx context.Context, platform string) (Release, error) {
	return s.highestPublishedRelease(ctx, `platform = $1`, []any{platform})
}

func (s *Store) LatestPublishedReleaseForChannel(ctx context.Context, platform, architecture, channel string) (Release, error) {
	return s.highestPublishedRelease(ctx, `platform = $1 AND architecture = $2 AND channel = $3`, []any{platform, architecture, channel})
}

func (s *Store) PublishedReleasesForUpdate(ctx context.Context, platform, architecture string, includeOwner bool) ([]Release, error) {
	where := `platform = $1 AND architecture = $2 AND channel = 'beta'`
	args := []any{platform, architecture}
	if includeOwner {
		where = `platform = $1 AND architecture = $2 AND channel IN ('beta', 'owner')`
	}
	return s.publishedReleasesWithArtifact(ctx, where, args, true)
}

func (s *Store) highestPublishedRelease(ctx context.Context, where string, args []any) (Release, error) {
	candidates, err := s.publishedReleases(ctx, where, args)
	if err != nil {
		return Release{}, err
	}
	var selected Release
	var selectedVersion releases.Version
	found := false
	for _, candidate := range candidates {
		version, err := releases.ParseVersion(candidate.Version)
		if err != nil {
			continue
		}
		if !found || version.Compare(selectedVersion) > 0 {
			selected, selectedVersion, found = candidate, version, true
		}
	}
	if !found {
		return Release{}, ErrNotFound
	}
	return selected, nil
}

func (s *Store) publishedReleases(ctx context.Context, where string, args []any) ([]Release, error) {
	return s.publishedReleasesWithArtifact(ctx, where, args, false)
}

func (s *Store) publishedReleasesWithArtifact(ctx context.Context, where string, args []any, requireCandidateReceipt bool) ([]Release, error) {
	receiptFilter := ""
	if requireCandidateReceipt {
		receiptFilter = " AND candidate_payload <> '' AND candidate_signing_key_id <> '' AND candidate_signature <> ''"
	}
	query := `SELECT release.id, release.channel, release.platform, release.architecture, release.version, release.download_url, release.artifact_object_key, release.sha256, release.signature, release.signing_key_id, release.supported_windows, release.release_notes_url, release.rollback_notice, release.status, release.rollout_percent, release.update_enabled, release.kill_switch, release.manifest_revision, release.published_at,
		artifact.id, artifact.format, artifact.architecture, artifact.artifact_object_key, artifact.artifact_sha256, artifact.artifact_bytes, artifact.updater_capable, artifact.updater_signature, artifact.updater_signing_key_id, artifact.distribution_class, artifact.sigstore_verified, artifact.sigstore_identity, artifact.authenticode_verified, artifact.candidate_payload, artifact.candidate_signing_key_id, artifact.candidate_signature
		FROM sesame_releases release
		JOIN LATERAL (SELECT * FROM sesame_release_artifacts WHERE release_id = release.id AND eligible_for_distribution AND updater_capable` + receiptFilter + ` ORDER BY format, architecture LIMIT 1) artifact ON TRUE
		WHERE ` + where + ` AND release.status = 'published' AND release.published_at IS NOT NULL AND release.update_enabled = TRUE AND release.kill_switch = FALSE`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]Release, 0)
	for rows.Next() {
		var release Release
		var artifact ReleaseArtifact
		if err := rows.Scan(&release.ID, &release.Channel, &release.Platform, &release.Architecture, &release.Version, &release.URL, &release.ArtifactObjectKey, &release.SHA256, &release.Signature, &release.SigningKeyID, &release.SupportedWindows, &release.ReleaseNotesURL, &release.RollbackNotice, &release.Status, &release.RolloutPercent, &release.UpdateEnabled, &release.KillSwitch, &release.ManifestRevision, &release.PublishedAt, &artifact.ID, &artifact.Format, &artifact.Architecture, &artifact.ObjectKey, &artifact.SHA256, &artifact.Bytes, &artifact.UpdaterCapable, &artifact.UpdaterSignature, &artifact.UpdaterSigningKeyID, &artifact.DistributionClass, &artifact.SigstoreVerified, &artifact.SigstoreIdentity, &artifact.AuthenticodeVerified, &artifact.CandidatePayload, &artifact.CandidateSigningKeyID, &artifact.CandidateSignature); err != nil {
			return nil, err
		}
		release.Artifact = &artifact
		release.Artifacts = []ReleaseArtifact{artifact}
		if release.ArtifactObjectKey != "" && release.ArtifactObjectKey == artifact.ObjectKey && release.SHA256 == artifact.SHA256 && release.Signature == artifact.UpdaterSignature && release.SigningKeyID == artifact.UpdaterSigningKeyID {
			result = append(result, release)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Store) IsOwnerReleaseRingMember(ctx context.Context, accountID string) (bool, error) {
	var exists bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sesame_release_ring_members WHERE account_id = $1 AND channel = 'owner')`, accountID).Scan(&exists)
	return exists, err
}

// Only verified, eligible, non-suspended accounts may receive owner builds.
func (s *Store) SetOwnerReleaseRingMember(ctx context.Context, actor Account, accountID string, enabled bool, ipHash string) error {
	return s.mutate(ctx, actor, "release.owner_ring.update", "account", accountID, ipHash, map[string]any{"enabled": enabled, "channel": "owner"}, func(tx *sql.Tx) error {
		if enabled {
			var eligible bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sesame_accounts WHERE id = $1 AND email_verified_at IS NOT NULL AND beta_granted_at IS NOT NULL AND suspended_at IS NULL)`, accountID).Scan(&eligible); err != nil {
				return err
			}
			if !eligible {
				return ErrNotAllowed
			}
			_, err := tx.ExecContext(ctx, `INSERT INTO sesame_release_ring_members (account_id, channel) VALUES ($1, 'owner') ON CONFLICT (account_id) DO NOTHING`, accountID)
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM sesame_release_ring_members WHERE account_id = $1 AND channel = 'owner'`, accountID)
		return err
	})
}
