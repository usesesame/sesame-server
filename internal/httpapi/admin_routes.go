package httpapi

import (
	"context"
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"usesesame.app/backend/internal/accounts"
	adminstore "usesesame.app/backend/internal/admin"
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

func (a *api) adminUserView(response http.ResponseWriter, request *http.Request) {
	accountID := request.PathValue("accountID")
	if accountID == "" {
		a.notFound(response, request)
		return
	}
	if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersRead); !ok {
		return
	}
	user, err := a.config.Admin.User(request.Context(), accountID)
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"user": user})
}

func (a *api) adminUserDelete(response http.ResponseWriter, request *http.Request) {
	accountID := request.PathValue("accountID")
	if accountID == "" {
		a.notFound(response, request)
		return
	}
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersDelete)
	if !ok {
		return
	}
	if err := a.config.Admin.DeleteUser(request.Context(), actor, accountID, a.adminIPHash(request)); err != nil {
		adminStoreError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) adminUserAction(action string, enable bool) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		accountID := request.PathValue("accountID")
		if accountID == "" {
			a.notFound(response, request)
			return
		}
		switch action {
		case "owner-release":
			actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
			if !ok {
				return
			}
			if err := a.config.Admin.SetOwnerReleaseRingMember(request.Context(), actor, accountID, enable, a.adminIPHash(request)); err != nil {
				adminStoreError(response, err)
				return
			}
		case "beta":
			actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersManage)
			if !ok {
				return
			}
			if err := a.config.Admin.SetBeta(request.Context(), actor, accountID, enable, a.adminIPHash(request)); err != nil {
				adminStoreError(response, err)
				return
			}
		case "suspend":
			actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersManage)
			if !ok {
				return
			}
			reason := ""
			if enable {
				var input suspendUserRequest
				if !decodeAdminJSON(response, request, &input) || !safeReason(input.Reason) {
					writeError(response, http.StatusBadRequest, "invalid_suspend_reason", "Use an ASCII reason of at most 200 characters.")
					return
				}
				reason = strings.TrimSpace(input.Reason)
			}
			if err := a.config.Admin.SetSuspended(request.Context(), actor, accountID, enable, reason, a.adminIPHash(request)); err != nil {
				adminStoreError(response, err)
				return
			}
		case "sessions":
			actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersManage)
			if !ok {
				return
			}
			if err := a.config.Admin.RevokeUserSessions(request.Context(), actor, accountID, a.adminIPHash(request)); err != nil {
				adminStoreError(response, err)
				return
			}
		case "devices":
			deviceID := request.PathValue("deviceID")
			if deviceID == "" {
				a.notFound(response, request)
				return
			}
			actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionUsersManage)
			if !ok {
				return
			}
			if err := a.config.Admin.RevokeUserDevice(request.Context(), actor, accountID, deviceID, a.adminIPHash(request)); err != nil {
				adminStoreError(response, err)
				return
			}
		default:
			a.notFound(response, request)
			return
		}
		response.WriteHeader(http.StatusNoContent)
	}
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
	account, ok := a.adminForRequest(response, request)
	if !ok || !(adminstore.Allowed(account.Role, adminstore.PermissionFlagsManage) || account.Role == adminstore.RoleReadonly) {
		if ok {
			writeError(response, http.StatusForbidden, "admin_forbidden", "Your admin role cannot view feature flags.")
		}
		return
	}
	flags, err := a.visibleFeatureFlags(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"flags": flags})
}

