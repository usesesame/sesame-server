package diagnostics

import (
	"context"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
)

const (
	SchemaVersion = 1
	ModeOffline   = "offline"

	StateOK      = "ok"
	StateAction  = "action"
	StateUnknown = "unknown"
)

type Options struct {
	Lookup      config.Lookup
	ToolVersion string
	Now         func() time.Time
}

type Report struct {
	SchemaVersion int            `json:"schemaVersion"`
	ToolVersion   string         `json:"toolVersion"`
	GeneratedAt   string         `json:"generatedAt"`
	Mode          string         `json:"mode"`
	Overall       string         `json:"overall"`
	Config        *ConfigSummary `json:"config"`
	Checks        []Check        `json:"checks"`
	Limitations   []string       `json:"limitations"`
}

type ConfigSummary struct {
	PublicURL           string   `json:"publicUrl"`
	PublicURLSource     string   `json:"publicUrlSource"`
	ListenScope         string   `json:"listenScope"`
	ListenPort          int      `json:"listenPort"`
	TrustedProxies      []string `json:"trustedProxies"`
	DataDirectorySource string   `json:"dataDirectorySource"`
	ConfigFile          string   `json:"configFile"`
}

type Check struct {
	ID      string  `json:"id"`
	State   string  `json:"state"`
	Code    string  `json:"code"`
	Summary string  `json:"summary"`
	Next    *string `json:"next"`
}

var limitationCodes = []string{
	"clock_accuracy_not_verified",
	"database_not_opened",
	"network_not_probed",
	"port_availability_not_tested",
	"proxy_path_not_observed",
	"host_mapping_not_visible",
	"secrets_contents_not_read",
	"service_context_not_proven",
	"write_access_not_tested",
}

func Inspect(ctx context.Context, options Options) Report {
	return inspect(ctx, options, osFileSystem)
}
