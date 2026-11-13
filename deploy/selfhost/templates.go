package selfhostdeploy

import _ "embed"

//go:embed compose.yaml
var composeYAML string

//go:embed Caddyfile
var caddyfile string

func Compose() string { return composeYAML }

func Caddy() string { return caddyfile }
