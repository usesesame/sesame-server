package admin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"usesesame.app/backend/internal/releases"
)

func (s *Store) Releases(ctx context.Context) ([]Release, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT release.id, release.channel, release.platform, release.architecture, release.version, release.download_url, release.artifact_object_key, release.sha256, release.signature, release.signing_key_id, release.supported_windows, release.release_notes_url, release.rollback_notice, release.status, release.rollout_percent, release.update_enabled, release.kill_switch, release.manifest_revision, release.published_at,
		artifact.id, COALESCE(artifact.artifact_url, ''), COALESCE(artifact.artifact_object_key, ''), COALESCE(artifact.artifact_sha256, ''), COALESCE(artifact.artifact_bytes, 0), COALESCE(artifact.updater_signature, ''), COALESCE(artifact.updater_signing_key_id, ''), COALESCE(artifact.distribution_class, 'lab'), artifact.sigstore_evidence, COALESCE(artifact.sigstore_verified, FALSE), COALESCE(artifact.sigstore_issuer, ''), COALESCE(artifact.sigstore_identity, ''), COALESCE(artifact.sigstore_bundle_sha256, ''), artifact.authenticode_evidence, COALESCE(artifact.authenticode_verified, FALSE), COALESCE(artifact.authenticode_subject, ''), COALESCE(artifact.authenticode_thumbprint, ''), COALESCE(artifact.verified_at, '0001-01-01'::timestamptz), COALESCE(artifact.candidate_payload, ''), COALESCE(artifact.candidate_signing_key_id, ''), COALESCE(artifact.candidate_signature, '')
	FROM sesame_releases release
	LEFT JOIN LATERAL (SELECT * FROM sesame_release_artifacts WHERE release_id = release.id ORDER BY created_at DESC LIMIT 1) artifact ON TRUE
	ORDER BY COALESCE(release.published_at, '0001-01-01'::timestamptz) DESC, release.version DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	releases := []Release{}
	for rows.Next() {
		var release Release
		var artifact ReleaseArtifact
		var artifactID sql.NullString
		var sigstoreEvidence []byte
		var authenticodeEvidence []byte
		if err := rows.Scan(&release.ID, &release.Channel, &release.Platform, &release.Architecture, &release.Version, &release.URL, &release.ArtifactObjectKey, &release.SHA256, &release.Signature, &release.SigningKeyID, &release.SupportedWindows, &release.ReleaseNotesURL, &release.RollbackNotice, &release.Status, &release.RolloutPercent, &release.UpdateEnabled, &release.KillSwitch, &release.ManifestRevision, &release.PublishedAt, &artifactID, &artifact.URL, &artifact.ObjectKey, &artifact.SHA256, &artifact.Bytes, &artifact.UpdaterSignature, &artifact.UpdaterSigningKeyID, &artifact.DistributionClass, &sigstoreEvidence, &artifact.SigstoreVerified, &artifact.SigstoreIssuer, &artifact.SigstoreIdentity, &artifact.SigstoreBundleSHA256, &authenticodeEvidence, &artifact.AuthenticodeVerified, &artifact.AuthenticodeSubject, &artifact.AuthenticodeThumbprint, &artifact.VerifiedAt, &artifact.CandidatePayload, &artifact.CandidateSigningKeyID, &artifact.CandidateSignature); err != nil {
			return nil, err
		}
		if artifactID.Valid {
			artifact.ID = artifactID.String
			if len(sigstoreEvidence) > 0 && json.Unmarshal(sigstoreEvidence, &artifact.SigstoreEvidence) != nil {
				return nil, errors.New("release Sigstore evidence is invalid")
			}
			if len(authenticodeEvidence) > 0 && json.Unmarshal(authenticodeEvidence, &artifact.AuthenticodeEvidence) != nil {
				return nil, errors.New("release Authenticode evidence is invalid")
			}
			release.Artifact = &artifact
		}
		releases = append(releases, release)
	}
	return releases, rows.Err()
}

