package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/netip"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"usesesame.app/backend/internal/buildinfo"
	"usesesame.app/backend/internal/selfhost/updates"
)

const (
	EnvDataDir        = "SESAME_DATA_DIR"
	EnvConfigFile     = "SESAME_CONFIG_FILE"
	EnvAddr           = "SESAME_ADDR"
	EnvHostedAddr     = "SESAME_API_ADDR"
	EnvPublicURL      = "SESAME_PUBLIC_URL"
	EnvTrustedProxies = "SESAME_TRUSTED_PROXIES"
	EnvLogLevel       = "SESAME_LOG_LEVEL"
	EnvSMTPAddr       = "SESAME_SMTP_ADDR"
	EnvSMTPUsername   = "SESAME_SMTP_USERNAME"
	EnvSMTPAuth       = "SESAME_SMTP_PASSWORD"
	EnvSMTPFrom       = "SESAME_SMTP_FROM"
	EnvMetrics        = "SESAME_METRICS"
	EnvBackupInterval = "SESAME_BACKUP_INTERVAL"

	EnvUpdateFeedURL    = "SESAME_UPDATE_FEED_URL"
	EnvUpdatePublicKeys = "SESAME_UPDATE_PUBLIC_KEYS"
	EnvInstallKind      = "SESAME_INSTALL_KIND"

	DefaultDataDir        = "/data"
	DefaultConfigName     = "config.json"
	DefaultLogLevel       = "info"
	DefaultBackupInterval = 24 * time.Hour
	MinimumBackupInterval = time.Minute
)

type Source string

const (
	SourceDefault     Source = "default"
	SourceEnvironment Source = "environment"
	SourceConfigFile  Source = "config file"
)

type SMTP struct {
	Addr     string
	Username string
	Password string
	From     string
}

func (s SMTP) Enabled() bool { return s.Addr != "" }

type Updates struct {
	FeedURL     string
	Keys        updates.Keys
	InstallKind string
}

type Config struct {
	DataDir          string
	ConfigFile       string
	ConfigFileLoaded bool
	Addr             string
	PublicURL        PublicURL
	TrustedProxies   []netip.Prefix
	LogLevel         string
	SMTP             SMTP
	Metrics          bool
	BackupInterval   time.Duration
	Updates          Updates
	Warnings         []string

	sources map[string]Source
}

func (c Config) SlogLevel() slog.Level {
	level, _ := parseLogLevel(c.LogLevel)
	return level
}

func (c Config) SourceOf(name string) Source {
	if source, ok := c.sources[name]; ok {
		return source
	}
	return SourceDefault
}

func trimmed(value string) string { return strings.TrimSpace(value) }

func parseLogLevel(value string) (slog.Level, bool) {
	switch strings.ToLower(trimmed(value)) {
	case "debug":
		return slog.LevelDebug, true
	case "info":
		return slog.LevelInfo, true
	case "warn", "warning":
		return slog.LevelWarn, true
	case "error":
		return slog.LevelError, true
	}
	return slog.LevelInfo, false
}

type resolver struct {
	lookup   Lookup
	file     fileValues
	path     string
	sources  map[string]Source
	problems []error
}

func (r *resolver) value(name string) (string, Source) {
	if value, ok := r.lookup(name); ok && trimmed(value) != "" {
		return trimmed(value), SourceEnvironment
	}
	if value, ok := r.file[name]; ok && trimmed(value) != "" {
		return trimmed(value), SourceConfigFile
	}
	return "", SourceDefault
}

func (r *resolver) rawValue(name string) (string, Source) {
	if value, ok := r.lookup(name); ok && value != "" {
		return value, SourceEnvironment
	}
	if value, ok := r.file[name]; ok && value != "" {
		return value, SourceConfigFile
	}
	return "", SourceDefault
}

func (r *resolver) label(name string, source Source) string {
	if source == SourceConfigFile {
		return fmt.Sprintf("%s (key %q in %s)", name, fileKeys[name], r.path)
	}
	return name
}

func (r *resolver) fail(name string, source Source, err error) {
	message := err.Error()
	if source == SourceConfigFile && strings.HasPrefix(message, name) {
		message = r.label(name, source) + strings.TrimPrefix(message, name)
	}
	r.problems = append(r.problems, errors.New(message))
}

