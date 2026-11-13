package diagnostics

import (
	"context"
	"net"
	"net/netip"
	"os"
	"strconv"
	"time"

	"usesesame.app/backend/internal/selfhost/config"
)

const (
	maxPublicOriginBytes = 2048
	maxProxyEntries      = 64
	maxToolVersionBytes  = 128
)

func inspect(ctx context.Context, options Options, files fileSystem) Report {
	if ctx == nil {
		ctx = context.Background()
	}
	files = normalizeFileSystem(files)
	now := options.Now
	if now == nil {
		now = time.Now
	}
	report := Report{
		SchemaVersion: SchemaVersion,
		ToolVersion:   safeToolVersion(options.ToolVersion),
		GeneratedAt:   now().UTC().Format(time.RFC3339),
		Mode:          ModeOffline,
		Limitations:   append([]string{}, limitationCodes...),
	}
	if interrupted(ctx) {
		return interruptedReport(report)
	}
	cfg, err := config.Load(effectiveLookup(options.Lookup))
	if interrupted(ctx) {
		return interruptedReport(report)
	}
	if err != nil {
		return invalidConfigurationReport(report)
	}
	summary, ok := buildConfigSummary(cfg)
	if !ok {
		return reportLimitReport(report)
	}
	report.Config = &summary
	scan := newScanner(cfg, summary, files)
	if interrupted(ctx) {
		return interruptedReport(report)
	}
	scan.readMetadata()
	if interrupted(ctx) {
		return interruptedReport(report)
	}
	checks := make([]Check, 0, 9)
	for _, evaluate := range []func() Check{
		scan.configurationCheck,
		scan.originCheck,
		scan.listenCheck,
		scan.proxyCheck,
		scan.configFileCheck,
		scan.dataDirectoryCheck,
		scan.secretsCheck,
		scan.databaseCheck,
		scan.spaceCheck,
	} {
		if interrupted(ctx) {
			return interruptedReport(report)
		}
		checks = append(checks, evaluate())
	}
	if interrupted(ctx) {
		return interruptedReport(report)
	}
	report.Checks = checks
	report.Overall = aggregate(checks)
	return report
}

func effectiveLookup(lookup config.Lookup) config.Lookup {
	if lookup != nil {
		return lookup
	}
	return func(string) (string, bool) { return "", false }
}

func interrupted(ctx context.Context) bool {
	return ctx.Err() != nil
}

func safeToolVersion(value string) string {
	if len(value) == 0 || len(value) > maxToolVersionBytes {
		return "unknown"
	}
	for i := 0; i < len(value); i++ {
		if value[i] < 0x20 || value[i] > 0x7e {
			return "unknown"
		}
	}
	return value
}

func buildConfigSummary(cfg config.Config) (ConfigSummary, bool) {
	if len(cfg.PublicURL.Origin) > maxPublicOriginBytes || len(cfg.TrustedProxies) > maxProxyEntries {
		return ConfigSummary{}, false
	}
	proxies := make([]string, 0, len(cfg.TrustedProxies))
	for _, prefix := range cfg.TrustedProxies {
		proxies = append(proxies, prefix.String())
	}
	scope, port := listenFacts(cfg.Addr)
	return ConfigSummary{
		PublicURL:           cfg.PublicURL.Origin,
		PublicURLSource:     sourceName(cfg.SourceOf(config.EnvPublicURL)),
		ListenScope:         scope,
		ListenPort:          port,
		TrustedProxies:      proxies,
		DataDirectorySource: sourceName(cfg.SourceOf(config.EnvDataDir)),
		ConfigFile:          configFileState(cfg.ConfigFileLoaded),
	}, true
}

func sourceName(source config.Source) string {
	switch source {
	case config.SourceEnvironment:
		return "environment"
	case config.SourceConfigFile:
		return "config_file"
	default:
		return "default"
	}
}

func configFileState(loaded bool) string {
	if loaded {
		return "loaded"
	}
	return "absent"
}

func listenFacts(addr string) (string, int) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return "other", 0
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		port = 0
	}
	return scopeName(host), port
}

func scopeName(host string) string {
	if host == "" {
		return "all"
	}
	if host == "localhost" {
		return "loopback"
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return "other"
	}
	switch {
	case addr.IsLoopback():
		return "loopback"
	case addr.IsUnspecified():
		return "all"
	case addr.IsPrivate():
		return "private"
	case addr.IsGlobalUnicast():
		return "public"
	default:
		return "other"
	}
}

func aggregate(checks []Check) string {
	overall := StateOK
	for _, check := range checks {
		if check.State == StateAction {
			return StateAction
		}
		if check.State == StateUnknown {
			overall = StateUnknown
		}
	}
	return overall
}

func interruptedReport(report Report) Report {
	report.Overall = StateUnknown
	report.Config = nil
	report.Checks = []Check{{
		ID:      "inspection",
		State:   StateUnknown,
		Code:    "interrupted",
		Summary: "The inspection was interrupted before it finished.",
		Next:    text("Run the doctor again when it can finish."),
	}}
	return report
}

func invalidConfigurationReport(report Report) Report {
	report.Overall = StateAction
	report.Config = nil
	report.Checks = []Check{{
		ID:      "configuration",
		State:   StateAction,
		Code:    "configuration_invalid",
		Summary: "The service configuration is invalid, and this report omits the error because it can contain a private path or credential.",
		Next:    text("Review the settings privately against the guide, then run the doctor again."),
	}}
	return report
}

func reportLimitReport(report Report) Report {
	report.Overall = StateAction
	report.Config = nil
	report.Checks = []Check{{
		ID:      "configuration",
		State:   StateAction,
		Code:    "configuration_report_limit",
		Summary: "The service configuration is valid but exceeds the limits for a safe report.",
		Next:    text("Review the public address and trusted proxy settings against the guide, then run the doctor again."),
	}}
	return report
}

func text(value string) *string {
	return &value
}

func normalizeFileSystem(files fileSystem) fileSystem {
	if files.lstat == nil {
		files.lstat = os.Lstat
	}
	if files.openDir == nil {
		files.openDir = openDirectory
	}
	if files.availableSpace == nil {
		files.availableSpace = directoryAvailableSpace
	}
	return files
}
