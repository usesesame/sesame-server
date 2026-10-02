package admin

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	auditCheckpointVersion    = "sesame-admin-audit-checkpoint-v1"
	auditCheckpointTimeFormat = "2006-01-02T15:04:05.000000Z"
)

var auditChainGenesisHash = make([]byte, sha256.Size)

type AuditChainBreak struct {
	Seq    int64
	ID     int64
	Reason string
}

type AuditChainReport struct {
	Verified   bool
	Rows       int64
	HeadSeq    int64
	HeadID     int64
	HeadHash   []byte
	FirstBreak *AuditChainBreak
}

type AuditCheckpoint struct {
	ID        int64
	CoverSeq  int64
	ChainHash []byte
	KeyID     string
	SignedAt  time.Time
	Signature []byte
}

type AuditCheckpointReport struct {
	Verified    bool
	Checkpoints int64
	Latest      *AuditCheckpoint
	FirstBreak  string
}

func auditCanonicalRow(alias string) string {
	return fmt.Sprintf(`jsonb_build_array(
	%[1]s.chain_seq, %[1]s.id, %[1]s.admin_id, %[1]s.admin_email, %[1]s.action, %[1]s.target_type,
	%[1]s.target_id, %[1]s.detail, %[1]s.ip_hash,
	to_char(%[1]s.created_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"')
)::text`, alias)
}

func VerifyAuditChain(ctx context.Context, db *sql.DB) (AuditChainReport, error) {
	rows, err := db.QueryContext(ctx, fmt.Sprintf(`
		SELECT audit.chain_seq, audit.id, audit.prev_hash, audit.hash, %s
		FROM sesame_admin_audit_log audit
		ORDER BY audit.chain_seq`, auditCanonicalRow("audit")))
	if err != nil {
		return AuditChainReport{}, fmt.Errorf("read audit chain: %w", err)
	}
	defer rows.Close()
	var report AuditChainReport
	expectedPrev := auditChainGenesisHash
	for rows.Next() {
		var seq, id int64
		var prevHash, rowHash []byte
		var canonical string
		if err := rows.Scan(&seq, &id, &prevHash, &rowHash, &canonical); err != nil {
			return AuditChainReport{}, fmt.Errorf("read audit chain row: %w", err)
		}
		report.Rows++
		if len(prevHash) != sha256.Size || len(rowHash) != sha256.Size {
			report.FirstBreak = &AuditChainBreak{Seq: seq, ID: id, Reason: "audit row has no chain hash"}
			return report, nil
		}
		if !bytes.Equal(prevHash, expectedPrev) {
			report.FirstBreak = &AuditChainBreak{Seq: seq, ID: id, Reason: "audit row does not link to the previous row"}
			return report, nil
		}
		digest := sha256.New()
		digest.Write(expectedPrev)
		digest.Write([]byte(canonical))
		if !bytes.Equal(rowHash, digest.Sum(nil)) {
			report.FirstBreak = &AuditChainBreak{Seq: seq, ID: id, Reason: "audit row hash does not match its contents"}
			return report, nil
		}
		report.HeadSeq = seq
		report.HeadID = id
		report.HeadHash = rowHash
		expectedPrev = rowHash
	}
	if err := rows.Err(); err != nil {
		return AuditChainReport{}, fmt.Errorf("read audit chain: %w", err)
	}
	report.Verified = true
	return report, nil
}

type auditCheckpointPayload struct {
	Version   string `json:"version"`
	CoverSeq  int64  `json:"coverSeq"`
	ChainHash string `json:"chainHash"`
	KeyID     string `json:"keyId"`
	SignedAt  string `json:"signedAt"`
}

func auditCheckpointMessage(coverSeq int64, chainHash []byte, keyID string, signedAt time.Time) ([]byte, error) {
	return json.Marshal(auditCheckpointPayload{
		Version:   auditCheckpointVersion,
		CoverSeq:  coverSeq,
		ChainHash: hex.EncodeToString(chainHash),
		KeyID:     keyID,
		SignedAt:  signedAt.UTC().Format(auditCheckpointTimeFormat),
	})
}