func (a *api) adminFlag(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionFlagsManage)
	if !ok {
		return
	}
	var input featureFlagRequest
	key := request.PathValue("key")
	if !decodeAdminJSON(response, request, &input) || !a.validFeatureFlag(key, input.Value) {
		writeError(response, http.StatusBadRequest, "invalid_feature_flag", "That feature flag value is not allowed.")
		return
	}
	if err := a.config.Admin.UpdateFeatureFlag(request.Context(), actor, key, input.Value, a.adminIPHash(request)); err != nil {
		adminStoreError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

var operatorHiddenFeatureFlags = map[string]struct{}{
	"public_download": {},
	"updater_enabled": {},
}

func (a *api) visibleFeatureFlags(ctx context.Context) ([]adminstore.FeatureFlag, error) {
	flags, err := a.config.Admin.FeatureFlags(ctx)
	if err != nil {
		return nil, err
	}
	if a.projectProfile() {
		return flags, nil
	}
	visible := make([]adminstore.FeatureFlag, 0, len(flags))
	for _, flag := range flags {
		if _, hidden := operatorHiddenFeatureFlags[flag.Key]; hidden {
			continue
		}
		visible = append(visible, flag)
	}
	return visible, nil
}

func (a *api) validFeatureFlag(key, value string) bool {
	switch key {
	case "registration_mode":
		return value == "closed" || value == "invite" || value == "public"
	case "cloud_sync_available", "public_download", "desktop_linking_enabled", "downloads_enabled", "updater_enabled":
		if _, hidden := operatorHiddenFeatureFlags[key]; hidden && !a.projectProfile() {
			return false
		}
		return value == "true" || value == "false"
	default:
		return false
	}
}

func (a *api) adminPlans(response http.ResponseWriter, request *http.Request) {
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
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionPlansWrite)
	if !ok {
		return
	}
	var input adminstore.Plan
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	input.ID = request.PathValue("planID")
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

func (a *api) adminReleasePublish(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
	if !ok {
		return
	}
	var input adminstore.PublishReleaseInput
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	if err := a.config.Admin.PublishRelease(request.Context(), actor, request.PathValue("releaseID"), input, a.adminIPHash(request)); err != nil {
		adminReleaseCommandError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) adminReleaseRollout(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
	if !ok {
		return
	}
	var input adminstore.RolloutReleaseInput
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	if err := a.config.Admin.SetReleaseRollout(request.Context(), actor, request.PathValue("releaseID"), input, a.adminIPHash(request)); err != nil {
		adminReleaseCommandError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) adminReleaseEmergencyStop(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
	if !ok {
		return
	}
	var input adminstore.EmergencyStopReleaseInput
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	if err := a.config.Admin.EmergencyStopRelease(request.Context(), actor, request.PathValue("releaseID"), input, a.adminIPHash(request)); err != nil {
		adminReleaseCommandError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) adminReleaseWithdraw(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionReleaseWrite)
	if !ok {
		return
	}
	var input adminstore.WithdrawReleaseInput
	if !decodeAdminJSON(response, request, &input) {
		return
	}
	if err := a.config.Admin.WithdrawRelease(request.Context(), actor, request.PathValue("releaseID"), input, a.adminIPHash(request)); err != nil {
		adminReleaseCommandError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func adminReleaseCommandError(response http.ResponseWriter, err error) {
	if errors.Is(err, adminstore.ErrManifestRevisionConflict) {
		writeError(response, http.StatusConflict, "release_manifest_conflict", "This release changed. Reload it before trying again.")
		return
	}
	adminStoreError(response, err)
}

func (a *api) adminAccounts(response http.ResponseWriter, request *http.Request) {
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
}

func (a *api) inviteAdminAccount(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionAdminsManage)
	if !ok {
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

func (a *api) deleteAdminAccount(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionAdminsManage)
	if !ok {
		return
	}
	if err := a.config.Admin.DeleteAdmin(request.Context(), actor, request.PathValue("adminID"), a.adminIPHash(request)); err != nil {
		adminStoreError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) updateAdminAccount(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionAdminsManage)
	if !ok {
		return
	}
	var input updateAdminRequest
	if !decodeAdminJSON(response, request, &input) || !adminstore.ValidRole(input.Role) {
		writeError(response, http.StatusBadRequest, "invalid_admin_update", "Use a valid admin role and suspension state.")
		return
	}
	if err := a.config.Admin.UpdateAdmin(request.Context(), actor, request.PathValue("adminID"), input.Role, input.Suspended, a.adminIPHash(request)); err != nil {
		adminStoreError(response, err)
		return
	}
	response.WriteHeader(http.StatusNoContent)
}

func (a *api) adminAudit(response http.ResponseWriter, request *http.Request) {
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionAuditAll)
	if !ok {
		return
	}
	a.writeAdminAudit(response, request, actor, true)
}

func (a *api) adminAuditMe(response http.ResponseWriter, request *http.Request) {
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
	if err := writer.Error(); err != nil {
		slog.Error("Sesame admin audit export failed", "error", err)
	}
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
	if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionSystemRead); !ok {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), 2*time.Second)
	defer cancel()
	writeJSON(response, http.StatusOK, a.config.OperationalSnapshot.Snapshot(ctx))
}

func (a *api) adminRateLimits(response http.ResponseWriter, request *http.Request) {
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
	if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionSystemRead); !ok {
		return
	}
	flags, err := a.visibleFeatureFlags(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{
		"adminOrigin": a.config.AdminOrigin, "sessionTTLSeconds": int(a.config.AdminSessionTTL.Seconds()),
		"trustedProxyCount": len(a.config.TrustedProxies), "featureFlags": flags,
		"deploymentProfile":             a.config.DeploymentProfile,
		"supportNotifyEmailConfigured":  a.config.SupportNotifyEmail != "",
		"emailDeliveryConfigured":       a.config.EmailSender != nil,
		"trustedProxiesRuntimeEditable": false,
		"note":                          "Trusted proxy changes require a reviewed configuration change and restart.",
	})
}

func (a *api) adminOverview(response http.ResponseWriter, request *http.Request) {
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

func (a *api) adminSupportTicketView(response http.ResponseWriter, request *http.Request) {
	ticketID := request.PathValue("ticketID")
	if ticketID == "" {
		a.notFound(response, request)
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
	ticket, err := a.config.Admin.Ticket(request.Context(), ticketID)
	if err != nil {
		adminStoreError(response, err)
		return
	}
	a.enrichSupportDelivery(request.Context(), &ticket)
	writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket, "mail": a.supportMailState()})
}

func (a *api) adminSupportTicketAction(action string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		request.SetPathValue("action", action)
		a.adminSupportTicketRoute(response, request)
	}
}

