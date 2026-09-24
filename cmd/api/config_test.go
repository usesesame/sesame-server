package main

import "testing"

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
