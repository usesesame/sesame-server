package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	adminstore "usesesame.app/backend/internal/admin"
)

type adminMutatingRoute struct {
	method         string
	pattern        string
	requiresStepUp bool
}

var adminMutatingRoutes = []adminMutatingRoute{
	{http.MethodPost, "/v1/admin/auth/login", false},
	{http.MethodPost, "/v1/admin/auth/logout", false},
	{http.MethodPost, "/v1/admin/auth/step-up", false},
	{http.MethodPost, "/v1/admin/auth/setup/begin", false},
	{http.MethodPost, "/v1/admin/auth/setup/complete", false},
	{http.MethodDelete, "/v1/admin/users/{accountID}", true},
	{http.MethodPost, "/v1/admin/users/{accountID}/owner-release", true},
	{http.MethodDelete, "/v1/admin/users/{accountID}/owner-release", true},
	{http.MethodPost, "/v1/admin/users/{accountID}/beta", true},
	{http.MethodDelete, "/v1/admin/users/{accountID}/beta", true},
	{http.MethodPost, "/v1/admin/users/{accountID}/suspend", true},
	{http.MethodDelete, "/v1/admin/users/{accountID}/suspend", true},
	{http.MethodDelete, "/v1/admin/users/{accountID}/sessions", true},
	{http.MethodDelete, "/v1/admin/users/{accountID}/devices/{deviceID}", true},
	{http.MethodPatch, "/v1/admin/flags/{key}", true},
	{http.MethodPost, "/v1/admin/releases/{releaseID}/publish", true},
	{http.MethodPost, "/v1/admin/releases/{releaseID}/rollout", true},
	{http.MethodPost, "/v1/admin/releases/{releaseID}/emergency-stop", true},
	{http.MethodPost, "/v1/admin/releases/{releaseID}/withdraw", true},
	{http.MethodPost, "/v1/admin/extension-publications", true},
	{http.MethodPost, "/v1/admin/extension-publications/{publicationID}/transition", true},
	{http.MethodPatch, "/v1/admin/plans/{planID}", true},
	{http.MethodPost, "/v1/admin/admins", true},
	{http.MethodDelete, "/v1/admin/admins/{adminID}", true},
	{http.MethodPatch, "/v1/admin/admins/{adminID}", true},
	{http.MethodPost, "/v1/admin/support/{ticketID}/reply", false},
	{http.MethodPost, "/v1/admin/support/{ticketID}/notes", false},
	{http.MethodPost, "/v1/admin/support/{ticketID}/assign", false},
	{http.MethodPost, "/v1/admin/support/{ticketID}/status", false},
	{http.MethodPost, "/v1/admin/support/{ticketID}/priority", false},
	{http.MethodPost, "/v1/admin/saved-replies", false},
	{http.MethodPatch, "/v1/admin/saved-replies/{replyID}", false},
	{http.MethodDelete, "/v1/admin/saved-replies/{replyID}", false},
}

func TestAdminMutatingRoutesHaveStepUpDecisions(t *testing.T) {
	service := &api{config: Config{DeploymentProfile: DeploymentProfileProject}, routes: newRouteRegistry()}
	service.registerAdminRoutes(http.NewServeMux())

	registered := make(map[string]struct{})
	for pattern := range service.routes.policies {
		method, bare, ok := strings.Cut(pattern, " ")
		if !ok || method == http.MethodOptions || !isUnsafeMethod(method) || !strings.HasPrefix(bare, "/v1/admin/") {
			continue
		}
		registered[pattern] = struct{}{}
	}

	decisions := make(map[string]adminMutatingRoute)
	for _, route := range adminMutatingRoutes {
		pattern := route.method + " " + route.pattern
		if _, duplicate := decisions[pattern]; duplicate {
			t.Fatalf("duplicate step-up decision for %s", pattern)
		}
		decisions[pattern] = route
	}
	for pattern := range registered {
		if _, decided := decisions[pattern]; !decided {
			t.Fatalf("mutating admin route %s has no step-up decision in adminMutatingRoutes", pattern)
		}
	}
	for pattern := range decisions {
		if _, ok := registered[pattern]; !ok {
			t.Fatalf("step-up decision %s does not match a registered mutating admin route", pattern)
		}
	}
}

func TestAdminMutatingRouteStepUpDecisionsMatchBehavior(t *testing.T) {
	env := newAdminStepUpEnv(t)
	actor := seedStepUpAdmin(t, env, "admin-step-up-routes", "admin-step-up-routes@example.invalid", adminstore.RoleSuper)

	for _, route := range adminMutatingRoutes {
		t.Run(route.method+" "+route.pattern, func(t *testing.T) {
			token := stepUpSession(t, env, actor, time.Now().UTC().Add(-10*time.Minute), time.Now().UTC().UnixNano())
			path := adminRouteTestPath(route.pattern)
			response := adminSessionRequest(t, env, token, route.method, path, nil)
			stepUpRequired := response.Code == http.StatusForbidden && errorCode(t, response) == "admin_step_up_required"
			if stepUpRequired != route.requiresStepUp {
				t.Fatalf("%s %s requires step-up = %v, want %v: %d %s", route.method, path, stepUpRequired, route.requiresStepUp, response.Code, response.Body.String())
			}
		})
	}
}

func adminRouteTestPath(pattern string) string {
	parts := strings.Split(pattern, "/")
	for index, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			parts[index] = "route-test-" + part[1:len(part)-1]
		}
	}
	return strings.Join(parts, "/")
}