func (a *api) adminSupportTicketRoute(response http.ResponseWriter, request *http.Request) {
	ticketID := request.PathValue("ticketID")
	action := request.PathValue("action")

	if action == "reply" {
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
		var email *adminstore.TicketReplyEmail
		var enqueue adminstore.TicketReplyEmailHook
		if prior.AccountID == "" {
			if a.config.EmailSender != nil {
				if _, ok := a.config.EmailSender.(TransactionalEmailSender); !ok {
					slog.Error("Sesame support link email requires a transactional email sender")
					writeError(response, http.StatusServiceUnavailable, "support_unavailable", "Support is temporarily unavailable.")
					return
				}
				token, tokenHash, err := accounts.NewSessionToken()
				if err != nil {
					slog.Error("Sesame support link token could not be generated", "error", err)
					writeError(response, http.StatusServiceUnavailable, "support_unavailable", "Support is temporarily unavailable.")
					return
				}
				expiresAt := time.Now().UTC().Add(7 * 24 * time.Hour)
				email = &adminstore.TicketReplyEmail{
					To:              prior.Email,
					Subject:         "Sesame support replied to your request",
					Body:            "Open the link to read the reply and answer in the same support thread.",
					ActionURL:       strings.TrimRight(a.config.WebBaseURL, "/") + "/support/request#token=" + url.QueryEscape(token),
					ExpiresAt:       expiresAt,
					AccessTokenHash: tokenHash,
				}
			}
		} else {
			sendEmail, err := a.supportReplyEmailEnabled(request.Context(), prior.AccountID)
			if err != nil {
				slog.Error("Sesame support reply email preference lookup failed", "error", err)
				writeError(response, http.StatusServiceUnavailable, "support_unavailable", "Support is temporarily unavailable.")
				return
			}
			if sendEmail {
				if _, ok := a.config.EmailSender.(TransactionalEmailSender); !ok {
					slog.Error("Sesame support reply email requires a transactional email sender")
					writeError(response, http.StatusServiceUnavailable, "support_unavailable", "Support is temporarily unavailable.")
					return
				}
				email = &adminstore.TicketReplyEmail{
					To:        prior.Email,
					Subject:   "Sesame support replied to your request",
					Body:      "A Sesame support specialist replied to your request. Sign in to the support portal to read the reply.",
					ActionURL: strings.TrimRight(a.config.WebBaseURL, "/") + "/support",
					ExpiresAt: time.Now().UTC().Add(7 * 24 * time.Hour),
				}
			}
		}
		if email != nil {
			sender := a.config.EmailSender.(TransactionalEmailSender)
			enqueue = func(ctx context.Context, tx *sql.Tx, messageID string) error {
				return sender.SendAccountEmailTx(ctx, tx, AccountEmail{
					Kind: "support-reply", To: email.To,
					Subject: email.Subject, Body: email.Body,
					ActionURL: email.ActionURL, ExpiresAt: email.ExpiresAt,
					SupportMessageID: messageID,
				})
			}
		}
		ticket, err := a.config.Admin.ReplyTicket(request.Context(), actor, ticketID, body, email, enqueue, a.adminIPHash(request))
		if err != nil {
			adminStoreError(response, err)
			return
		}
		a.enrichSupportDelivery(request.Context(), &ticket)
		writeJSON(response, http.StatusOK, map[string]any{"ticket": ticket})
		return
	}

	if action == "notes" {
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

	if action == "assign" {
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

	if action == "status" {
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

	if action == "priority" {
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

type savedReplyRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

const (
	savedReplyTitleMax = 120
	savedReplyBodyMax  = 8000
)

func (a *api) adminSupportSavedReplies(response http.ResponseWriter, request *http.Request) {
	if _, ok := a.requireAdminPermission(response, request, adminstore.PermissionSupportRead); !ok {
		return
	}
	replies, err := a.config.Admin.SavedReplies(request.Context())
	if err != nil {
		adminStoreError(response, err)
		return
	}
	writeJSON(response, http.StatusOK, map[string]any{"savedReplies": replies})
}

func (a *api) adminSupportSavedReplyAction(action string) http.HandlerFunc {
	return func(response http.ResponseWriter, request *http.Request) {
		request.SetPathValue("action", action)
		a.adminSupportSavedReplyRoute(response, request)
	}
}

func (a *api) adminSupportSavedReplyRoute(response http.ResponseWriter, request *http.Request) {
	action := request.PathValue("action")
	actor, ok := a.requireAdminPermission(response, request, adminstore.PermissionSupportManage)
	if !ok {
		return
	}
	if action == "create" {
		var input savedReplyRequest
		if !decodeAdminJSON(response, request, &input) {
			return
		}
		title, body, ok := validSavedReply(response, input.Title, input.Body)
		if !ok {
			return
		}
		reply, err := a.config.Admin.CreateSavedReply(request.Context(), actor, title, body, a.adminIPHash(request))
		if err != nil {
			adminStoreError(response, err)
			return
		}
		writeJSON(response, http.StatusCreated, map[string]any{"savedReply": reply})
		return
	}
	replyID := request.PathValue("replyID")
	if replyID == "" || len(replyID) > 128 {
		a.notFound(response, request)
		return
	}
	if action == "update" {
		var input savedReplyRequest
		if !decodeAdminJSON(response, request, &input) {
			return
		}
		title, body, ok := validSavedReply(response, input.Title, input.Body)
		if !ok {
			return
		}
		reply, err := a.config.Admin.UpdateSavedReply(request.Context(), actor, replyID, title, body, a.adminIPHash(request))
		if err != nil {
			adminStoreError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"savedReply": reply})
		return
	}
	if action == "delete" {
		if err := a.config.Admin.DeleteSavedReply(request.Context(), actor, replyID, a.adminIPHash(request)); err != nil {
			adminStoreError(response, err)
			return
		}
		response.WriteHeader(http.StatusNoContent)
		return
	}
	a.notFound(response, request)
}

func validSavedReply(response http.ResponseWriter, rawTitle, rawBody string) (string, string, bool) {
	title := strings.TrimSpace(rawTitle)
	body := strings.TrimSpace(rawBody)
	if len(title) < 1 || len(title) > savedReplyTitleMax || len(body) < 1 || len(body) > savedReplyBodyMax {
		writeError(response, http.StatusBadRequest, "invalid_saved_reply", "The saved reply needs a title under 121 characters and a body under 8,001 characters.")
		return "", "", false
	}
	if containsSecretShapedText(title + "\n" + body) {
		writeError(response, http.StatusBadRequest, "secret_shaped_content", "Remove passwords, codes, keys, tokens, and vault data before saving this reply.")
		return "", "", false
	}
	return title, body, true
}

// Only id and email, and only for roles AssignTicket would accept as a target.
func (a *api) adminSupportAssignees(response http.ResponseWriter, request *http.Request) {
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

func (a *api) supportReplyEmailEnabled(ctx context.Context, accountID string) (bool, error) {
	if accountID == "" || a.config.EmailSender == nil {
		return false, nil
	}
	store, ok := a.config.Accounts.(accounts.NotificationPreferencesStore)
	if !ok {
		return false, errors.New("account store does not expose notification preferences")
	}
	preferences, err := store.NotificationPreferences(ctx, accountID)
	if err != nil {
		return false, err
	}
	return preferences.SupportReplies, nil
}
