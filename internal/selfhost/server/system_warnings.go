package server

import (
	"strconv"

	"usesesame.app/backend/internal/selfhost"
)

const (
	warningCodeConfiguration = "configuration"
	warningCodeAudit         = "audit"
	warningCodeBackup        = "backup"
	warningCodeForwarding    = "forwarding"
	warningCodeOrigin        = "origin"

	staleBackupText = "No backup has been made in over twice the configured interval."
)

type systemWarning struct {
	code string
	text string
}

func (s *server) systemWarnings(info selfhost.SystemInfo, report selfhost.AuditReport) []systemWarning {
	warnings := make([]systemWarning, 0, len(s.cfg.Warnings)+4)
	for _, text := range s.cfg.Warnings {
		warnings = append(warnings, systemWarning{code: warningCodeConfiguration, text: text})
	}
	if !report.OK {
		reason := "The audit log chain does not verify."
		if report.FirstBreak != nil {
			reason = "The audit log chain breaks at entry " + strconv.FormatInt(report.FirstBreak.Seq, 10) + ": " + report.FirstBreak.Reason + "."
		}
		warnings = append(warnings, systemWarning{code: warningCodeAudit, text: reason})
	}
	switch classifyBackup(info.LastBackupAt, s.now(), s.cfg.BackupInterval) {
	case backupMissing:
		warnings = append(warnings, systemWarning{code: warningCodeBackup, text: "No backup has been made yet."})
	case backupStale:
		warnings = append(warnings, systemWarning{code: warningCodeBackup, text: staleBackupText})
	case backupMissingScheduleOff:
		warnings = append(warnings, systemWarning{code: warningCodeBackup, text: "Scheduled backups are off and no backup has been made yet."})
	}
	if s.forwardedFromUntrusted() {
		warnings = append(warnings, systemWarning{code: warningCodeForwarding, text: "Requests from a private address carry X-Forwarded-For, but that address is not a trusted proxy. Every visitor shares one rate limit until SESAME_TRUSTED_PROXIES is set."})
	}
	if !s.cfg.PublicURL.Secure {
		warnings = append(warnings, systemWarning{code: warningCodeOrigin, text: "The public address is plain http on this machine, so pairing from another machine needs an https SESAME_PUBLIC_URL."})
	}
	return warnings
}
