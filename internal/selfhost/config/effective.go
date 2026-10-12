package config

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

const redacted = "[redacted]"

type Setting struct {
	Name   string
	Value  string
	Source Source
}

func (c Config) Effective() []Setting {
	proxies := make([]string, 0, len(c.TrustedProxies))
	for _, prefix := range c.TrustedProxies {
		proxies = append(proxies, prefix.String())
	}
	trusted := "none"
	if len(proxies) > 0 {
		trusted = strings.Join(proxies, ",")
	}
	configFile := "none"
	if c.ConfigFileLoaded {
		configFile = c.ConfigFile
	}
	backup := "disabled"
	if c.BackupInterval > 0 {
		backup = c.BackupInterval.Round(time.Second).String()
	}
	keyNames := make([]string, 0, len(c.Updates.Keys))
	for name := range c.Updates.Keys {
		keyNames = append(keyNames, name)
	}
	sort.Strings(keyNames)
	updateKeys := "none"
	if len(keyNames) > 0 {
		updateKeys = strings.Join(keyNames, ",")
	}
	installKind := "detected"
	if c.Updates.InstallKind != "" {
		installKind = c.Updates.InstallKind
	}
	settings := []Setting{
		{EnvDataDir, c.DataDir, c.SourceOf(EnvDataDir)},
		{EnvConfigFile, configFile, c.SourceOf(EnvConfigFile)},
		{EnvAddr, c.Addr, c.SourceOf(EnvAddr)},
		{EnvPublicURL, c.PublicURL.Origin, c.SourceOf(EnvPublicURL)},
		{EnvTrustedProxies, trusted, c.SourceOf(EnvTrustedProxies)},
		{EnvLogLevel, c.LogLevel, c.SourceOf(EnvLogLevel)},
		{EnvMetrics, fmt.Sprint(c.Metrics), c.SourceOf(EnvMetrics)},
		{EnvBackupInterval, backup, c.SourceOf(EnvBackupInterval)},
		{EnvUpdateFeedURL, c.Updates.FeedURL, c.SourceOf(EnvUpdateFeedURL)},
		{EnvUpdatePublicKeys, updateKeys, c.SourceOf(EnvUpdatePublicKeys)},
		{EnvInstallKind, installKind, c.SourceOf(EnvInstallKind)},
	}
	if !c.SMTP.Enabled() {
		return append(settings, Setting{EnvSMTPAddr, "disabled", c.SourceOf(EnvSMTPAddr)})
	}
	return append(settings,
		Setting{EnvSMTPAddr, c.SMTP.Addr, c.SourceOf(EnvSMTPAddr)},
		Setting{EnvSMTPFrom, c.SMTP.From, c.SourceOf(EnvSMTPFrom)},
		Setting{EnvSMTPUsername, redactIfSet(c.SMTP.Username), c.SourceOf(EnvSMTPUsername)},
		Setting{EnvSMTPAuth, redactIfSet(c.SMTP.Password), c.SourceOf(EnvSMTPAuth)},
	)
}

func redactIfSet(value string) string {
	if value == "" {
		return "unset"
	}
	return redacted
}

func (c Config) WriteEffective(w io.Writer) error {
	for _, setting := range c.Effective() {
		if _, err := fmt.Fprintf(w, "%s=%s (%s)\n", setting.Name, setting.Value, setting.Source); err != nil {
			return err
		}
	}
	return nil
}
