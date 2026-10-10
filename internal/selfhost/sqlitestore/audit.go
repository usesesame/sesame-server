package sqlitestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const (
	maxAuditActionLength = 64
	maxAuditTargetLength = 256
	maxAuditKeyLength    = 64
	defaultAuditPage     = 50
	maxAuditPage         = 200
)

var auditGenesisHash = make([]byte, sha256.Size)

func auditCanonical(actor, action, target string, detail map[string]string, at int64) ([]byte, error) {
	if detail == nil {
		detail = map[string]string{}
	}
	return json.Marshal(map[string]any{
		"actor":  actor,
		"action": action,
		"target": target,
		"detail": detail,
		"at":     time.Unix(at, 0).UTC().Format(time.RFC3339),
	})
}

func auditHash(previous, canonical []byte) []byte {
	digest := sha256.New()
	digest.Write([]byte(selfhost.AuditChainVersion + "\n"))
	digest.Write(previous)
	digest.Write([]byte("\n"))
	digest.Write(canonical)
	return digest.Sum(nil)
}

func validAuditInput(input selfhost.AuditInput) bool {
	switch input.Actor.Kind {
	case selfhost.ActorOwner, selfhost.ActorDevice, selfhost.ActorSystem:
	default:
		return false
	}
	if input.Action == "" || len(input.Action) > maxAuditActionLength || len(input.Target) > maxAuditTargetLength || len(input.Actor.ID) > maxAuditTargetLength {
		return false
	}
	if len(input.Detail) > selfhost.MaxAuditDetailEntries {
		return false
	}
	for key, value := range input.Detail {
		if key == "" || len(key) > maxAuditKeyLength || len(value) > selfhost.MaxAuditDetailValueBytes {
			return false
		}
	}
	return true
}

func (s *Store) audit(ctx context.Context, tx *sql.Tx, by selfhost.Actor, action, target string, detail map[string]string) error {
	_, err := s.appendAudit(ctx, tx, selfhost.AuditInput{Actor: by, Action: action, Target: target, Detail: detail})
	return err
}

func (s *Store) appendAudit(ctx context.Context, tx *sql.Tx, input selfhost.AuditInput) (selfhost.AuditEntry, error) {
	if !validAuditInput(input) {
		return selfhost.AuditEntry{}, selfhost.ErrInvalidInput
	}
	var lastSeq int64
	previous := auditGenesisHash
	err := tx.QueryRowContext(ctx, `SELECT seq, hash FROM audit_log ORDER BY seq DESC LIMIT 1`).Scan(&lastSeq, &previous)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return selfhost.AuditEntry{}, err
	}
	detail := input.Detail
	if detail == nil {
		detail = map[string]string{}
	}
	at := s.clock().Unix()
	actor := input.Actor.String()
	canonical, err := auditCanonical(actor, input.Action, input.Target, detail, at)
	if err != nil {
		return selfhost.AuditEntry{}, err
	}
	hash := auditHash(previous, canonical)
	detailJSON, err := json.Marshal(detail)
	if err != nil {
		return selfhost.AuditEntry{}, err
	}
	seq := lastSeq + 1
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_log (seq, actor, action, target, detail, at, prev_hash, hash) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		seq, actor, input.Action, input.Target, string(detailJSON), at, previous, hash); err != nil {
		return selfhost.AuditEntry{}, err
	}
	return selfhost.AuditEntry{
		Seq: seq, Actor: actor, Action: input.Action, Target: input.Target, Detail: detail,
		At: unixTime(at), PrevHash: hex.EncodeToString(previous), Hash: hex.EncodeToString(hash),
	}, nil
}

func (s *Store) AppendAudit(ctx context.Context, input selfhost.AuditInput) (selfhost.AuditEntry, error) {
	var entry selfhost.AuditEntry
	err := s.write(ctx, func(tx *sql.Tx) error {
		var err error
		entry, err = s.appendAudit(ctx, tx, input)
		return err
	})
	return entry, err
}

func (s *Store) ListAudit(ctx context.Context, cursor int64, limit int) (selfhost.AuditPage, error) {
	db, err := s.read()
	if err != nil {
		return selfhost.AuditPage{}, err
	}
	if limit <= 0 {
		limit = defaultAuditPage
	}
	if limit > maxAuditPage {
		limit = maxAuditPage
	}
	if cursor <= 0 {
		cursor = 1<<62 - 1
	}
	rows, err := db.QueryContext(ctx, `
		SELECT seq, actor, action, target, detail, at, prev_hash, hash
		FROM audit_log WHERE seq < ? ORDER BY seq DESC LIMIT ?
	`, cursor, limit+1)
	if err != nil {
		return selfhost.AuditPage{}, err
	}
	defer rows.Close()
	page := selfhost.AuditPage{Entries: []selfhost.AuditEntry{}}
	for rows.Next() {
		var entry selfhost.AuditEntry
		var detail string
		var at int64
		var previous, hash []byte
		if err := rows.Scan(&entry.Seq, &entry.Actor, &entry.Action, &entry.Target, &detail, &at, &previous, &hash); err != nil {
			return selfhost.AuditPage{}, err
		}
		if err := json.Unmarshal([]byte(detail), &entry.Detail); err != nil {
			entry.Detail = map[string]string{}
		}
		entry.At = unixTime(at)
		entry.PrevHash = hex.EncodeToString(previous)
		entry.Hash = hex.EncodeToString(hash)
		page.Entries = append(page.Entries, entry)
	}
	if err := rows.Err(); err != nil {
		return selfhost.AuditPage{}, err
	}
	if len(page.Entries) > limit {
		page.Entries = page.Entries[:limit]
		page.NextCursor = page.Entries[limit-1].Seq
	}
	return page, nil
}