func (s *Store) UpdateRelease(ctx context.Context, actor Account, release Release, ipHash string) error {
	if release.ID == "" {
		return errors.New("release candidates must be accepted by the release pipeline")
	}
	var publishedAt any
	if release.Status == "published" {
		if release.PublishedAt != nil {
			publishedAt = *release.PublishedAt
		} else {
			publishedAt = time.Now().UTC()
		}
	}
	return s.mutate(ctx, actor, "release.update", "release", release.ID, ipHash, map[string]any{"version": release.Version, "status": release.Status, "rolloutPercent": release.RolloutPercent, "killSwitch": release.KillSwitch}, func(tx *sql.Tx) error {
		if release.Status == "published" {
			var exists bool
			if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM sesame_release_artifacts WHERE release_id = $1 AND sigstore_verified = TRUE AND (distribution_class = 'early_access' OR (distribution_class = 'production' AND authenticode_verified = TRUE)))`, release.ID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				return errors.New("a Sigstore-verified early-access artifact or Authenticode-verified production artifact is required before publishing")
			}
		}
		return affected(tx.ExecContext(ctx, `UPDATE sesame_releases SET supported_windows = $2, release_notes_url = $3, rollback_notice = $4, status = $5, rollout_percent = $6, update_enabled = $7, kill_switch = $8, published_at = $9 WHERE id = $1`, release.ID, release.SupportedWindows, release.ReleaseNotesURL, release.RollbackNotice, release.Status, release.RolloutPercent, release.UpdateEnabled, release.KillSwitch, publishedAt))
	})
}

func (s *Store) AcceptReleaseCandidate(ctx context.Context, actor Account, candidate ReleaseCandidate, ipHash string) (Release, error) {
	if candidate.SigningPayload == "" {
		return Release{}, errors.New("release candidate signing payload is required")
	}
	releaseID, err := newID()
	if err != nil {
		return Release{}, err
	}
	artifactID, err := newID()
	if err != nil {
		return Release{}, err
	}
	sigstoreEvidence, err := json.Marshal(candidate.Artifact.SigstoreEvidence)
	if err != nil {
		return Release{}, err
	}
	authenticodeEvidence, err := json.Marshal(candidate.Artifact.AuthenticodeEvidence)
	if err != nil {
		return Release{}, err
	}
	release := Release{ID: releaseID, Channel: candidate.Channel, Platform: candidate.Platform, Architecture: candidate.Architecture, Version: candidate.Version, URL: candidate.Artifact.URL, ArtifactObjectKey: candidate.Artifact.ObjectKey, SHA256: candidate.Artifact.SHA256, Signature: candidate.Artifact.UpdaterSignature, SigningKeyID: candidate.Artifact.UpdaterSigningKeyID, SupportedWindows: candidate.SupportedWindows, ReleaseNotesURL: candidate.ReleaseNotesURL, Status: "draft", RolloutPercent: 100, UpdateEnabled: true}
	err = s.mutate(ctx, actor, "release.candidate.accept", "release", releaseID, ipHash, map[string]any{"version": candidate.Version, "channel": candidate.Channel, "sha256": candidate.Artifact.SHA256, "candidateSigningKeyId": candidate.CandidateSigningKeyID}, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO sesame_releases (id, channel, platform, architecture, version, download_url, artifact_object_key, sha256, signature, signing_key_id, supported_windows, release_notes_url, rollback_notice, status, rollout_percent, update_enabled, kill_switch) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,'','draft',100,TRUE,FALSE)`, release.ID, release.Channel, release.Platform, release.Architecture, release.Version, release.URL, release.ArtifactObjectKey, release.SHA256, release.Signature, release.SigningKeyID, release.SupportedWindows, release.ReleaseNotesURL); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `INSERT INTO sesame_release_artifacts (id, release_id, artifact_url, artifact_object_key, artifact_sha256, artifact_bytes, updater_signature, updater_signing_key_id, distribution_class, sigstore_evidence, sigstore_verified, sigstore_issuer, sigstore_identity, sigstore_bundle_sha256, authenticode_evidence, authenticode_verified, authenticode_subject, authenticode_thumbprint, candidate_payload, candidate_signing_key_id, candidate_signature) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21)`, artifactID, release.ID, candidate.Artifact.URL, candidate.Artifact.ObjectKey, candidate.Artifact.SHA256, candidate.Artifact.Bytes, candidate.Artifact.UpdaterSignature, candidate.Artifact.UpdaterSigningKeyID, candidate.Artifact.DistributionClass, sigstoreEvidence, candidate.Artifact.SigstoreVerified, candidate.Artifact.SigstoreIssuer, candidate.Artifact.SigstoreIdentity, candidate.Artifact.SigstoreBundleSHA256, authenticodeEvidence, candidate.Artifact.AuthenticodeVerified, candidate.Artifact.AuthenticodeSubject, candidate.Artifact.AuthenticodeThumbprint, candidate.SigningPayload, candidate.CandidateSigningKeyID, candidate.CandidateSignature)
		return err
	})
	if err != nil {
		return Release{}, err
	}
	return release, nil
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
		artifact.id, artifact.artifact_object_key, artifact.artifact_sha256, artifact.artifact_bytes, artifact.updater_signature, artifact.updater_signing_key_id, artifact.distribution_class, artifact.sigstore_verified, artifact.sigstore_identity, artifact.authenticode_verified, artifact.candidate_payload, artifact.candidate_signing_key_id, artifact.candidate_signature
		FROM sesame_releases release
		JOIN LATERAL (SELECT * FROM sesame_release_artifacts WHERE release_id = release.id AND sigstore_verified = TRUE AND (distribution_class = 'early_access' OR (distribution_class = 'production' AND authenticode_verified = TRUE))` + receiptFilter + ` ORDER BY created_at DESC LIMIT 1) artifact ON TRUE
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
		if err := rows.Scan(&release.ID, &release.Channel, &release.Platform, &release.Architecture, &release.Version, &release.URL, &release.ArtifactObjectKey, &release.SHA256, &release.Signature, &release.SigningKeyID, &release.SupportedWindows, &release.ReleaseNotesURL, &release.RollbackNotice, &release.Status, &release.RolloutPercent, &release.UpdateEnabled, &release.KillSwitch, &release.ManifestRevision, &release.PublishedAt, &artifact.ID, &artifact.ObjectKey, &artifact.SHA256, &artifact.Bytes, &artifact.UpdaterSignature, &artifact.UpdaterSigningKeyID, &artifact.DistributionClass, &artifact.SigstoreVerified, &artifact.SigstoreIdentity, &artifact.AuthenticodeVerified, &artifact.CandidatePayload, &artifact.CandidateSigningKeyID, &artifact.CandidateSignature); err != nil {
			return nil, err
		}
		release.Artifact = &artifact
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
