package main

import (
	"testing"

	"usesesame.app/backend/internal/httpapi"
)

func TestValidateAdminOriginRefusesACollapsedPortalBoundary(t *testing.T) {
	if err := validateAdminOrigin("https://admin.example.invalid", "https://account.example.invalid"); err != nil {
		t.Fatalf("distinct origins must be accepted, got %v", err)
	}
	if err := validateAdminOrigin("", "https://account.example.invalid"); err != nil {
		t.Fatalf("an absent admin origin must be accepted, got %v", err)
	}
	if err := validateAdminOrigin("https://account.example.invalid", "https://account.example.invalid"); err == nil {
		t.Fatal("an admin origin equal to the account origin must be refused")
	}
}

func TestDeploymentProfileFromEnvironment(t *testing.T) {
	for value, expected := range map[string]httpapi.DeploymentProfile{
		"":         httpapi.DeploymentProfileOperator,
		"operator": httpapi.DeploymentProfileOperator,
		"project":  httpapi.DeploymentProfileProject,
	} {
		t.Setenv("SESAME_DEPLOYMENT_PROFILE", value)
		profile, err := deploymentProfileFromEnvironment()
		if err != nil || profile != expected {
			t.Fatalf("SESAME_DEPLOYMENT_PROFILE=%q = %q, %v; want %q", value, profile, err, expected)
		}
	}
	t.Setenv("SESAME_DEPLOYMENT_PROFILE", "staging")
	if _, err := deploymentProfileFromEnvironment(); err == nil {
		t.Fatal("an unknown deployment profile must stop startup")
	}
}
