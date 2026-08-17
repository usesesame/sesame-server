package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
	"usesesame.app/backend/internal/releases"
)

var sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var pricePattern = regexp.MustCompile(`^\d{1,6}(\.\d{1,2})?$`)

type suspendUserRequest struct {
	Reason string `json:"reason"`
}

type featureFlagRequest struct {
	Value string `json:"value"`
}

type inviteAdminRequest struct {
	Email string          `json:"email"`
	Role  adminstore.Role `json:"role"`
}

type updateAdminRequest struct {
	Role      adminstore.Role `json:"role"`
	Suspended bool            `json:"suspended"`
}

func pagination(request *http.Request) (int, int) {
	page, _ := strconv.Atoi(request.URL.Query().Get("page"))
	size, _ := strconv.Atoi(request.URL.Query().Get("size"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 25
	}
	return page, size
}

func (a *api) adminUsers(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersRead); !ok {
		return
	}
	page, size := pagination(request)
	query := request.URL.Query().Get("query")
	if len(query) > 254 {
		writeError(response, http.StatusBadRequest, "invalid_user_search", "The user search is too long.")
		return
	}
	users, total, err := a.config.Admin.Users(request.Context(), query, page, size)
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"users": users, "page": page, "size": size, "total": total})
}

func (a *api) adminUserRoute(response http.ResponseWriter, request *http.Request) {
	parts := adminPathParts(request.URL.Path, "/v1/admin/users/")
	if len(parts) == 0 || parts[0] == "" {
		a.notFound(response, request)
		return
	}
	accountID := parts[0]
	if len(parts) == 1 && request.Method == http.MethodGet {
		if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersRead); !ok {
			return
		}
		user, err := a.config.Admin.User(request.Context(), accountID)
		if err != nil {
			adminStoreError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"user": user})
		return
	}
	if len(parts) == 1 && request.Method == http.MethodDelete {
		actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersDelete)
		if !ok {
			return
		}
		if err := a.config.Admin.DeleteUser(request.Context(), actor, accountID, a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[1] == "owner-release" && (request.Method == http.MethodPost || request.Method == http.MethodDelete) {
		actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
		if !ok {
			return
		}
		if err := a.config.Admin.SetOwnerReleaseRingMember(request.Context(), actor, accountID, request.Method == http.MethodPost, a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersManage)
	if !ok {
		return
	}
	if len(parts) == 2 && parts[1] == "beta" && (request.Method == http.MethodPost || request.Method == http.MethodDelete) {
		if err := a.config.Admin.SetBeta(request.Context(), actor, accountID, request.Method == http.MethodPost, a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[1] == "suspend" && (request.Method == http.MethodPost || request.Method == http.MethodDelete) {
		reason := ""
		if request.Method == http.MethodPost {
			var input suspendUserRequest
			if !decodeAdminJSON(response, request, &input) || !safeReason(input.Reason) {
				writeError(response, http.StatusBadRequest, "invalid_suspend_reason", "Use an ASCII reason of at most 200 characters.")
				return
			}
			reason = strings.TrimSpace(input.Reason)
		}
		if err := a.config.Admin.SetSuspended(request.Context(), actor, accountID, request.Method == http.MethodPost, reason, a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 2 && parts[1] == "sessions" && request.Method == http.MethodDelete {
		if err := a.config.Admin.RevokeUserSessions(request.Context(), actor, accountID, a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if len(parts) == 3 && parts[1] == "devices" && request.Method == http.MethodDelete {
		if err := a.config.Admin.RevokeUserDevice(request.Context(), actor, accountID, parts[2], a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	a.notFound(response, request)
}

func safeReason(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) > 200 {
		return false
	}
	for _, character := range value {
		if character < 0x20 || character > 0x7e {
			return false
		}
	}
	return true
}

func (a *api) adminFlags(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	account, ok := a.adminForRequest(response, request)
	if !ok || !(adminstore.Allowed(account.Role, adminstore.PermissionFlagsManage) || account.Role == adminstore.RoleReadonly) {
		if ok {
			writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view feature flags.")
		}
		return
	}
	flags, err := a.config.Admin.FeatureFlags(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"flags": flags})
}

func (a *api) adminFlag(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPatch) {
		return
	}
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionFlagsManage)
	if !ok {
		return
	}
	parts := adminPathParts(request.URL.Path, "/v1/admin/flags/")
	if len(parts) != 1 {
		a.notFound(response, request)
		return
	}
	var input featureFlagRequest
	if !decodeAdminJSON(response, request, &input) || !validFeatureFlag(parts[0], input.Value) {
		writeError(response, http.StatusBadRequest, "invalid_feature_flag", "That feature flag value is not allowed.")
		return
	}
	if err := a.config.Admin.UpdateFeatureFlag(request.Context(), actor, parts[0], input.Value, a.adminIPHash(request)); err != nil {
		adminStoreError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func validFeatureFlag(key, value string) bool {
	switch key {
	case "registration_mode":
		return value == "closed" || value == "invite" || value == "public"
	case "cloud_sync_available", "public_download", "desktop_linking_enabled", "downloads_enabled", "updater_enabled":
		return value == "true" || value == "false"
	default:
		return false
	}
}

func (a *api) adminPlans(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	account, ok := a.adminForRequest(response, request)
	if !ok || !(adminstore.Allowed(account.Role, adminstore.PermissionPlansWrite) || account.Role == adminstore.RoleReadonly) {
		if ok {
			writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view product plans.")
		}
		return
	}
	plans, err := a.config.Admin.Plans(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"plans": plans})
}

func (a *api) adminPlan(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPatch) {
		return
	}
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionPlansWrite)
	if !ok {
		return
	}
	parts := adminPathParts(request.URL.Path, "/v1/admin/plans/")
	if len(parts) != 1 {
		a.notFound(response, request)
		return
	}
	var input adminstore.Plan
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	input.ID = parts[0]
	if !validPlan(input) {
		writeError(response, http.StatusBadRequest, "invalid_plan", "The plan fields are invalid.")
		return
	}
	if err := a.config.Admin.UpdatePlan(request.Context(), actor, input, a.adminIPHash(request)); err != nil {
		adminStoreError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func validPlan(plan adminstore.Plan) bool {
	if strings.TrimSpace(plan.Name) == "" || len(plan.Name) > 80 || len(plan.Description) > 500 || !pricePattern.MatchString(plan.Price) || len(plan.Includes) > 20 {
		return false
	}
	if plan.Billing != "none" && plan.Billing != "one_time" && plan.Billing != "monthly" && plan.Billing != "yearly" {
		return false
	}
	for _, item := range plan.Includes {
		if len(item) == 0 || len(item) > 120 {
			return false
		}
	}
	return true
}

func (a *api) adminReleases(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	account, ok := a.adminForRequest(response, request)
	if !ok || !(adminstore.Allowed(account.Role, adminstore.PermissionReleaseWrite) || account.Role == adminstore.RoleReadonly) {
		if ok {
			writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view releases.")
		}
		return
	}
	releases, err := a.config.Admin.Releases(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"releases": releases})
}

func (a *api) adminRelease(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodPut) {
		return
	}
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
	if !ok {
		return
	}
	parts := adminPathParts(request.URL.Path, "/v1/admin/releases/")
	if len(parts) != 1 {
		a.notFound(response, request)
		return
	}
	var input adminstore.Release
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	input.Platform = parts[0]
	if !validRelease(input) {
		writeError(response, http.StatusBadRequest, "invalid_release", "Release metadata must be complete, signed, and use HTTPS URLs before it can be saved.")
		return
	}
	if err := a.config.Admin.UpdateRelease(request.Context(), actor, input, a.adminIPHash(request)); err != nil {
		adminStoreError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func validRelease(release adminstore.Release) bool {
	validHTTPS := func(raw string) bool {
		parsed, err := url.Parse(raw)
		return err == nil && parsed.Scheme == "https" && parsed.Host != ""
	}
	baseValid := len(release.Channel) > 0 && len(release.Channel) <= 40 && len(release.Platform) > 0 && len(release.Platform) <= 40 && len(release.Architecture) > 0 && len(release.Architecture) <= 40 && release.RolloutPercent >= 0 && release.RolloutPercent <= 100 &&
		len(release.Version) <= 80 && releases.ValidVersion(release.Version) && (release.Status == "draft" || release.Status == "published" || release.Status == "withdrawn")
	if !baseValid {
		return false
	}
	if release.Status == "draft" {
		return (release.URL == "" || validHTTPS(release.URL)) && (release.ReleaseNotesURL == "" || validHTTPS(release.ReleaseNotesURL)) &&
			(release.SHA256 == "" || sha256Pattern.MatchString(release.SHA256)) && len(release.SigningKeyID) <= 120
	}
	return validHTTPS(release.URL) && sha256Pattern.MatchString(release.SHA256) && len(release.Signature) >= 64 &&
		len(release.SigningKeyID) > 0 && len(release.SigningKeyID) <= 120 && len(release.SupportedWindows) > 0 && validHTTPS(release.ReleaseNotesURL)
}

func (a *api) adminAccounts(response http.ResponseWriter, request *http.Request) {
	if request.Method == http.MethodGet {
		actor, ok := a.adminForRequest(response, request)
		if !ok {
			return
		}
		if !adminstore.Allowed(actor.Role, adminstore.PermissionAdminsManage) && actor.Role != adminstore.RoleReadonly {
			writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view administrators.")
			return
		}
		admins, err := a.config.Admin.Admins(request.Context())
		if err != nil {
			adminStoreError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"admins": admins})
		return
	}
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionAdminsManage)
	if !ok {
		return
	}
	if request.Method != http.MethodPost {
		allowMethod(response, request, http.MethodPost)
		return
	}
	var input inviteAdminRequest
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	email, valid := normalizedEmail(input.Email)
	if !valid || !adminstore.ValidRole(input.Role) {
		writeError(response, http.StatusBadRequest, "invalid_admin_invite", "Use a valid email address and admin role.")
		return
	}
	created, token, err := a.config.Admin.InviteAdmin(request.Context(), actor, email, input.Role, time.Now().UTC().Add(time.Hour), a.adminIPHash(request))
	if err != nil {
		adminStoreError(response, err)
		return
	}
	setupURL := strings.TrimSuffix(a.config.AdminOrigin, "/") + "/setup?token=" + url.QueryEscape(token)
	writeJSON(response, http.StatusCreated, map[string]any{"admin": created, "setupUrl": setupURL, "expiresAt": time.Now().UTC().Add(time.Hour)})
}

func (a *api) adminAccountRoute(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionAdminsManage)
	if !ok {
		return
	}
	parts := adminPathParts(request.URL.Path, "/v1/admin/admins/")
	if len(parts) != 1 {
		a.notFound(response, request)
		return
	}
	if request.Method == http.MethodDelete {
		if err := a.config.Admin.DeleteAdmin(request.Context(), actor, parts[0], a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	if request.Method != http.MethodPatch {
		allowMethod(response, request, http.MethodPatch)
		return
	}
	var input updateAdminRequest
	if !decodeAdminJSON(response, request, &input) || !adminstore.ValidRole(input.Role) {
		writeError(response, http.StatusBadRequest, "invalid_admin_update", "Use a valid admin role and suspension state.")
		return
	}
	if err := a.config.Admin.UpdateAdmin(request.Context(), actor, parts[0], input.Role, input.Suspended, a.adminIPHash(request)); err != nil {
		adminStoreError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) adminAudit(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionAuditAll)
	if !ok {
		return
	}
	a.writeAdminAudit(response, request, actor, true)
}

func (a *api) adminAuditMe(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	actor, ok := a.adminForRequest(response, request)
	if !ok {
		return
	}
	a.writeAdminAudit(response, request, actor, false)
}

func (a *api) writeAdminAudit(response http.ResponseWriter, request *http.Request, actor adminstore.Account, all bool) {
	page, size := pagination(request)
	filter, ok := adminAuditFilter(response, request)
	if !ok {
		return
	}
	entries, total, err := a.config.Admin.Audit(request.Context(), actor, all, filter, page, size)
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"entries": entries, "page": page, "size": size, "total": total})
}

func adminAuditFilter(response http.ResponseWriter, request *http.Request) (adminstore.AuditFilter, bool) {
	filter := adminstore.AuditFilter{AdminID: request.URL.Query().Get("admin"), Action: request.URL.Query().Get("action")}
	if len(filter.AdminID) > 128 || len(filter.Action) > 128 {
		writeError(response, http.StatusBadRequest, "invalid_audit_filter", "The audit filter is too long.")
		return adminstore.AuditFilter{}, false
	}
	for raw, target := range map[string]*time.Time{"from": &filter.From, "to": &filter.To} {
		value := request.URL.Query().Get(raw)
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(response, http.StatusBadRequest, "invalid_audit_filter", "Audit dates must use RFC 3339 timestamps.")
			return adminstore.AuditFilter{}, false
		}
		*target = parsed
	}
	return filter, true
}

func (a *api) adminAuditExport(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionAuditAll)
	if !ok {
		return
	}
	filter, ok := adminAuditFilter(response, request)
	if !ok {
		return
	}
	entries := make([]adminstore.AuditEntry, 0)
	for page := 1; ; page++ {
		batch, total, err := a.config.Admin.Audit(request.Context(), actor, true, filter, page, 200)
		if err != nil {
			adminStoreError(response, err)
			return
		}
		entries = append(entries, batch...)
		if len(entries) >= total || len(batch) == 0 {
			break
		}
	}
	response.Header().Set("Content-Type", "text/csv; charset=utf-8")
	response.Header().Set("Content-Disposition", `attachment; filename="sesame-admin-audit.csv"`)
	writer := csv.NewWriter(response)
	_ = writer.Write([]string{"id", "created_at", "admin_email", "action", "target_type", "target_id", "detail"})
	for _, entry := range entries {
		target := ""
		if entry.TargetID != nil {
			target = *entry.TargetID
		}
		detail, _ := json.Marshal(entry.Detail)
		_ = writer.Write([]string{strconv.FormatInt(entry.ID, 10), entry.CreatedAt.Format(time.RFC3339), csvSafe(entry.AdminEmail), csvSafe(entry.Action), csvSafe(entry.TargetType), csvSafe(target), csvSafe(string(detail))})
	}
	writer.Flush()
}

func csvSafe(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	default:
		return value
	}
}

