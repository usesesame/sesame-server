package updates

import (
	"strings"
)

type InstallKind string

const (
	InstallContainer InstallKind = "container"
	InstallBinary    InstallKind = "binary"

	dockerEnvPath      = "/.dockerenv"
	defaultExecutable  = "/usr/local/bin/sesame-server"
	defaultServiceName = "sesame-server"
	downloadName       = "sesame-server.new"
	imageTagVariable   = "SESAME_IMAGE_TAG"

	pulledDigestCommand = `docker image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$(docker compose config --images)"`
)

func ParseInstallKind(value string) (InstallKind, bool) {
	switch InstallKind(strings.ToLower(strings.TrimSpace(value))) {
	case InstallContainer:
		return InstallContainer, true
	case InstallBinary:
		return InstallBinary, true
	}
	return "", false
}

func DetectInstallKind(setting string, exists func(path string) bool) InstallKind {
	if kind, ok := ParseInstallKind(setting); ok {
		return kind
	}
	if exists != nil && exists(dockerEnvPath) {
		return InstallContainer
	}
	return InstallBinary
}

type Command struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

type Platform struct {
	OS         string
	Arch       string
	Executable string
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func Commands(kind InstallKind, release Release, platform Platform) []Command {
	if kind == InstallContainer {
		return containerCommands(release)
	}
	return binaryCommands(release, platform)
}

func containerCommands(release Release) []Command {
	commands := []Command{
		{Label: "Set the new version in the settings your compose file reads", Text: imageTagVariable + "=" + release.Version},
		{Label: "Pull the image, in the directory that holds your compose file", Text: "docker compose pull"},
		{Label: "Print the digest of the image you pulled", Text: pulledDigestCommand},
	}
	if len(release.Images) > 0 {
		commands = append(commands, Command{Label: "Image named in the signed feed, to compare with the digest above", Text: release.Images[0].Ref})
	}
	return append(commands, Command{Label: "Start the server on the new image, in the same directory", Text: "docker compose up -d"})
}

func binaryCommands(release Release, platform Platform) []Command {
	var match *Binary
	for index := range release.Binaries {
		if release.Binaries[index].OS == platform.OS && release.Binaries[index].Arch == platform.Arch {
			match = &release.Binaries[index]
			break
		}
	}
	if match == nil {
		return []Command{}
	}
	executable := platform.Executable
	if executable == "" {
		executable = defaultExecutable
	}
	compare := "sha256sum -c -"
	if platform.OS == "darwin" {
		compare = "shasum -a 256 -c -"
	}
	return []Command{
		{Label: "Download the program", Text: "curl -fsSLo " + downloadName + " --proto '=https' --proto-redir '=https' " + shellQuote(match.URL)},
		{Label: "Compare it with the SHA-256 in the signed feed", Text: "echo '" + match.SHA256 + "  " + downloadName + "' | " + compare},
		{Label: "Replace the installed program", Text: "sudo install -m 0755 " + downloadName + " " + shellQuote(executable)},
		{Label: "Restart the service", Text: "sudo systemctl restart " + defaultServiceName},
	}
}