func Load(lookup Lookup) (Config, error) {
	r := &resolver{lookup: lookup, sources: map[string]Source{}}
	cfg := Config{sources: r.sources}

	dataDir := DefaultDataDir
	if value, ok := lookup(EnvDataDir); ok && trimmed(value) != "" {
		dataDir = trimmed(value)
		r.sources[EnvDataDir] = SourceEnvironment
	}
	if strings.ContainsRune(dataDir, 0) {
		return cfg, errors.New(EnvDataDir + " must not contain a NUL byte")
	}
	absolute, err := filepath.Abs(dataDir)
	if err != nil {
		return cfg, fmt.Errorf("%s is not a usable path: %w", EnvDataDir, err)
	}
	cfg.DataDir = absolute

	explicit := false
	configPath := filepath.Join(cfg.DataDir, DefaultConfigName)
	if value, ok := lookup(EnvConfigFile); ok && trimmed(value) != "" {
		if strings.ContainsRune(value, 0) {
			return cfg, errors.New(EnvConfigFile + " must not contain a NUL byte")
		}
		explicit = true
		configPath, err = filepath.Abs(trimmed(value))
		if err != nil {
			return cfg, fmt.Errorf("%s is not a usable path: %w", EnvConfigFile, err)
		}
		r.sources[EnvConfigFile] = SourceEnvironment
	}
	cfg.ConfigFile = configPath
	r.path = configPath
	values, loaded, fileWarnings, err := readConfigFile(configPath, explicit)
	if err != nil {
		return cfg, err
	}
	r.file = values
	cfg.ConfigFileLoaded = loaded
	cfg.Warnings = append(cfg.Warnings, fileWarnings...)

	r.loadAddr(&cfg)
	r.loadPublicURL(&cfg)
	r.loadTrustedProxies(&cfg)
	r.loadLogLevel(&cfg)
	r.loadSMTP(&cfg)
	r.loadMetrics(&cfg)
	r.loadBackupInterval(&cfg)
	r.loadUpdates(&cfg)

	if len(r.problems) > 0 {
		return Config{}, errors.Join(r.problems...)
	}
	return cfg, nil
}

func (r *resolver) loadAddr(cfg *Config) {
	value, source := r.value(EnvAddr)
	r.sources[EnvAddr] = source
	if value == "" {
		value = DefaultAddr
	}
	if err := ValidateListenAddress(value); err != nil {
		r.fail(EnvAddr, source, fmt.Errorf("%s %w", EnvAddr, err))
		return
	}
	cfg.Addr = value
}

func (r *resolver) loadPublicURL(cfg *Config) {
	value, source := r.value(EnvPublicURL)
	r.sources[EnvPublicURL] = source
	if value == "" {
		port := "8787"
		if cfg.Addr != "" {
			_, number, _ := splitListenAddress(cfg.Addr)
			port = strconv.Itoa(number)
		}
		value = "http://localhost:" + port
		if port == "80" {
			value = "http://localhost"
		}
		if cfg.Addr != "" && !loopbackListen(cfg.Addr) {
			cfg.Warnings = append(cfg.Warnings, EnvPublicURL+" is not set, so pairing links point at "+value+" and only work on this machine; set it to the https address people use to reach the server")
		}
	}
	parsed, err := ParsePublicURL(EnvPublicURL, value)
	if err != nil {
		r.fail(EnvPublicURL, source, err)
		return
	}
	cfg.PublicURL = parsed
}