func (a *api) adminSystemHealth(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionSystemRead); !ok {
		return
	}
	db := "ok"
	if err := a.config.Admin.Ping(request.Context()); err != nil {
		db = "unavailable"
	}
	overview, err := a.config.Admin.Overview(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"api": "ok", "database": db, "version": a.config.Version, "uptimeSeconds": int(time.Since(a.startedAt).Seconds()), "overview": overview})
}

func (a *api) adminRateLimits(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionSystemRead); !ok {
		return
	}
	metrics, err := a.config.Admin.RateLimitMetrics(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"metrics": metrics})
}

func (a *api) adminSystemConfig(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionSystemRead); !ok {
		return
	}
	flags, err := a.config.Admin.FeatureFlags(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"adminOrigin": a.config.AdminOrigin, "sessionTTLSeconds": int(a.config.AdminSessionTTL.Seconds()),
		"trustedProxyCount": len(a.config.TrustedProxies), "featureFlags": flags,
		"trustedProxiesRuntimeEditable": false,
		"note":                          "Trusted proxy changes require a reviewed configuration change and restart.",
	})
}

func (a *api) adminOverview(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	if _, ok := a.adminForRequest(response, request); !ok {
		return
	}
	overview, err := a.config.Admin.Overview(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"overview": overview})
}

