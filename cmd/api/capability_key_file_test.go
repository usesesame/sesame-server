package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestCapabilitySigningKeyComesFromTheFileWhenOneIsNamed(t *testing.T) {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	encoded := base64.RawURLEncoding.EncodeToString(seed)
	getenv := func(name string) string {
		if name == "SESAME_CAPABILITY_SIGNING_KEY_FILE" {
			return "/run/secrets/capability_signing_key"
		}
		return ""
	}
	value, err := capabilitySigningKeyValue(getenv, func(path string) ([]byte, error) {
		if path != "/run/secrets/capability_signing_key" {
			t.Fatalf("read %q", path)
		}
		return []byte(encoded + "\n"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	key, err := capabilitySigningKey(value)
	if err != nil || !bytes.Equal(key.Seed(), seed) {
		t.Fatalf("key = %v, err = %v", key, err)
	}
}

func TestCapabilitySigningKeyKeepsTheEnvironmentValueWithoutAFile(t *testing.T) {
	value, err := capabilitySigningKeyValue(func(name string) string {
		if name == "SESAME_CAPABILITY_SIGNING_KEY" {
			return "inline-value"
		}
		return ""
	}, func(string) ([]byte, error) { t.Fatal("read a file nobody named"); return nil, nil })
	if err != nil || value != "inline-value" {
		t.Fatalf("value = %q, err = %v", value, err)
	}
}

func TestCapabilitySigningKeyRefusesBothSourcesAndBadFiles(t *testing.T) {
	both := func(name string) string { return "set-" + name }
	if _, err := capabilitySigningKeyValue(both, func(string) ([]byte, error) { return nil, nil }); err == nil || !strings.Contains(err.Error(), "not both") {
		t.Fatalf("both sources: %v", err)
	}
	fileOnly := func(name string) string {
		if name == "SESAME_CAPABILITY_SIGNING_KEY_FILE" {
			return "/missing"
		}
		return ""
	}
	if _, err := capabilitySigningKeyValue(fileOnly, func(string) ([]byte, error) { return nil, errors.New("open /missing: no such file") }); err == nil || strings.Contains(err.Error(), "/missing") {
		t.Fatalf("a missing file: %v", err)
	}
	if _, err := capabilitySigningKeyValue(fileOnly, func(string) ([]byte, error) { return make([]byte, 1025), nil }); err == nil {
		t.Fatal("an oversized file was accepted")
	}
}
