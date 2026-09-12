package main

import (
	"testing"

	"github.com/go-webauthn/webauthn/protocol"
)

func TestPasskeyConfigurationRequiresUserVerification(t *testing.T) {
	wa := buildPasskeys("https://account.example.invalid", "account.example.invalid", "Fictional account")
	if wa == nil {
		t.Fatal("passkeys unavailable")
	}
	_, session, err := wa.BeginDiscoverableLogin()
	if err != nil {
		t.Fatal(err)
	}
	if session.UserVerification != protocol.VerificationRequired {
		t.Fatal("login must require user verification")
	}
}
