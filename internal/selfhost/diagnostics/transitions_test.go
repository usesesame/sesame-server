//go:build linux

package diagnostics

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"usesesame.app/backend/internal/selfhost/config"
)

func TestCheckTransitions(t *testing.T) {
	cases := []struct {
		name   string
		setup  func(t *testing.T, f *fixture)
		files  func(t *testing.T, f *fixture) fileSystem
		id     string
		state  string
		code   string
		verify func(t *testing.T, report Report)
	}{
		{
			name:  "configuration valid",
			id:    "configuration",
			state: StateOK,
			code:  "configuration_valid",
		},
		{
			name: "origin local http",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvPublicURL] = "http://127.0.0.1:8787"
			},
			id:    "origin",
			state: StateOK,
			code:  "origin_local",
		},
		{
			name:  "listen loopback",
			id:    "listen",
			state: StateOK,
			code:  "listen_loopback",
		},
		{
			name: "listen all interfaces",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvAddr] = "0.0.0.0:8787"
			},
			id:    "listen",
			state: StateUnknown,
			code:  "listen_all_interfaces",
			verify: func(t *testing.T, report Report) {
				if report.Config == nil || report.Config.ListenScope != "all" {
					t.Fatalf("listen scope = %#v", report.Config)
				}
			},
		},
		{
			name: "listen private address",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvAddr] = "10.0.0.1:8787"
			},
			id:    "listen",
			state: StateUnknown,
			code:  "listen_non_loopback",
			verify: func(t *testing.T, report Report) {
				if report.Config == nil || report.Config.ListenScope != "private" {
					t.Fatalf("listen scope = %#v", report.Config)
				}
			},
		},
		{
			name: "listen public address",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvAddr] = "192.0.2.10:8787"
			},
			id:    "listen",
			state: StateUnknown,
			code:  "listen_non_loopback",
			verify: func(t *testing.T, report Report) {
				if report.Config == nil || report.Config.ListenScope != "public" {
					t.Fatalf("listen scope = %#v", report.Config)
				}
			},
		},
		{
			name:  "proxy empty https",
			id:    "proxy_trust",
			state: StateUnknown,
			code:  "proxy_trust_empty",
		},
		{
			name: "proxy empty local http",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvPublicURL] = "http://127.0.0.1:8787"
			},
			id:    "proxy_trust",
			state: StateOK,
			code:  "proxy_trust_empty",
		},
		{
			name: "proxy configured narrow",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvTrustedProxies] = "10.0.0.0/24"
			},
			id:    "proxy_trust",
			state: StateOK,
			code:  "proxy_trust_configured",
		},
		{
			name: "proxy wide ipv4",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvTrustedProxies] = "10.0.0.0/8"
			},
			id:    "proxy_trust",
			state: StateAction,
			code:  "proxy_trust_wide",
		},
		{
			name: "proxy wide ipv6",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvTrustedProxies] = "fd00::/8"
			},
			id:    "proxy_trust",
			state: StateAction,
			code:  "proxy_trust_wide",
		},
		{
			name: "proxy wide loopback accepted",
			setup: func(t *testing.T, f *fixture) {
				f.env[config.EnvTrustedProxies] = "127.0.0.0/8"
			},
			id:    "proxy_trust",
			state: StateOK,
			code:  "proxy_trust_configured",
		},
		{
			name: "config file absent",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Remove(f.configPath); err != nil {
					t.Fatal(err)
				}
			},
			id:    "config_file",
			state: StateOK,
			code:  "config_file_absent",
			verify: func(t *testing.T, report Report) {
				if report.Config == nil || report.Config.ConfigFile != "absent" {
					t.Fatalf("config file = %#v", report.Config)
				}
			},
		},
		{
			name:  "config file private",
			id:    "config_file",
			state: StateOK,
			code:  "config_file_private",
		},
		{
			name: "config file permissions",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Chmod(f.configPath, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			id:    "config_file",
			state: StateAction,
			code:  "config_file_permissions",
		},
		{
			name: "config file symlink",
			setup: func(t *testing.T, f *fixture) {
				target := filepath.Join(f.root, "config-target.json")
				if err := os.Rename(f.configPath, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, f.configPath); err != nil {
					t.Fatal(err)
				}
			},
			id:    "config_file",
			state: StateUnknown,
			code:  "config_file_symlink",
		},
		{
			name:  "data directory private",
			id:    "data_directory",
			state: StateOK,
			code:  "data_directory_private",
		},
		{
			name: "data directory missing",
			setup: func(t *testing.T, f *fixture) {
				if err := os.RemoveAll(f.dataDir); err != nil {
					t.Fatal(err)
				}
			},
			id:    "data_directory",
			state: StateUnknown,
			code:  "data_directory_missing",
		},
		{
			name: "data directory wrong type",
			setup: func(t *testing.T, f *fixture) {
				outside := filepath.Join(f.root, "outside-config.json")
				mustWrite(t, outside, []byte(`{"publicUrl":"https://vault.example.test"}`), 0o600)
				if err := os.RemoveAll(f.dataDir); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, f.dataDir, []byte("not a directory"), 0o600)
				f.env[config.EnvConfigFile] = outside
			},
			id:    "data_directory",
			state: StateAction,
			code:  "data_directory_wrong_type",
		},
		{
			name: "data directory symlink",
			setup: func(t *testing.T, f *fixture) {
				target := filepath.Join(f.root, "data-target")
				if err := os.Rename(f.dataDir, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, f.dataDir); err != nil {
					t.Fatal(err)
				}
			},
			id:    "data_directory",
			state: StateUnknown,
			code:  "data_directory_symlink",
			verify: func(t *testing.T, report Report) {
				assertCheck(t, report, "secrets", StateUnknown, "metadata_unavailable")
				assertCheck(t, report, "database", StateUnknown, "metadata_unavailable")
				assertCheck(t, report, "space", StateUnknown, "space_unavailable")
			},
		},
		{
			name: "data directory permissions",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Chmod(f.dataDir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			id:    "data_directory",
			state: StateAction,
			code:  "data_directory_permissions",
		},
		{
			name:  "secrets metadata ok",
			id:    "secrets",
			state: StateOK,
			code:  "secrets_metadata_ok",
		},
		{
			name: "secrets directory missing with database",
			setup: func(t *testing.T, f *fixture) {
				if err := os.RemoveAll(f.secretsDir); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_missing",
		},
		{
			name: "secrets directory missing without database",
			setup: func(t *testing.T, f *fixture) {
				if err := os.RemoveAll(f.secretsDir); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(f.databasePath); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateUnknown,
			code:  "secrets_uninitialized",
		},
		{
			name: "secret key missing with database",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Remove(filepath.Join(f.secretsDir, "signing.key")); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_missing",
		},
		{
			name: "secret key missing without database",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Remove(filepath.Join(f.secretsDir, "signing.key")); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(f.databasePath); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateUnknown,
			code:  "secrets_uninitialized",
		},
		{
			name: "secrets directory symlink",
			setup: func(t *testing.T, f *fixture) {
				target := filepath.Join(f.root, "secrets-target")
				if err := os.Rename(f.secretsDir, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, f.secretsDir); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_directory_unsafe",
		},
		{
			name: "secrets directory wrong type",
			setup: func(t *testing.T, f *fixture) {
				if err := os.RemoveAll(f.secretsDir); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, f.secretsDir, []byte("not a directory"), 0o600)
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_directory_unsafe",
		},
		{
			name: "secrets directory permissions",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Chmod(f.secretsDir, 0o755); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_directory_permissions",
		},
		{
			name: "secret key symlink",
			setup: func(t *testing.T, f *fixture) {
				source := filepath.Join(f.secretsDir, "signing.key")
				target := filepath.Join(f.root, "signing-target.key")
				if err := os.Rename(source, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, source); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_symlink",
		},
		{
			name: "secret key wrong type",
			setup: func(t *testing.T, f *fixture) {
				source := filepath.Join(f.secretsDir, "signing.key")
				if err := os.Remove(source); err != nil {
					t.Fatal(err)
				}
				mustMkdir(t, source, 0o700)
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_wrong_type",
		},
		{
			name: "secret key wrong size",
			setup: func(t *testing.T, f *fixture) {
				mustWrite(t, filepath.Join(f.secretsDir, "signing.key"), []byte("short"), 0o600)
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_size",
		},
		{
			name: "secret key oversized",
			setup: func(t *testing.T, f *fixture) {
				mustWrite(t, filepath.Join(f.secretsDir, "signing.key"), bytes.Repeat([]byte("x"), 33), 0o600)
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_size",
		},
		{
			name: "secret key permissions",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Chmod(filepath.Join(f.secretsDir, "signing.key"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateAction,
			code:  "secrets_permissions",
		},
		{
			name: "unreadable secret metadata only",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Chmod(filepath.Join(f.secretsDir, "signing.key"), 0o000); err != nil {
					t.Fatal(err)
				}
			},
			id:    "secrets",
			state: StateOK,
			code:  "secrets_metadata_ok",
		},
		{
			name:  "database not opened",
			id:    "database",
			state: StateUnknown,
			code:  "database_not_opened",
		},
		{
			name: "database missing",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Remove(f.databasePath); err != nil {
					t.Fatal(err)
				}
			},
			id:    "database",
			state: StateUnknown,
			code:  "database_missing",
		},
		{
			name: "database wrong type",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Remove(f.databasePath); err != nil {
					t.Fatal(err)
				}
				mustMkdir(t, f.databasePath, 0o700)
			},
			id:    "database",
			state: StateAction,
			code:  "database_wrong_type",
		},
		{
			name: "database empty",
			setup: func(t *testing.T, f *fixture) {
				mustWrite(t, f.databasePath, nil, 0o600)
			},
			id:    "database",
			state: StateAction,
			code:  "database_empty",
		},
		{
			name: "database permissions",
			setup: func(t *testing.T, f *fixture) {
				if err := os.Chmod(f.databasePath, 0o644); err != nil {
					t.Fatal(err)
				}
			},
			id:    "database",
			state: StateAction,
			code:  "database_permissions",
		},
		{
			name: "database symlink",
			setup: func(t *testing.T, f *fixture) {
				target := filepath.Join(f.root, "database-target.db")
				if err := os.Rename(f.databasePath, target); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, f.databasePath); err != nil {
					t.Fatal(err)
				}
			},
			id:    "database",
			state: StateAction,
			code:  "database_symlink",
		},
		{
			name:  "space sufficient",
			id:    "space",
			state: StateOK,
			code:  "space_sufficient",
		},
		{
			name: "space tight",
			files: func(t *testing.T, f *fixture) fileSystem {
				databaseSize := uint64(len(fictionalDatabase))
				return testFiles(saturatingAdd(saturatingMul(databaseSize, 8), 256*mebibyte)-1, nil)
			},
			id:    "space",
			state: StateUnknown,
			code:  "space_tight",
		},
		{
			name: "space critical",
			files: func(t *testing.T, f *fixture) fileSystem {
				databaseSize := uint64(len(fictionalDatabase))
				return testFiles(saturatingAdd(databaseSize, 256*mebibyte)-1, nil)
			},
			id:    "space",
			state: StateAction,
			code:  "space_critical",
		},
		{
			name: "space unavailable",
			files: func(t *testing.T, f *fixture) fileSystem {
				return testFiles(0, errSpaceUnavailable)
			},
			id:    "space",
			state: StateUnknown,
			code:  "space_unavailable",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			f := newFixture(t)
			if testCase.setup != nil {
				testCase.setup(t, f)
			}
			files := testFiles(1<<40, nil)
			if testCase.files != nil {
				files = testCase.files(t, f)
			}
			report := f.inspectWith(files)
			assertCheck(t, report, testCase.id, testCase.state, testCase.code)
			if testCase.verify != nil {
				testCase.verify(t, report)
			}
		})
	}
}
