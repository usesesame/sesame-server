package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func lookupFrom(values map[string]string) Lookup {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func writeFile(t *testing.T, path, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func loadWith(t *testing.T, env map[string]string, fileContent string) (Config, error) {
	t.Helper()
	dir := t.TempDir()
	merged := map[string]string{EnvDataDir: dir}
	for key, value := range env {
		merged[key] = value
	}
	if fileContent != "" {
		writeFile(t, filepath.Join(dir, DefaultConfigName), fileContent, 0o600)
	}
	return Load(lookupFrom(merged))
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(lookupFrom(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != "/data" || cfg.Addr != "127.0.0.1:8787" || cfg.PublicURL.Origin != "http://localhost:8787" || cfg.LogLevel != "info" || cfg.Metrics || cfg.BackupInterval != 24*time.Hour || cfg.SMTP.Enabled() || len(cfg.TrustedProxies) != 0 || cfg.ConfigFileLoaded {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.ConfigFile != "/data/config.json" {
		t.Fatalf("ConfigFile = %q", cfg.ConfigFile)
	}
	if cfg.PublicURL.Secure {
		t.Fatal("http loopback public URL reported as secure")
	}
	for _, setting := range cfg.Effective() {
		if setting.Source != SourceDefault {
			t.Fatalf("%s source = %s, want default", setting.Name, setting.Source)
		}
	}
}

func TestDefaultPublicURLFollowsTheListenPort(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{EnvAddr: "127.0.0.1:9090"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.PublicURL.Origin != "http://localhost:9090" || cfg.PublicURL.Host != "localhost:9090" {
		t.Fatalf("PublicURL = %+v", cfg.PublicURL)
	}
}

func TestDefaultPublicURLWarnsWhenListeningOnEveryInterface(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{EnvAddr: "0.0.0.0:8787"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], EnvPublicURL) {
		t.Fatalf("Warnings = %v", cfg.Warnings)
	}
	configured, err := loadWith(t, map[string]string{EnvAddr: "0.0.0.0:8787", EnvPublicURL: "https://sesame.example.net"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(configured.Warnings) != 0 {
		t.Fatalf("Warnings = %v", configured.Warnings)
	}
}

func TestEnvironmentOverridesFileOverridesDefaults(t *testing.T) {
	file := `{"addr":"127.0.0.1:9001","publicUrl":"https://file.example.net","logLevel":"debug","metrics":true,"backupInterval":"6h","trustedProxies":["10.0.0.0/24"]}`
	fromFile, err := loadWith(t, nil, file)
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.Addr != "127.0.0.1:9001" || fromFile.PublicURL.Origin != "https://file.example.net" || fromFile.LogLevel != "debug" || !fromFile.Metrics || fromFile.BackupInterval != 6*time.Hour || len(fromFile.TrustedProxies) != 1 {
		t.Fatalf("file values = %+v", fromFile)
	}
	if !fromFile.ConfigFileLoaded || fromFile.SourceOf(EnvAddr) != SourceConfigFile || fromFile.SourceOf(EnvSMTPAddr) != SourceDefault {
		t.Fatalf("sources = %+v", fromFile.sources)
	}

	overridden, err := loadWith(t, map[string]string{
		EnvAddr:           "127.0.0.1:9002",
		EnvPublicURL:      "https://env.example.net",
		EnvLogLevel:       "ERROR",
		EnvMetrics:        "false",
		EnvBackupInterval: "0",
		EnvTrustedProxies: "172.30.0.0/24",
	}, file)
	if err != nil {
		t.Fatal(err)
	}
	if overridden.Addr != "127.0.0.1:9002" || overridden.PublicURL.Origin != "https://env.example.net" || overridden.LogLevel != "error" || overridden.Metrics || overridden.BackupInterval != 0 || overridden.TrustedProxies[0].String() != "172.30.0.0/24" {
		t.Fatalf("env values = %+v", overridden)
	}
	if overridden.SourceOf(EnvAddr) != SourceEnvironment {
		t.Fatalf("source = %s", overridden.SourceOf(EnvAddr))
	}
}

func TestEmptyEnvironmentValueDoesNotMaskTheFile(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{EnvAddr: "  "}, `{"addr":"127.0.0.1:9100"}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Addr != "127.0.0.1:9100" {
		t.Fatalf("Addr = %q", cfg.Addr)
	}
}

func TestExplicitConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(t.TempDir(), "custom.json")
	writeFile(t, path, `{"logLevel":"warn"}`, 0o600)
	writeFile(t, filepath.Join(dir, DefaultConfigName), `{"logLevel":"debug"}`, 0o600)
	cfg, err := Load(lookupFrom(map[string]string{EnvDataDir: dir, EnvConfigFile: path}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LogLevel != "warn" || cfg.ConfigFile != path {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestMissingExplicitConfigFileIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent.json")
	_, err := Load(lookupFrom(map[string]string{EnvDataDir: t.TempDir(), EnvConfigFile: missing}))
	if err == nil || !strings.Contains(err.Error(), EnvConfigFile) || !strings.Contains(err.Error(), missing) {
		t.Fatalf("error = %v", err)
	}
}

func TestMissingDefaultConfigFileIsFine(t *testing.T) {
	cfg, err := loadWith(t, nil, "")
	if err != nil || cfg.ConfigFileLoaded {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestConfigFileRejections(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    string
	}{
		{"unknown key", `{"addr":"127.0.0.1:8787","bogus":1}`, `unknown key "bogus"`},
		{"unknown nested key", `{"smtp":{"addr":"mail.example.net:587","bogus":"x"}}`, `unknown key "bogus"`},
		{"retired data dir key", `{"dataDir":"/elsewhere"}`, `unknown key "dataDir"`},
		{"syntax error", `{"addr":`, "syntax error"},
		{"wrong type", `{"metrics":"yes"}`, `key "metrics" must be a bool`},
		{"wrong list type", `{"trustedProxies":"10.0.0.0/24"}`, `key "trustedProxies"`},
		{"not an object", `[]`, "config file"},
		{"trailing data", `{} {}`, "nothing after it"},
		{"empty file", " \n", "file is empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			content := tc.content
			if strings.TrimSpace(content) == "" {
				content = " \n"
			}
			path := filepath.Join(dir, DefaultConfigName)
			writeFile(t, path, content, 0o600)
			_, err := Load(lookupFrom(map[string]string{EnvDataDir: dir}))
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), path) {
				t.Fatalf("error = %v, want mention of %q and the path", err, tc.want)
			}
		})
	}
}

func TestConfigFileThatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, DefaultConfigName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(lookupFrom(map[string]string{EnvDataDir: dir})); err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v", err)
	}
}

func TestOversizedConfigFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, DefaultConfigName), `{"publicUrl":"`+strings.Repeat("a", maxConfigFileBytes)+`"}`, 0o600)
	if _, err := Load(lookupFrom(map[string]string{EnvDataDir: dir})); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("error = %v", err)
	}
}

func TestMalformedValuesNameTheVariable(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"address without a port", map[string]string{EnvAddr: "localhost"}, EnvAddr},
		{"address with a zero port", map[string]string{EnvAddr: "127.0.0.1:0"}, EnvAddr},
		{"address with a large port", map[string]string{EnvAddr: "127.0.0.1:70000"}, EnvAddr},
		{"address with a hostname", map[string]string{EnvAddr: "example.net:8787"}, EnvAddr},
		{"address with a padded port", map[string]string{EnvAddr: "127.0.0.1:08787"}, EnvAddr},
		{"public url without a scheme", map[string]string{EnvPublicURL: "sesame.example.net"}, EnvPublicURL},
		{"public url with a path", map[string]string{EnvPublicURL: "https://sesame.example.net/app"}, EnvPublicURL},
		{"public url with credentials", map[string]string{EnvPublicURL: "https://user:pw@sesame.example.net"}, EnvPublicURL},
		{"public url with a query", map[string]string{EnvPublicURL: "https://sesame.example.net/?a=b"}, EnvPublicURL},
		{"public url with a fragment", map[string]string{EnvPublicURL: "https://sesame.example.net/#x"}, EnvPublicURL},
		{"public url with a bad port", map[string]string{EnvPublicURL: "https://sesame.example.net:99999"}, EnvPublicURL},
		{"public url with another scheme", map[string]string{EnvPublicURL: "ftp://sesame.example.net"}, EnvPublicURL},
		{"public url that is http on a public host", map[string]string{EnvPublicURL: "http://sesame.example.net"}, EnvPublicURL},
		{"public url that is http on a lan address", map[string]string{EnvPublicURL: "http://192.168.1.20:8787"}, EnvPublicURL},
		{"public url that is http on a loopback lookalike", map[string]string{EnvPublicURL: "http://localhost.example.net"}, EnvPublicURL},
		{"public url that is http on another loopback address", map[string]string{EnvPublicURL: "http://127.0.0.2"}, EnvPublicURL},
		{"trusted proxy that is not cidr", map[string]string{EnvTrustedProxies: "10.0.0.1"}, EnvTrustedProxies},
		{"trusted proxy that is public", map[string]string{EnvTrustedProxies: "0.0.0.0/0"}, EnvTrustedProxies},
		{"log level unknown", map[string]string{EnvLogLevel: "verbose"}, EnvLogLevel},
		{"metrics unknown", map[string]string{EnvMetrics: "yes"}, EnvMetrics},
		{"backup interval unparsable", map[string]string{EnvBackupInterval: "daily"}, EnvBackupInterval},
		{"backup interval negative", map[string]string{EnvBackupInterval: "-1h"}, EnvBackupInterval},
		{"backup interval too short", map[string]string{EnvBackupInterval: "5s"}, EnvBackupInterval},
		{"smtp address without a port", map[string]string{EnvSMTPAddr: "mail.example.net", EnvSMTPFrom: "Sesame <sesame@example.net>"}, EnvSMTPAddr},
		{"smtp without a from address", map[string]string{EnvSMTPAddr: "mail.example.net:587"}, EnvSMTPFrom},
		{"smtp with a bad from address", map[string]string{EnvSMTPAddr: "mail.example.net:587", EnvSMTPFrom: "not an address"}, EnvSMTPFrom},
		{"smtp username without a password", map[string]string{EnvSMTPAddr: "mail.example.net:587", EnvSMTPFrom: "sesame@example.net", EnvSMTPUsername: "sender"}, EnvSMTPAuth},
		{"smtp password without a username", map[string]string{EnvSMTPAddr: "mail.example.net:587", EnvSMTPFrom: "sesame@example.net", EnvSMTPAuth: "pw"}, EnvSMTPUsername},
		{"smtp password with a line break", map[string]string{EnvSMTPAddr: "mail.example.net:587", EnvSMTPFrom: "sesame@example.net", EnvSMTPUsername: "sender", EnvSMTPAuth: "pw\r\nRCPT"}, EnvSMTPAuth},
		{"smtp settings without an address", map[string]string{EnvSMTPFrom: "sesame@example.net"}, EnvSMTPAddr},
		{"data dir with a NUL byte", map[string]string{EnvDataDir: "/data\x00x"}, EnvDataDir},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{}
			if _, set := tc.env[EnvDataDir]; !set {
				env[EnvDataDir] = t.TempDir()
			}
			for key, value := range tc.env {
				env[key] = value
			}
			_, err := Load(lookupFrom(env))
			if err == nil {
				t.Fatal("expected an error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not name %s", err, tc.want)
			}
			if strings.Contains(err.Error(), "scripts/") || strings.Contains(err.Error(), "npm run") {
				t.Fatalf("error %q names a script", err)
			}
		})
	}
}

func TestEveryProblemIsReportedTogether(t *testing.T) {
	_, err := loadWith(t, map[string]string{EnvAddr: "nope", EnvLogLevel: "loud", EnvMetrics: "maybe"}, "")
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, name := range []string{EnvAddr, EnvLogLevel, EnvMetrics} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("error %q does not name %s", err, name)
		}
	}
}

func TestFileProvenanceAppearsInErrors(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultConfigName)
	writeFile(t, path, `{"publicUrl":"http://sesame.example.net"}`, 0o600)
	_, err := Load(lookupFrom(map[string]string{EnvDataDir: dir}))
	if err == nil || !strings.Contains(err.Error(), `SESAME_PUBLIC_URL (key "publicUrl" in `+path+`)`) {
		t.Fatalf("error = %v", err)
	}
}

func TestEnvironmentValueFixesABadFileValue(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{EnvPublicURL: "https://sesame.example.net"}, `{"publicUrl":"http://sesame.example.net"}`)
	if err != nil || cfg.PublicURL.Origin != "https://sesame.example.net" {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestPublicURLRules(t *testing.T) {
	good := []struct {
		value  string
		origin string
		host   string
		secure bool
	}{
		{"https://sesame.example.net", "https://sesame.example.net", "sesame.example.net", true},
		{"https://Sesame.Example.NET/", "https://sesame.example.net", "sesame.example.net", true},
		{"https://sesame.example.net:443", "https://sesame.example.net", "sesame.example.net", true},
		{"https://sesame.example.net:8443", "https://sesame.example.net:8443", "sesame.example.net:8443", true},
		{"http://localhost:8787", "http://localhost:8787", "localhost:8787", false},
		{"http://localhost", "http://localhost", "localhost", false},
		{"http://localhost:80", "http://localhost", "localhost", false},
		{"http://127.0.0.1:9000", "http://127.0.0.1:9000", "127.0.0.1:9000", false},
		{"http://[::1]:8787", "http://[::1]:8787", "[::1]:8787", false},
		{"http://[::1]", "http://[::1]", "[::1]", false},
		{"HTTP://LOCALHOST:8787", "http://localhost:8787", "localhost:8787", false},
		{"https://192.168.1.20:8443", "https://192.168.1.20:8443", "192.168.1.20:8443", true},
	}
	for _, tc := range good {
		got, err := ParsePublicURL("SESAME_PUBLIC_URL", tc.value)
		if err != nil || got.Origin != tc.origin || got.Host != tc.host || got.Secure != tc.secure {
			t.Errorf("ParsePublicURL(%q) = %+v, %v; want origin %q host %q secure %v", tc.value, got, err, tc.origin, tc.host, tc.secure)
		}
	}
	bad := []string{"", "localhost:8787", "http://", "http://sesame.example.net", "http://10.0.0.5", "http://[fd00::1]", "https://", "https://sesame.example.net/x", "https://u@sesame.example.net", "https://sesame.example.net?x", "https://sesame.example.net#x", "https://sesame.example.net:0", "mailto:a@example.net", "javascript:alert(1)", "https://sesame.example.net:80:80", "http://localhost%2Eexample.net"}
	for _, value := range bad {
		if got, err := ParsePublicURL("SESAME_PUBLIC_URL", value); err == nil {
			t.Errorf("ParsePublicURL(%q) = %+v, want an error", value, got)
		}
	}
}

func TestSMTPFromEnvironmentAndFile(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{EnvSMTPAuth: " spaced pw "}, `{"smtp":{"addr":"mail.example.net:465","username":"sender","from":"Sesame <sesame@example.net>"}}`)
	if err != nil {
		t.Fatal(err)
	}
	want := SMTP{Addr: "mail.example.net:465", Username: "sender", Password: " spaced pw ", From: "Sesame <sesame@example.net>"}
	if cfg.SMTP != want || !cfg.SMTP.Enabled() {
		t.Fatalf("SMTP = %+v", cfg.SMTP)
	}
	if cfg.SourceOf(EnvSMTPAuth) != SourceEnvironment || cfg.SourceOf(EnvSMTPAddr) != SourceConfigFile {
		t.Fatalf("sources = %+v", cfg.sources)
	}
}

func TestSMTPPasswordInLooseFileWarns(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, DefaultConfigName)
	content := `{"smtp":{"addr":"mail.example.net:587","username":"sender","password":"pw","from":"sesame@example.net"}}`
	writeFile(t, path, content, 0o644)
	cfg, err := Load(lookupFrom(map[string]string{EnvDataDir: dir}))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Warnings) != 1 || !strings.Contains(cfg.Warnings[0], "readable by other users") {
		t.Fatalf("Warnings = %v", cfg.Warnings)
	}
	writeFile(t, path, content, 0o600)
	cfg, err = Load(lookupFrom(map[string]string{EnvDataDir: dir}))
	if err != nil || len(cfg.Warnings) != 0 {
		t.Fatalf("Warnings = %v, err = %v", cfg.Warnings, err)
	}
}

func TestEffectiveConfigRedactsSecrets(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{
		EnvSMTPAddr:     "mail.example.net:587",
		EnvSMTPFrom:     "sesame@example.net",
		EnvSMTPUsername: "sender-name",
		EnvSMTPAuth:     "correct horse battery staple",
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cfg.WriteEffective(&out); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, secret := range []string{"correct horse battery staple", "sender-name"} {
		if strings.Contains(text, secret) {
			t.Fatalf("effective config leaks %q:\n%s", secret, text)
		}
	}
	for _, want := range []string{"SESAME_SMTP_PASSWORD=[redacted] (environment)", "SESAME_SMTP_USERNAME=[redacted]", "SESAME_SMTP_ADDR=mail.example.net:587", "SESAME_ADDR=127.0.0.1:8787 (default)", "SESAME_BACKUP_INTERVAL=24h0m0s"} {
		if !strings.Contains(text, want) {
			t.Fatalf("effective config lacks %q:\n%s", want, text)
		}
	}
}

func TestEffectiveConfigShowsDisabledFeatures(t *testing.T) {
	cfg, err := loadWith(t, map[string]string{EnvBackupInterval: "0"}, "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := cfg.WriteEffective(&out); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"SESAME_SMTP_ADDR=disabled", "SESAME_BACKUP_INTERVAL=disabled", "SESAME_TRUSTED_PROXIES=none", "SESAME_CONFIG_FILE=none"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("effective config lacks %q:\n%s", want, out.String())
		}
	}
}

func TestSlogLevel(t *testing.T) {
	for name, want := range map[string]int{"debug": -4, "info": 0, "warn": 4, "error": 8} {
		cfg, err := loadWith(t, map[string]string{EnvLogLevel: name}, "")
		if err != nil || int(cfg.SlogLevel()) != want {
			t.Fatalf("%s: level = %d, err = %v", name, cfg.SlogLevel(), err)
		}
	}
}