type ticketReplyRequest struct {
	Body string `json:"body"`
}

type ticketNoteRequest struct {
	Body string `json:"body"`
}

type ticketAssignRequest struct {
	AdminID string `json:"adminId"`
}

type ticketStatusRequest struct {
	Status string `json:"status"`
}

type ticketPriorityRequest struct {
	Priority string `json:"priority"`
}

func (a *api) adminSupportTickets(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	actor, ok := a.adminForRequest(response, request)
	if !ok {
		return
	}
	if !adminstore.Allowed(actor.Role, adminstore.PermissionSupportRead) {
		writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view support tickets.")
		return
	}
	page, size := pagination(request)
	filter := adminstore.TicketListFilter{
		Status:   request.URL.Query().Get("status"),
		Priority: request.URL.Query().Get("priority"),
		Category: request.URL.Query().Get("category"),
		Assigned: request.URL.Query().Get("assigned"),
		Query:    request.URL.Query().Get("query"),
	}
	if len(filter.Query) > 254 {
		writeError(response, http.StatusBadRequest, "invalid_ticket_search", "The search is too long.")
		return
	}
	if filter.Status != "" && !adminstore.ValidTicketStatus(filter.Status) {
		writeError(response, http.StatusBadRequest, "invalid_ticket_filter", "That status value is not recognized.")
		return
	}
	if filter.Priority != "" && !adminstore.ValidTicketPriority(filter.Priority) {
		writeError(response, http.StatusBadRequest, "invalid_ticket_filter", "That priority value is not recognized.")
		return
	}
	if filter.Category != "" && !adminstore.ValidTicketCategory(filter.Category) {
		writeError(response, http.StatusBadRequest, "invalid_ticket_filter", "That category value is not recognized.")
		return
	}
	if len(filter.Assigned) > 128 {
		writeError(response, http.StatusBadRequest, "invalid_ticket_filter", "The assignment filter is too long.")
		return
	}
	tickets, total, err := a.config.Admin.Tickets(request.Context(), filter, page, size)
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"tickets": tickets, "page": page, "size": size, "total": total})
}

