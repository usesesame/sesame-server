package main

import (
	"context"
	"testing"

	"usesesame.app/backend/internal/httpapi"
)

func TestBuildEmailSenderRequiresTheEncryptionKey(t *testing.T) {
	t.Setenv("SESAME_SMTP_ADDR", "smtp.example.invalid:587")
	t.Setenv("SESAME_SMTP_FROM", "Sesame <accounts@example.invalid>")
	sender, outbox, worker, err := buildEmailSender(context.Background(), nil, nil)
	if err == nil || sender != nil || outbox != nil || worker != nil {
		t.Fatalf("buildEmailSender without a key = %v, %v, %v, %v; want a refusal", sender, outbox, worker, err)
	}
}

func TestBuildEmailSenderStaysOffWithoutSMTP(t *testing.T) {
	t.Setenv("SESAME_SMTP_ADDR", "")
	sender, outbox, worker, err := buildEmailSender(context.Background(), nil, nil)
	if err != nil || sender != nil || outbox != nil || worker != nil {
		t.Fatalf("buildEmailSender without SMTP = %v, %v, %v, %v; want it disabled", sender, outbox, worker, err)
	}
}

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

func TestSupportNotifyAddressValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		want    string
		wantErr bool
	}{
		{name: "empty", value: "", want: "", wantErr: false},
		{name: "plain address", value: "support@example.invalid", want: "support@example.invalid", wantErr: false},
		{name: "display name", value: "Sesame Support <support@example.invalid>", want: "Sesame Support <support@example.invalid>", wantErr: false},
		{name: "trimmed", value: "  support@example.invalid  ", want: "support@example.invalid", wantErr: false},
		{name: "not an address", value: "not-an-address", want: "", wantErr: true},
		{name: "header injection", value: "support@example.invalid\r\nBcc: attacker@example.invalid", want: "", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := supportNotifyAddress(tc.value)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("address = %q, want %q", got, tc.want)
			}
		})
	}
}