func CheckpointAuditChain(ctx context.Context, db *sql.DB, signingKey ed25519.PrivateKey, keyID string) (*AuditCheckpoint, error) {
	if len(signingKey) != ed25519.PrivateKeySize {
		return nil, errors.New("audit checkpoint signing key is invalid")
	}
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return nil, errors.New("audit checkpoint key id is required")
	}
	var coverSeq int64
	var chainHash []byte
	err := db.QueryRowContext(ctx, `SELECT chain_seq, hash FROM sesame_admin_audit_log ORDER BY chain_seq DESC LIMIT 1`).Scan(&coverSeq, &chainHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read audit chain head: %w", err)
	}
	if len(chainHash) != sha256.Size {
		return nil, errors.New("audit chain head has no valid hash")
	}
	var lastCoverSeq int64
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(cover_seq), 0) FROM sesame_admin_audit_checkpoints`).Scan(&lastCoverSeq); err != nil {
		return nil, fmt.Errorf("read audit checkpoints: %w", err)
	}
	if coverSeq <= lastCoverSeq {
		return nil, nil
	}
	signedAt := time.Now().UTC().Truncate(time.Microsecond)
	message, err := auditCheckpointMessage(coverSeq, chainHash, keyID, signedAt)
	if err != nil {
		return nil, err
	}
	signature := ed25519.Sign(signingKey, message)
	var id int64
	err = db.QueryRowContext(ctx, `
		INSERT INTO sesame_admin_audit_checkpoints (cover_seq, chain_hash, key_id, signed_at, signature)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (cover_seq) DO NOTHING
		RETURNING id
	`, coverSeq, chainHash, keyID, signedAt, signature).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("write audit checkpoint: %w", err)
	}
	return &AuditCheckpoint{
		ID:        id,
		CoverSeq:  coverSeq,
		ChainHash: chainHash,
		KeyID:     keyID,
		SignedAt:  signedAt,
		Signature: signature,
	}, nil
}

func VerifyAuditCheckpoints(ctx context.Context, db *sql.DB, publicKey ed25519.PublicKey, keyID string) (AuditCheckpointReport, error) {
	if len(publicKey) != ed25519.PublicKeySize {
		return AuditCheckpointReport{}, errors.New("audit checkpoint verification key is invalid")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, cover_seq, chain_hash, key_id, signed_at, signature
		FROM sesame_admin_audit_checkpoints
		WHERE key_id = $1
		ORDER BY cover_seq`, strings.TrimSpace(keyID))
	if err != nil {
		return AuditCheckpointReport{}, fmt.Errorf("read audit checkpoints: %w", err)
	}
	defer rows.Close()
	checkpoints := make([]AuditCheckpoint, 0)
	for rows.Next() {
		var checkpoint AuditCheckpoint
		if err := rows.Scan(&checkpoint.ID, &checkpoint.CoverSeq, &checkpoint.ChainHash, &checkpoint.KeyID, &checkpoint.SignedAt, &checkpoint.Signature); err != nil {
			return AuditCheckpointReport{}, fmt.Errorf("read audit checkpoint: %w", err)
		}
		checkpoints = append(checkpoints, checkpoint)
	}
	if err := rows.Err(); err != nil {
		return AuditCheckpointReport{}, fmt.Errorf("read audit checkpoints: %w", err)
	}
	if err := rows.Close(); err != nil {
		return AuditCheckpointReport{}, err
	}
	report := AuditCheckpointReport{Checkpoints: int64(len(checkpoints))}
	for _, checkpoint := range checkpoints {
		message, err := auditCheckpointMessage(checkpoint.CoverSeq, checkpoint.ChainHash, checkpoint.KeyID, checkpoint.SignedAt)
		if err != nil {
			return AuditCheckpointReport{}, err
		}
		if !ed25519.Verify(publicKey, message, checkpoint.Signature) {
			report.FirstBreak = fmt.Sprintf("checkpoint %d signature does not match", checkpoint.CoverSeq)
			return report, nil
		}
		var chainHash []byte
		err = db.QueryRowContext(ctx, `SELECT hash FROM sesame_admin_audit_log WHERE chain_seq = $1`, checkpoint.CoverSeq).Scan(&chainHash)
		if errors.Is(err, sql.ErrNoRows) {
			report.FirstBreak = fmt.Sprintf("checkpoint %d covers a missing audit row", checkpoint.CoverSeq)
			return report, nil
		}
		if err != nil {
			return AuditCheckpointReport{}, fmt.Errorf("read audit chain row: %w", err)
		}
		if !bytes.Equal(chainHash, checkpoint.ChainHash) {
			report.FirstBreak = fmt.Sprintf("checkpoint %d does not match the audit row", checkpoint.CoverSeq)
			return report, nil
		}
		latest := checkpoint
		report.Latest = &latest
	}
	report.Verified = true
	return report, nil
}