func (a *api) adminSupportTicketRoute(response http.ResponseWriter, request *http.Request) {
	parts := adminPathParts(request.URL.Path, "/v1/admin/support/")
	if len(parts) == 0 || parts[0] == "" {
		a.notFound(response, request)
		return
	}
	ticketID := parts[0]

	if len(parts) == 1 && request.Method == http.MethodGet {
		actor, ok := a.adminForRequest(response, request)
		if !ok {
			return
		}
		if !adminstore.Allowed(actor.Role, adminstore.PermissionSupportRead) {
			writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view support tickets.")
			return
		}
		ticket, err := a.config.Admin.Ticket(request.Context(), ticketID)
		if err != nil {
			adminStoreError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket})
		return
	}

	if len(parts) == 2 && parts[1] == "reply" && request.Method == http.MethodPost {
		actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionSupportManage)
		if !ok {
			return
		}
		var input ticketReplyRequest
		if !decodeAdminJSON(response, request, &input) {
			return
		}
		body := strings.TrimSpace(input.Body)
		if len(body) < 1 || len(body) > 8000 {
			writeError(response, http.StatusBadRequest, "invalid_reply", "The reply must be between 1 and 8,000 characters.")
			return
		}
		if containsSecretShapedText(body) {
			writeError(response, http.StatusBadRequest, "secret_shaped_content", "Remove passwords, codes, keys, tokens, and vault data before sending this reply.")
			return
		}
		prior, err := a.config.Admin.Ticket(request.Context(), ticketID)
		if err != nil {
			adminStoreError(response, err)
			return
		}
		sendEmail := a.supportReplyEmailEnabled(request.Context(), prior.AccountID)
		ticket, err := a.config.Admin.ReplyTicket(request.Context(), actor, ticketID, body, sendEmail, a.adminIPHash(request))
		if err != nil {
			adminStoreError(response, err)
			return
		}
		if sendEmail && len(ticket.Messages) > 0 && a.config.EmailSender != nil {
			messageID := ticket.Messages[len(ticket.Messages)-1].ID
			_ = a.config.EmailSender.SendAccountEmail(request.Context(), AccountEmail{
				Kind: "support-reply", To: ticket.Email,
				Subject:          "Sesame support replied to your request",
				Body:             "A Sesame support specialist replied to your request. Sign in to the support portal to read the reply.",
				ActionURL:        strings.TrimRight(a.config.WebBaseURL, "/") + "/support",
				SupportMessageID: messageID,
				ExpiresAt:        time.Now().UTC().Add(7 * 24 * time.Hour),
			})
		}
		writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket})
		return
	}

	if len(parts) == 2 && parts[1] == "notes" && request.Method == http.MethodPost {
		actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionSupportManage)
		if !ok {
			return
		}
		var input ticketNoteRequest
		if !decodeAdminJSON(response, request, &input) {
			return
		}
		body := strings.TrimSpace(input.Body)
		if len(body) < 1 || len(body) > 4000 {
			writeError(response, http.StatusBadRequest, "invalid_note", "The note must be between 1 and 4,000 characters.")
			return
		}
		if containsSecretShapedText(body) {
			writeError(response, http.StatusBadRequest, "secret_shaped_content", "Remove passwords, codes, keys, tokens, and vault data before saving this note.")
			return
		}
		note, err := a.config.Admin.AddTicketNote(request.Context(), actor, ticketID, body, a.adminIPHash(request))
		if err != nil {
			adminStoreError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, map[string]any{"note": note})
		return
	}

	if len(parts) == 2 && parts[1] == "assign" && request.Method == http.MethodPost {
		actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionSupportManage)
		if !ok {
			return
		}
		var input ticketAssignRequest
		if !decodeAdminJSON(response, request, &input) || len(input.AdminID) > 128 {
			writeError(response, http.StatusBadRequest, "invalid_assign", "Use a valid admin ID or an empty string to unassign.")
			return
		}
		if err := a.config.Admin.AssignTicket(request.Context(), actor, ticketID, input.AdminID, a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}

	if len(parts) == 2 && parts[1] == "status" && request.Method == http.MethodPost {
		actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionSupportManage)
		if !ok {
			return
		}
		var input ticketStatusRequest
		if !decodeAdminJSON(response, request, &input) || !adminstore.ValidTicketStatus(input.Status) {
			writeError(response, http.StatusBadRequest, "invalid_status", "Use one of: open, in_progress, waiting, closed.")
			return
		}
		if err := a.config.Admin.SetTicketStatus(request.Context(), actor, ticketID, adminstore.TicketStatus(input.Status), a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}

	if len(parts) == 2 && parts[1] == "priority" && request.Method == http.MethodPost {
		actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionSupportManage)
		if !ok {
			return
		}
		var input ticketPriorityRequest
		if !decodeAdminJSON(response, request, &input) || !adminstore.ValidTicketPriority(input.Priority) {
			writeError(response, http.StatusBadRequest, "invalid_priority", "Use one of: low, normal, high, urgent.")
			return
		}
		if err := a.config.Admin.SetTicketPriority(request.Context(), actor, ticketID, adminstore.TicketPriority(input.Priority), a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}

	a.notFound(response, request)
}

