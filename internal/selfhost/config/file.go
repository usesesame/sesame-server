package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

const maxConfigFileBytes = 1 << 20

type fileValues map[string]string

var fileKeys = map[string]string{
	EnvAddr:           "addr",
	EnvPublicURL:      "publicUrl",
	EnvTrustedProxies: "trustedProxies",
	EnvLogLevel:       "logLevel",
	EnvSMTPAddr:       "smtp.addr",
	EnvSMTPUsername:   "smtp.username",
	EnvSMTPAuth:       "smtp.password",
	EnvSMTPFrom:       "smtp.from",
	EnvMetrics:        "metrics",
	EnvBackupInterval: "backupInterval",
}

type fileSMTP struct {
	Addr     *string `json:"addr"`
	Username *string `json:"username"`
	Password *string `json:"password"`
	From     *string `json:"from"`
}

type fileDocument struct {
	Addr           *string   `json:"addr"`
	PublicURL      *string   `json:"publicUrl"`
	TrustedProxies *[]string `json:"trustedProxies"`
	LogLevel       *string   `json:"logLevel"`
	SMTP           *fileSMTP `json:"smtp"`
	Metrics        *bool     `json:"metrics"`
	BackupInterval *string   `json:"backupInterval"`
}

func readConfigFile(path string, required bool) (fileValues, bool, []string, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && !required {
			return fileValues{}, false, nil, nil
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil, fmt.Errorf("%s points at %s, which does not exist", EnvConfigFile, path)
		}
		return nil, false, nil, fmt.Errorf("config file %s cannot be read: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return nil, false, nil, fmt.Errorf("config file %s must be a regular file", path)
	}
	if info.Size() > maxConfigFileBytes {
		return nil, false, nil, fmt.Errorf("config file %s is larger than %d bytes", path, maxConfigFileBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, false, nil, fmt.Errorf("config file %s cannot be read: %w", path, err)
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxConfigFileBytes+1))
	if err != nil {
		return nil, false, nil, fmt.Errorf("config file %s cannot be read: %w", path, err)
	}
	if len(content) > maxConfigFileBytes {
		return nil, false, nil, fmt.Errorf("config file %s is larger than %d bytes", path, maxConfigFileBytes)
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var document fileDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, false, nil, fmt.Errorf("config file %s is invalid: %s", path, describeJSONError(err))
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, false, nil, fmt.Errorf("config file %s is invalid: it must hold one JSON object and nothing after it", path)
	}
	values := fileValues{}
	setString := func(name string, value *string) {
		if value != nil {
			values[name] = *value
		}
	}
	setString(EnvAddr, document.Addr)
	setString(EnvPublicURL, document.PublicURL)
	setString(EnvLogLevel, document.LogLevel)
	setString(EnvBackupInterval, document.BackupInterval)
	if document.TrustedProxies != nil {
		values[EnvTrustedProxies] = strings.Join(*document.TrustedProxies, ",")
	}
	if document.Metrics != nil {
		values[EnvMetrics] = strconv.FormatBool(*document.Metrics)
	}
	var warnings []string
	if document.SMTP != nil {
		setString(EnvSMTPAddr, document.SMTP.Addr)
		setString(EnvSMTPUsername, document.SMTP.Username)
		setString(EnvSMTPAuth, document.SMTP.Password)
		setString(EnvSMTPFrom, document.SMTP.From)
		if document.SMTP.Password != nil && *document.SMTP.Password != "" && info.Mode().Perm()&0o077 != 0 {
			warnings = append(warnings, fmt.Sprintf("config file %s holds an SMTP password but is readable by other users; run chmod 600 on it or set SESAME_SMTP_PASSWORD instead", path))
		}
	}
	return values, true, warnings, nil
}

func describeJSONError(err error) string {
	var syntax *json.SyntaxError
	var typeErr *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF):
		return "the file is empty; write {} for no settings"
	case errors.Is(err, io.ErrUnexpectedEOF):
		return "syntax error, the file ends early"
	case errors.As(err, &syntax):
		return fmt.Sprintf("syntax error at byte %d", syntax.Offset)
	case errors.As(err, &typeErr):
		return fmt.Sprintf("key %q must be a %s", typeErr.Field, typeErr.Type.String())
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		return "unknown key " + strings.TrimPrefix(err.Error(), "json: unknown field ")
	}
	return err.Error()
}
