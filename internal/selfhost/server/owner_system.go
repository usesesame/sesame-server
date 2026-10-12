package server

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const (
	exportFormat    = "sesame-selfhost-export-v1"
	exportPageSize  = 200
	exportMaxPages  = 5000
	exportWriteTime = 10 * time.Minute
	staleBackupText = "No backup has been made in over twice the configured interval."
)

func (s *server) warnings(info selfhost.SystemInfo, report selfhost.AuditReport) []string {
	warnings := append([]string{}, s.cfg.Warnings...)
	if !report.OK {
		reason := "The audit log chain does not verify."
		if report.FirstBreak != nil {
			reason = "The audit log chain breaks at entry " + strconv.FormatInt(report.FirstBreak.Seq, 10) + ": " + report.FirstBreak.Reason + "."
		}
		warnings = append(warnings, reason)
	}
	if s.cfg.BackupInterval > 0 {
		switch {
		case info.LastBackupAt == nil:
			warnings = append(warnings, "No backup has been made yet.")
		case s.now().Sub(*info.LastBackupAt) > 2*s.cfg.BackupInterval:
			warnings = append(warnings, staleBackupText)
		}
	} else if info.LastBackupAt == nil {
		warnings = append(warnings, "Scheduled backups are off and no backup has been made yet.")
	}
	if s.forwardedFromUntrusted() {
		warnings = append(warnings, "Requests from a private address carry X-Forwarded-For, but that address is not a trusted proxy. Every visitor shares one rate limit until SESAME_TRUSTED_PROXIES is set.")
	}
	if !s.cfg.PublicURL.Secure {
		warnings = append(warnings, "The public address is plain http on this machine, so pairing from another machine needs an https SESAME_PUBLIC_URL.")
	}
	return warnings
}

func fullCheckRequested(w http.ResponseWriter, r *http.Request) (bool, bool) {
	switch r.URL.Query().Get("full") {
	case "", "0", "false":
		return false, true
	case "1", "true":
		return true, true
	}
	writeError(w, http.StatusBadRequest, "invalid_full", "The full parameter must be true or false.")
	return false, false
}

func (s *server) verifyAudit(ctx context.Context, full bool) (selfhost.AuditReport, error) {
	if full {
		return s.cfg.Store.VerifyAudit(ctx)
	}
	return s.cfg.Store.VerifyAuditIncremental(ctx)
}

func (s *server) system(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	full, ok := fullCheckRequested(w, r)
	if !ok {
		return
	}
	info, err := s.cfg.Store.System(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	report, err := s.verifyAudit(r.Context(), full)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"version":         s.cfg.Version,
		"commit":          s.cfg.Commit,
		"schemaVersion":   info.SchemaVersion,
		"databaseBytes":   info.DatabaseBytes,
		"lastBackupAt":    stampPointer(info.LastBackupAt),
		"warnings":        s.warnings(info, report),
		"auditChainOk":    report.OK,
		"activeOwners":    info.ActiveOwners,
		"members":         info.Members,
		"activeDevices":   info.ActiveDevices,
		"pendingPairings": info.PendingPairing,
		"metricsEnabled":  s.cfg.Metrics,
		"updateAvailable": s.cfg.Updates.Available(r.Context()),
	})
}

func (s *server) export(w http.ResponseWriter, r *http.Request, session selfhost.OwnerSession) {
	ctx := r.Context()
	if _, err := s.cfg.Store.AppendAudit(ctx, selfhost.AuditInput{Actor: actorOf(session), Action: "export.created", Target: "instance"}); err != nil {
		s.storeError(w, r, err)
		return
	}
	instance, err := s.cfg.Store.Instance(ctx)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	members, err := s.cfg.Store.ListMembers(ctx)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	devices, err := s.cfg.Store.ListDevices(ctx, selfhost.DeviceFilter{IncludeInactive: true})
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	var newestFirst []auditRecord
	var cursor int64
	truncated := false
	for pages := 0; ; pages++ {
		if pages >= exportMaxPages {
			truncated = true
			break
		}
		page, err := s.cfg.Store.ListAudit(ctx, cursor, exportPageSize)
		if err != nil {
			s.storeError(w, r, err)
			return
		}
		for _, entry := range page.Entries {
			newestFirst = append(newestFirst, auditOf(entry))
		}
		if page.NextCursor == 0 {
			break
		}
		cursor = page.NextCursor
	}
	report, err := s.cfg.Store.VerifyAudit(ctx)
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	entries := make([]auditRecord, 0, len(newestFirst))
	for index := len(newestFirst) - 1; index >= 0; index-- {
		entries = append(entries, newestFirst[index])
	}
	memberRecords := make([]memberRecord, 0, len(members))
	for _, member := range members {
		memberRecords = append(memberRecords, memberOf(member))
	}
	deviceRecords := make([]deviceRecord, 0, len(devices))
	for _, device := range devices {
		deviceRecords = append(deviceRecords, deviceOf(device))
	}
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(exportWriteTime)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		requestLog(ctx).Warn("Sesame server could not extend the export write deadline", "error", err)
	}
	exportedAt := stamp(s.now())
	w.Header().Set("Content-Disposition", `attachment; filename="sesame-export-`+exportedAt.Format("20060102T150405Z")+`.json"`)
	writeJSON(w, http.StatusOK, map[string]any{
		"format":     exportFormat,
		"exportedAt": exportedAt,
		"instance":   map[string]any{"id": instance.ID, "name": instance.Name, "publicUrl": s.cfg.PublicURL.Origin, "createdAt": stamp(instance.CreatedAt)},
		"members":    memberRecords,
		"devices":    deviceRecords,
		"audit":      map[string]any{"chain": chainOf(report), "entries": entries, "truncated": truncated},
	})
}