type auditState struct {
	report selfhost.AuditReport
	head   []byte
}

type auditScan struct {
	report           selfhost.AuditReport
	expectedSeq      int64
	expectedPrevious []byte
	head             []byte
}

func newAuditScan() *auditScan {
	return &auditScan{report: selfhost.AuditReport{OK: true}, expectedSeq: 1, expectedPrevious: auditGenesisHash, head: auditGenesisHash}
}

func (a *auditScan) resume(state *auditState) {
	a.report = state.report
	a.report.OK = true
	a.report.FirstBreak = nil
	a.expectedSeq = state.report.HeadSeq + 1
	a.expectedPrevious = state.head
	a.head = state.head
}

func (a *auditScan) fail(seq int64, reason string) {
	if a.report.FirstBreak == nil {
		a.report.OK = false
		a.report.FirstBreak = &selfhost.AuditBreak{Seq: seq, Reason: reason}
	}
}

func (a *auditScan) consume(seq int64, actor, action, target, detailText string, at int64, previous, hash []byte) {
	a.report.Rows++
	a.report.HeadSeq = seq
	a.head = hash
	if a.report.FirstBreak != nil {
		return
	}
	if reason := auditRowBreak(a.expectedSeq, a.expectedPrevious, seq, actor, action, target, detailText, at, previous, hash); reason != "" {
		a.fail(seq, reason)
		return
	}
	a.expectedSeq++
	a.expectedPrevious = hash
}

func (a *auditScan) result() selfhost.AuditReport {
	report := a.report
	report.HeadHash = hex.EncodeToString(a.head)
	return report
}

func (s *Store) VerifyAudit(ctx context.Context) (selfhost.AuditReport, error) {
	db, err := s.read()
	if err != nil {
		return selfhost.AuditReport{}, err
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	return s.verifyAuditFull(ctx, db)
}

func (s *Store) verifyAuditFull(ctx context.Context, db queryer) (selfhost.AuditReport, error) {
	scan, err := scanAudit(ctx, db, newAuditScan(), 0, nil)
	if err != nil {
		s.auditState = nil
		return selfhost.AuditReport{}, err
	}
	s.remember(scan)
	return scan.result(), nil
}

func (s *Store) VerifyAuditIncremental(ctx context.Context) (selfhost.AuditReport, error) {
	db, err := s.read()
	if err != nil {
		return selfhost.AuditReport{}, err
	}
	s.auditMu.Lock()
	defer s.auditMu.Unlock()
	state := s.auditState
	if state == nil {
		return s.verifyAuditFull(ctx, db)
	}
	if !state.report.OK {
		sticky := state.report
		broken := *sticky.FirstBreak
		sticky.FirstBreak = &broken
		return sticky, nil
	}
	scan := newAuditScan()
	scan.resume(state)
	var verifiedHead []byte
	if state.report.HeadSeq > 0 {
		verifiedHead = state.head
	}
	scan, err = scanAudit(ctx, db, scan, state.report.HeadSeq, verifiedHead)
	if err != nil {
		return selfhost.AuditReport{}, err
	}
	s.remember(scan)
	return scan.result(), nil
}

func (s *Store) remember(scan *auditScan) {
	s.auditState = &auditState{report: scan.result(), head: scan.head}
}

func verifyAudit(ctx context.Context, db queryer) (selfhost.AuditReport, error) {
	scan, err := scanAudit(ctx, db, newAuditScan(), 0, nil)
	if err != nil {
		return selfhost.AuditReport{}, err
	}
	return scan.result(), nil
}

func scanAudit(ctx context.Context, db queryer, scan *auditScan, fromSeq int64, verifiedHash []byte) (*auditScan, error) {
	rows, err := db.QueryContext(ctx, `SELECT seq, actor, action, target, detail, at, prev_hash, hash FROM audit_log WHERE seq >= ? ORDER BY seq`, max(fromSeq, 1))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	checkHead := verifiedHash != nil
	for rows.Next() {
		var seq, at int64
		var actor, action, target, detailText string
		var previous, hash []byte
		if err := rows.Scan(&seq, &actor, &action, &target, &detailText, &at, &previous, &hash); err != nil {
			return nil, err
		}
		if checkHead {
			checkHead = false
			if seq != fromSeq || !bytes.Equal(hash, verifiedHash) {
				scan.fail(fromSeq, "the last verified row is missing or changed")
				scan.consume(seq, actor, action, target, detailText, at, previous, hash)
				continue
			}
			if reason := auditRowBreak(seq, previous, seq, actor, action, target, detailText, at, previous, hash); reason != "" {
				scan.fail(seq, reason)
			}
			continue
		}
		scan.consume(seq, actor, action, target, detailText, at, previous, hash)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if checkHead {
		scan.fail(fromSeq, "the last verified row is missing or changed")
	}
	return scan, nil
}

func auditRowBreak(expectedSeq int64, expectedPrevious []byte, seq int64, actor, action, target, detailText string, at int64, previous, hash []byte) string {
	if seq != expectedSeq {
		return "sequence gap or reorder"
	}
	if !bytes.Equal(previous, expectedPrevious) {
		return "previous hash does not match the preceding row"
	}
	var detail map[string]string
	if err := json.Unmarshal([]byte(detailText), &detail); err != nil {
		return "detail is not valid"
	}
	canonical, err := auditCanonical(actor, action, target, detail, at)
	if err != nil {
		return "row cannot be encoded"
	}
	if !bytes.Equal(auditHash(previous, canonical), hash) {
		return "row hash does not match its content"
	}
	return ""
}
