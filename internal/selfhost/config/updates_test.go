package config

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"usesesame.app/backend/internal/buildinfo"
	"usesesame.app/backend/internal/selfhost/updates"
)

func testKey(t *testing.T, id string) string {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return updates.FormatKey(id, public)
}

func withBuiltInKeys(t *testing.T, value string) {
	t.Helper()
	previous := buildinfo.UpdatePublicKeys
	buildinfo.UpdatePublicKeys = value
	t.Cleanup(func() { buildinfo.UpdatePublicKeys = previous })
}

func TestUpdateDefaults(t *testing.T) {
	withBuiltInKeys(t, "")
	cfg, err := loadWith(t, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Updates.FeedURL != updates.DefaultFeedURL || len(cfg.Updates.Keys) != 0 || cfg.Updates.InstallKind != "" {
		t.Fatalf("updates = %+v", cfg.Updates)
	}
	if !strings.HasPrefix(cfg.Updates.FeedURL, "https://") {
		t.Fatal("the default feed address is not https")
	}
}

func TestUpdateFeedURLSetting(t *testing.T) {
	withBuiltInKeys(t, "")
	cfg, err := loadWith(t, map[string]string{EnvUpdateFeedURL: "https://updates.example.net/feed.json"}, "")
	if err != nil || cfg.Updates.FeedURL != "https://updates.example.net/feed.json" || cfg.SourceOf(EnvUpdateFeedURL) != SourceEnvironment {
		t.Fatalf("updates = %+v, err = %v", cfg.Updates, err)
	}
	for _, bad := range []string{
		"http://updates.example.net/feed.json",
		"ftp://updates.example.net/feed.json",
		"https://user:pass@updates.example.net/feed.json",
		"https://updates.example.net/feed.json?channel=stable",
		"https://updates.example.net/feed.json#x",
		"updates.example.net/feed.json",
		"https:///feed.json",
	} {
		_, err := loadWith(t, map[string]string{EnvUpdateFeedURL: bad}, "")
		if err == nil || !strings.Contains(err.Error(), EnvUpdateFeedURL) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestUpdatePublicKeysSetting(t *testing.T) {
	withBuiltInKeys(t, "")
	first, second := testKey(t, "key-a"), testKey(t, "key-b")
	cfg, err := loadWith(t, map[string]string{EnvUpdatePublicKeys: first + "," + second}, "")
	if err != nil || len(cfg.Updates.Keys) != 2 || cfg.SourceOf(EnvUpdatePublicKeys) != SourceEnvironment {
		t.Fatalf("updates = %+v, err = %v", cfg.Updates, err)
	}
	for _, bad := range []string{"key-a", "key-a:short", first + "," + first, "bad id:" + strings.SplitN(first, ":", 2)[1]} {
		_, err := loadWith(t, map[string]string{EnvUpdatePublicKeys: bad}, "")
		if err == nil || !strings.Contains(err.Error(), EnvUpdatePublicKeys) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestBuiltInKeysApplyUntilTheEnvironmentReplacesThem(t *testing.T) {
	builtIn, override := testKey(t, "built-in"), testKey(t, "override")
	withBuiltInKeys(t, builtIn)
	cfg, err := loadWith(t, nil, "")
	if err != nil || len(cfg.Updates.Keys) != 1 || cfg.Updates.Keys["built-in"] == nil || cfg.SourceOf(EnvUpdatePublicKeys) != SourceDefault {
		t.Fatalf("updates = %+v, err = %v", cfg.Updates, err)
	}
	cfg, err = loadWith(t, map[string]string{EnvUpdatePublicKeys: override}, "")
	if err != nil || len(cfg.Updates.Keys) != 1 || cfg.Updates.Keys["override"] == nil || cfg.Updates.Keys["built-in"] != nil {
		t.Fatalf("updates = %+v, err = %v", cfg.Updates, err)
	}
	withBuiltInKeys(t, "garbage")
	if _, err := loadWith(t, nil, ""); err == nil || !strings.Contains(err.Error(), "compiled into this build") {
		t.Fatalf("invalid built in keys err = %v", err)
	}
}

func TestInstallKindSetting(t *testing.T) {
	withBuiltInKeys(t, "")
	for value, want := range map[string]string{"container": "container", "Binary": "binary", " container ": "container"} {
		cfg, err := loadWith(t, map[string]string{EnvInstallKind: value}, "")
		if err != nil || cfg.Updates.InstallKind != want {
			t.Errorf("%q: kind %q, err %v", value, cfg.Updates.InstallKind, err)
		}
	}
	for _, bad := range []string{"vm", "docker", "snap"} {
		_, err := loadWith(t, map[string]string{EnvInstallKind: bad}, "")
		if err == nil || !strings.Contains(err.Error(), EnvInstallKind) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestEffectiveConfigListsUpdateSettingsWithoutKeyMaterial(t *testing.T) {
	withBuiltInKeys(t, "")
	key := testKey(t, "key-a")
	cfg, err := loadWith(t, map[string]string{EnvUpdatePublicKeys: key, EnvInstallKind: "container"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cfg.WriteEffective(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"SESAME_UPDATE_FEED_URL=" + updates.DefaultFeedURL + " (default)", "SESAME_UPDATE_PUBLIC_KEYS=key-a (environment)", "SESAME_INSTALL_KIND=container (environment)"} {
		if !strings.Contains(text, want) {
			t.Errorf("effective config lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, strings.SplitN(key, ":", 2)[1]) {
		t.Errorf("effective config prints key material:\n%s", text)
	}
	cfg, err = loadWith(t, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	_ = cfg.WriteEffective(&out)
	for _, want := range []string{"SESAME_UPDATE_PUBLIC_KEYS=none (default)", "SESAME_INSTALL_KIND=detected (default)"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("effective config lacks %q:\n%s", want, out.String())
		}
	}
}