func loopbackListen(addr string) bool {
	host, _, err := splitListenAddress(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	parsed, err := netip.ParseAddr(host)
	return err == nil && parsed.IsLoopback()
}

func (r *resolver) loadTrustedProxies(cfg *Config) {
	value, source := r.value(EnvTrustedProxies)
	r.sources[EnvTrustedProxies] = source
	prefixes, warnings, err := ParseTrustedProxies(EnvTrustedProxies, value)
	if err != nil {
		r.fail(EnvTrustedProxies, source, err)
		return
	}
	cfg.TrustedProxies = prefixes
	cfg.Warnings = append(cfg.Warnings, warnings...)
}

func (r *resolver) loadLogLevel(cfg *Config) {
	value, source := r.value(EnvLogLevel)
	r.sources[EnvLogLevel] = source
	if value == "" {
		value = DefaultLogLevel
	}
	if _, ok := parseLogLevel(value); !ok {
		r.fail(EnvLogLevel, source, errors.New(EnvLogLevel+" must be one of debug, info, warn or error"))
		return
	}
	cfg.LogLevel = strings.ToLower(value)
	if cfg.LogLevel == "warning" {
		cfg.LogLevel = "warn"
	}
}

func (r *resolver) loadSMTP(cfg *Config) {
	addr, addrSource := r.value(EnvSMTPAddr)
	from, fromSource := r.value(EnvSMTPFrom)
	username, usernameSource := r.value(EnvSMTPUsername)
	password, passwordSource := r.rawValue(EnvSMTPAuth)
	r.sources[EnvSMTPAddr] = addrSource
	r.sources[EnvSMTPFrom] = fromSource
	r.sources[EnvSMTPUsername] = usernameSource
	r.sources[EnvSMTPAuth] = passwordSource
	if addr == "" {
		for _, extra := range []struct {
			name  string
			value string
		}{{EnvSMTPFrom, from}, {EnvSMTPUsername, username}, {EnvSMTPAuth, password}} {
			if extra.value != "" {
				r.problems = append(r.problems, errors.New(extra.name+" is set but "+EnvSMTPAddr+" is not; set "+EnvSMTPAddr+" to enable email or remove the SMTP settings"))
			}
		}
		return
	}
	valid := true
	if !validHostPort(addr) {
		r.fail(EnvSMTPAddr, addrSource, errors.New(EnvSMTPAddr+" must be host:port with a port from 1 to 65535"))
		valid = false
	}
	if from == "" {
		r.problems = append(r.problems, errors.New(EnvSMTPFrom+" is required when "+EnvSMTPAddr+" is set"))
		valid = false
	} else if parsed, err := mail.ParseAddress(from); err != nil || parsed.Address == "" || strings.ContainsAny(from, "\r\n") {
		r.fail(EnvSMTPFrom, fromSource, errors.New(EnvSMTPFrom+" must be one valid email address"))
		valid = false
	}
	if (username == "") != (password == "") {
		r.problems = append(r.problems, errors.New(EnvSMTPUsername+" and "+EnvSMTPAuth+" must be set together"))
		valid = false
	}
	if strings.ContainsAny(username, "\r\n") {
		r.fail(EnvSMTPUsername, usernameSource, errors.New(EnvSMTPUsername+" must not contain line breaks"))
		valid = false
	}
	if strings.ContainsAny(password, "\r\n") {
		r.fail(EnvSMTPAuth, passwordSource, errors.New(EnvSMTPAuth+" must not contain line breaks"))
		valid = false
	}
	if valid {
		cfg.SMTP = SMTP{Addr: addr, Username: username, Password: password, From: from}
	}
}

func (r *resolver) loadMetrics(cfg *Config) {
	value, source := r.value(EnvMetrics)
	r.sources[EnvMetrics] = source
	if value == "" {
		return
	}
	switch strings.ToLower(value) {
	case "1", "true":
		cfg.Metrics = true
	case "0", "false":
		cfg.Metrics = false
	default:
		r.fail(EnvMetrics, source, errors.New(EnvMetrics+" must be true, false, 1 or 0"))
	}
}

func (r *resolver) loadBackupInterval(cfg *Config) {
	value, source := r.value(EnvBackupInterval)
	r.sources[EnvBackupInterval] = source
	if value == "" {
		cfg.BackupInterval = DefaultBackupInterval
		return
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		r.fail(EnvBackupInterval, source, errors.New(EnvBackupInterval+" must be a duration such as 24h or 90m, or 0 to turn scheduled backups off"))
		return
	}
	if duration != 0 && duration < MinimumBackupInterval {
		r.fail(EnvBackupInterval, source, errors.New(EnvBackupInterval+" must be 0 or at least 1m"))
		return
	}
	cfg.BackupInterval = duration
}

func validHostPort(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || host == "" || strings.ContainsAny(host, " \t\r\n/") {
		return false
	}
	number, err := strconv.Atoi(port)
	return err == nil && number >= 1 && number <= 65535 && port == strconv.Itoa(number)
}

func (r *resolver) loadUpdates(cfg *Config) {
	feed, source := r.value(EnvUpdateFeedURL)
	r.sources[EnvUpdateFeedURL] = source
	if feed == "" {
		feed = updates.DefaultFeedURL
	}
	if _, err := updates.ParseFeedURL(feed); err != nil {
		r.fail(EnvUpdateFeedURL, source, errors.New(EnvUpdateFeedURL+": "+err.Error()))
	} else {
		cfg.Updates.FeedURL = feed
	}

	keys, keySource := r.value(EnvUpdatePublicKeys)
	r.sources[EnvUpdatePublicKeys] = keySource
	if keys == "" {
		keys = buildinfo.UpdatePublicKeys
	}
	parsed, err := updates.ParseKeys(keys)
	if err != nil {
		if keySource == SourceDefault {
			r.problems = append(r.problems, errors.New("the update public keys compiled into this build are invalid: "+err.Error()))
		} else {
			r.fail(EnvUpdatePublicKeys, keySource, errors.New(EnvUpdatePublicKeys+": "+err.Error()))
		}
	} else {
		cfg.Updates.Keys = parsed
	}

	kind, kindSource := r.value(EnvInstallKind)
	r.sources[EnvInstallKind] = kindSource
	if kind != "" {
		if _, ok := updates.ParseInstallKind(kind); !ok {
			r.fail(EnvInstallKind, kindSource, errors.New(EnvInstallKind+" must be container or binary"))
		} else {
			cfg.Updates.InstallKind = strings.ToLower(kind)
		}
	}
}
