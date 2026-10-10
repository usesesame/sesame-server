package updates

import (
	"strings"
	"testing"
)

func TestDetectInstallKind(t *testing.T) {
	present := func(path string) bool { return path == "/.dockerenv" }
	absent := func(string) bool { return false }
	cases := []struct {
		name    string
		setting string
		exists  func(string) bool
		want    InstallKind
	}{
		{"setting container", "container", absent, InstallContainer},
		{"setting binary overrides the marker file", "binary", present, InstallBinary},
		{"setting is case insensitive", " Container ", absent, InstallContainer},
		{"marker file means container", "", present, InstallContainer},
		{"nothing means binary", "", absent, InstallBinary},
		{"unknown setting falls back to the marker file", "vm", present, InstallContainer},
		{"unknown setting falls back to binary", "vm", absent, InstallBinary},
		{"no probe means binary", "", nil, InstallBinary},
	}
	for _, tc := range cases {
		if got := DetectInstallKind(tc.setting, tc.exists); got != tc.want {
			t.Errorf("%s: got %s, want %s", tc.name, got, tc.want)
		}
	}
	if _, ok := ParseInstallKind("snap"); ok {
		t.Fatal("snap is not an install kind")
	}
}

func texts(commands []Command) []string {
	out := make([]string, len(commands))
	for index, command := range commands {
		out[index] = command.Text
	}
	return out
}

func TestContainerCommands(t *testing.T) {
	release := samplePayload().Products[0].Channels["stable"]
	commands := Commands(InstallContainer, release, Platform{})
	want := []string{"SESAME_IMAGE_TAG=0.2.0", "docker compose pull", pulledDigestCommand, "registry.example.net/sesame/server@sha256:" + testDigest, "docker compose up -d"}
	got := texts(commands)
	if len(got) != len(want) {
		t.Fatalf("commands = %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("command %d = %q, want %q", index, got[index], want[index])
		}
	}
	for _, command := range commands {
		if command.Label == "" {
			t.Errorf("command %q has no label", command.Text)
		}
	}
	release.Images = nil
	if got := texts(Commands(InstallContainer, release, Platform{})); len(got) != 4 || got[3] != "docker compose up -d" {
		t.Fatalf("commands without an image = %v", got)
	}
}

func TestBinaryCommands(t *testing.T) {
	release := samplePayload().Products[0].Channels["stable"]
	commands := Commands(InstallBinary, release, Platform{OS: "linux", Arch: "amd64", Executable: "/opt/sesame/bin/sesame-server"})
	want := []string{
		"curl -fsSLo sesame-server.new --proto '=https' --proto-redir '=https' 'https://downloads.example.net/sesame-server-linux-amd64'",
		"echo '" + testSHA + "  sesame-server.new' | sha256sum -c -",
		"sudo install -m 0755 sesame-server.new '/opt/sesame/bin/sesame-server'",
		"sudo systemctl restart sesame-server",
	}
	got := texts(commands)
	if len(got) != len(want) {
		t.Fatalf("commands = %v", got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Errorf("command %d = %q, want %q", index, got[index], want[index])
		}
	}
	darwin := texts(Commands(InstallBinary, release, Platform{OS: "darwin", Arch: "arm64"}))
	if len(darwin) != 4 || !strings.HasSuffix(darwin[1], "| shasum -a 256 -c -") || !strings.Contains(darwin[2], "'/usr/local/bin/sesame-server'") {
		t.Fatalf("darwin commands = %v", darwin)
	}
	if none := Commands(InstallBinary, release, Platform{OS: "linux", Arch: "riscv64"}); len(none) != 0 || none == nil {
		t.Fatalf("a platform without a download = %v", none)
	}
	quoted := texts(Commands(InstallBinary, release, Platform{OS: "linux", Arch: "amd64", Executable: "/opt/it's/sesame-server"}))
	if !strings.Contains(quoted[2], `'/opt/it'\''s/sesame-server'`) {
		t.Fatalf("an executable path with a quote = %q", quoted[2])
	}
}

func TestCommandsDoNotClaimMoreThanTheSignature(t *testing.T) {
	release := samplePayload().Products[0].Channels["stable"]
	for _, kind := range []InstallKind{InstallContainer, InstallBinary} {
		for _, command := range Commands(kind, release, Platform{OS: "linux", Arch: "amd64"}) {
			lower := strings.ToLower(command.Label)
			if strings.Contains(lower, "verified") || strings.Contains(lower, "trusted") || strings.Contains(lower, "safe") {
				t.Errorf("label %q claims more than the feed signature", command.Label)
			}
		}
	}
}

func TestVersionRules(t *testing.T) {
	newer := []struct{ candidate, current string }{
		{"0.2.0", "0.1.0"}, {"0.1.1", "0.1.0"}, {"1.0.0", "0.9.9"}, {"0.1.0", "0.1.0-dev"},
		{"0.1.0-rc.2", "0.1.0-rc.1"}, {"0.1.0-rc.10", "0.1.0-rc.9"}, {"0.1.0", "0.1.0-rc.1"}, {"0.10.0", "0.9.0"},
	}
	for _, tc := range newer {
		if !Newer(tc.candidate, tc.current) {
			t.Errorf("%s should be newer than %s", tc.candidate, tc.current)
		}
		if Newer(tc.current, tc.candidate) {
			t.Errorf("%s should not be newer than %s", tc.current, tc.candidate)
		}
	}
	for _, tc := range []struct{ candidate, current string }{{"0.1.0", "0.1.0"}, {"bad", "0.1.0"}, {"0.2.0", "dev"}, {"0.2.0", ""}, {"v0.2.0", "0.1.0"}} {
		if Newer(tc.candidate, tc.current) {
			t.Errorf("%q against %q must not be offered", tc.candidate, tc.current)
		}
	}
	prerelease := Release{Version: "0.3.0-beta.1"}
	if Offered(prerelease, ChannelStable) || !Offered(prerelease, ChannelBeta) || !Offered(Release{Version: "0.3.0"}, ChannelStable) || Offered(Release{Version: "x"}, ChannelBeta) {
		t.Fatal("prerelease offering rules are wrong")
	}
}
