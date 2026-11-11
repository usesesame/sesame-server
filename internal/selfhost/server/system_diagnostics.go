package server

import (
	"net/http"
	"net/netip"
	"time"

	"usesesame.app/backend/internal/selfhost"
)

const (
	diagnosticsSchemaVersion = 1

	diagnosticsOK      = "ok"
	diagnosticsAction  = "action"
	diagnosticsUnknown = "unknown"

	diagnosticsOrigin     = warningCodeOrigin
	diagnosticsProxy      = "proxy"
	diagnosticsAudit      = warningCodeAudit
	diagnosticsBackup     = warningCodeBackup
	diagnosticsForwarding = warningCodeForwarding
	diagnosticsExternal   = "external"
)

var diagnosticsLimitations = []string{
	"certificate_validity_not_checked",
	"network_reachability_not_probed",
	"host_configuration_not_inspected",
	"audit_uses_incremental_verification",
	"backup_off_host_copy_not_verified",
	"recovery_drill_not_verified",
	"unseen_traffic_not_evaluated",
}

type diagnosticsReport struct {
	SchemaVersion int                `json:"schemaVersion"`
	GeneratedAt   time.Time          `json:"generatedAt"`
	Overall       string             `json:"overall"`
	Checks        []diagnosticsCheck `json:"checks"`
	Limitations   []string           `json:"limitations"`
}

type diagnosticsCheck struct {
	ID      string  `json:"id"`
	State   string  `json:"state"`
	Code    string  `json:"code"`
	Summary string  `json:"summary"`
	Next    *string `json:"next"`
}

type backupKind int

const (
	backupMissing backupKind = iota
	backupMissingScheduleOff
	backupRecorded
	backupRecordedScheduleOff
	backupStale
	backupFuture
)

type proxyTrustKind int

const (
	proxyNotConfigured proxyTrustKind = iota
	proxyConfigured
	proxyWide
)

func (s *server) systemDiagnostics(w http.ResponseWriter, r *http.Request, _ selfhost.OwnerSession) {
	if r.URL.RawQuery != "" || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "The diagnostics request takes no query parameters and no body.")
		return
	}
	info, err := s.cfg.Store.System(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	audit, err := s.cfg.Store.VerifyAuditIncremental(r.Context())
	if err != nil {
		s.storeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, s.buildDiagnostics(info, audit))
}

func (s *server) buildDiagnostics(info selfhost.SystemInfo, audit selfhost.AuditReport) diagnosticsReport {
	now := s.now()
	checks := []diagnosticsCheck{
		originDiagnostics(s.cfg.PublicURL.Secure),
		proxyDiagnostics(s.cfg.TrustedProxies, s.cfg.PublicURL.Secure),
		auditDiagnostics(audit),
		backupDiagnostics(info.LastBackupAt, now, s.cfg.BackupInterval),
		forwardingDiagnostics(s.forwardedFromUntrusted()),
		externalDiagnostics(),
	}
	return diagnosticsReport{
		SchemaVersion: diagnosticsSchemaVersion,
		GeneratedAt:   stamp(now),
		Overall:       aggregateDiagnostics(checks),
		Checks:        checks,
		Limitations:   append([]string{}, diagnosticsLimitations...),
	}
}

func originDiagnostics(secure bool) diagnosticsCheck {
	if secure {
		return newDiagnosticsCheck(diagnosticsOrigin, diagnosticsOK, "origin_https", "The configured public address uses HTTPS.", nil)
	}
	return newDiagnosticsCheck(diagnosticsOrigin, diagnosticsOK, "origin_local", "The configured public address is a loopback HTTP origin, so links work only on this computer.", diagnosticsNext("Set an HTTPS public address before pairing from another computer."))
}

func proxyDiagnostics(prefixes []netip.Prefix, secure bool) diagnosticsCheck {
	switch classifyProxy(prefixes) {
	case proxyWide:
		return newDiagnosticsCheck(diagnosticsProxy, diagnosticsAction, "proxy_wide", "A trusted proxy range is wider than /24 for IPv4 or /64 for IPv6.", diagnosticsNext("Narrow the trusted proxy ranges to the addresses the proxy uses."))
	case proxyConfigured:
		return newDiagnosticsCheck(diagnosticsProxy, diagnosticsOK, "proxy_configured", "Trusted proxy ranges are configured, and none of the non-loopback ranges is wider than /24 for IPv4 or /64 for IPv6.", nil)
	}
	if secure {
		return newDiagnosticsCheck(diagnosticsProxy, diagnosticsUnknown, "proxy_not_configured", "No trusted proxy ranges are configured while the public address uses HTTPS, so proxy trust is unspecified.", diagnosticsNext("Configure proxy ranges only when a proxy terminates TLS in front of this server."))
	}
	return newDiagnosticsCheck(diagnosticsProxy, diagnosticsOK, "proxy_not_configured", "No trusted proxy ranges are configured, and the public address uses loopback HTTP.", nil)
}