// Only id and email, and only for roles AssignTicket would accept as a target.
func (a *api) adminSupportAssignees(response http.ResponseWriter, request *http.Request) {
	if !allowMethod(response, request, http.MethodGet) {
		return
	}
	actor, ok := a.adminForRequest(response, request)
	if !ok {
		return
	}
	if !adminstore.Allowed(actor.Role, adminstore.PermissionSupportRead) {
		writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view support assignments.")
		return
	}
	admins, err := a.config.Admin.Admins(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	type assignee struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	}
	assignees := make([]assignee, 0, len(admins))
	for _, admin := range admins {
		if admin.Role != adminstore.RoleSuper && admin.Role != adminstore.RoleSupport {
			continue
		}
		assignees = append(assignees, assignee{ID: admin.ID, Email: admin.Email})
	}
	writeJSON(response, http.StatusOK, map[string]any{"assignees": assignees})
}

func (a *api) supportReplyEmailEnabled(ctx context.Context, accountID string) bool {
	if accountID == "" || a.config.EmailSender == nil {
		return false
	}
	store, ok := a.config.Accounts.(accounts.NotificationPreferencesStore)
	if !ok {
		return false
	}
	preferences, err := store.NotificationPreferences(ctx, accountID)
	return err == nil && preferences.SupportReplies
}