func auditDiagnostics(report selfhost.AuditReport) diagnosticsCheck {
	if report.OK {
		return newDiagnosticsCheck(diagnosticsAudit, diagnosticsOK, "audit_incremental_ok", "The incremental audit verification found no break in the rows it covers, which are the last checked row and rows added since the last successful check.", nil)
	}
	return newDiagnosticsCheck(diagnosticsAudit, diagnosticsAction, "audit_incremental_break", "The incremental audit verification found a break in the audit chain.", diagnosticsNext("Run a full audit check to find the affected entry and keep the database for review."))
}

func backupDiagnostics(last *time.Time, now time.Time, interval time.Duration) diagnosticsCheck {
	switch classifyBackup(last, now, interval) {
	case backupMissing:
		return newDiagnosticsCheck(diagnosticsBackup, diagnosticsAction, "backup_missing", "No backup is recorded while scheduled backups are configured.", diagnosticsNext("Run a backup now or wait for the next scheduled one, and keep an encrypted copy off this host."))
	case backupMissingScheduleOff:
		return newDiagnosticsCheck(diagnosticsBackup, diagnosticsAction, "backup_missing_schedule_off", "No backup is recorded and scheduled backups are off.", diagnosticsNext("Take an encrypted backup and turn on a backup interval if this instance holds data you need."))
	case backupFuture:
		return newDiagnosticsCheck(diagnosticsBackup, diagnosticsUnknown, "backup_future", "The last recorded backup time is in the future, so this report cannot judge its age.", diagnosticsNext("Check the host clock and the recorded backup time."))
	case backupRecordedScheduleOff:
		return newDiagnosticsCheck(diagnosticsBackup, diagnosticsUnknown, "backup_recorded_schedule_off", "A backup has been recorded, but scheduled backups are off, so this report cannot judge whether it is recent enough.", diagnosticsNext("Keep an encrypted copy off this host, check that a restore works, and turn on a backup interval if you want this report to judge freshness."))
	case backupStale:
		return newDiagnosticsCheck(diagnosticsBackup, diagnosticsAction, "backup_stale", "The last recorded backup is older than twice the configured interval.", diagnosticsNext("Run a backup now and check that the scheduled backup can write one."))
	}
	return newDiagnosticsCheck(diagnosticsBackup, diagnosticsOK, "backup_recorded", "The last recorded backup is within twice the configured interval, but that record does not prove an encrypted off-host copy or a successful recovery drill.", diagnosticsNext("Keep an encrypted copy off this host and check that a restore works."))
}

func forwardingDiagnostics(observed bool) diagnosticsCheck {
	if observed {
		return newDiagnosticsCheck(diagnosticsForwarding, diagnosticsAction, "forwarding_untrusted_observed", "Requests from a private address carried X-Forwarded-For, but that address is not a trusted proxy, so every visitor shares one rate limit.", diagnosticsNext("Set SESAME_TRUSTED_PROXIES to the ranges the proxy uses, or remove the forwarding header."))
	}
	return newDiagnosticsCheck(diagnosticsForwarding, diagnosticsOK, "forwarding_not_observed", "The server has not observed a forwarded request from an untrusted proxy, and that says nothing about traffic it has not seen.", nil)
}

func externalDiagnostics() diagnosticsCheck {
	return newDiagnosticsCheck(diagnosticsExternal, diagnosticsUnknown, "external_not_inspected", "This report does not inspect certificates, network reachability or host configuration.", diagnosticsNext("Check the public address and its certificate from another network with your own tools."))
}

func newDiagnosticsCheck(id, state, code, summary string, next *string) diagnosticsCheck {
	return diagnosticsCheck{ID: id, State: state, Code: code, Summary: summary, Next: next}
}

func diagnosticsNext(value string) *string { return &value }

func aggregateDiagnostics(checks []diagnosticsCheck) string {
	overall := diagnosticsOK
	for _, check := range checks {
		if check.State == diagnosticsAction {
			return diagnosticsAction
		}
		if check.State == diagnosticsUnknown {
			overall = diagnosticsUnknown
		}
	}
	return overall
}

func classifyProxy(prefixes []netip.Prefix) proxyTrustKind {
	if len(prefixes) == 0 {
		return proxyNotConfigured
	}
	for _, prefix := range prefixes {
		if proxyRangeWide(prefix) {
			return proxyWide
		}
	}
	return proxyConfigured
}

func proxyRangeWide(prefix netip.Prefix) bool {
	if prefix.Addr().IsLoopback() {
		return false
	}
	limit := 64
	if prefix.Addr().Is4() {
		limit = 24
	}
	return prefix.Bits() < limit
}

func classifyBackup(last *time.Time, now time.Time, interval time.Duration) backupKind {
	if last == nil {
		if interval <= 0 {
			return backupMissingScheduleOff
		}
		return backupMissing
	}
	if last.After(now) {
		return backupFuture
	}
	if interval <= 0 {
		return backupRecordedScheduleOff
	}
	age := now.Sub(*last)
	if age <= interval {
		return backupRecorded
	}
	if age-interval <= interval {
		return backupRecorded
	}
	return backupStale
}
