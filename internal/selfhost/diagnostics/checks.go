package diagnostics

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"path/filepath"
	"runtime"

	"usesesame.app/backend/internal/selfhost/secrets"
)

var instanceKeyFiles = [...]string{
	secrets.SigningKeyFile,
	secrets.AdminKeyFile,
	secrets.IPPepperFile,
}

func (s *scanner) configurationCheck() Check {
	return configurationValidCheck()
}

func (s *scanner) originCheck() Check {
	if s.cfg.PublicURL.Secure {
		return originHTTPSCheck()
	}
	return originLocalCheck()
}

func (s *scanner) listenCheck() Check {
	switch s.summary.ListenScope {
	case "loopback":
		return listenLoopbackCheck()
	case "all":
		return listenAllInterfacesCheck()
	default:
		return listenNonLoopbackCheck()
	}
}

func (s *scanner) proxyCheck() Check {
	if len(s.cfg.TrustedProxies) == 0 {
		if s.cfg.PublicURL.Secure {
			return proxyTrustEmptyHTTPSCheck()
		}
		return proxyTrustEmptyLocalCheck()
	}
	for _, prefix := range s.cfg.TrustedProxies {
		if wideProxy(prefix) {
			return proxyTrustWideCheck()
		}
	}
	return proxyTrustConfiguredCheck()
}

func wideProxy(prefix netip.Prefix) bool {
	if prefix.Addr().IsLoopback() {
		return false
	}
	if prefix.Addr().Is4() {
		return prefix.Bits() < 24
	}
	return prefix.Bits() < 64
}

func (s *scanner) configFileCheck() Check {
	if !s.cfg.ConfigFileLoaded {
		_, err := s.files.lstat(s.cfg.ConfigFile)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return configFileAbsentCheck()
		case err != nil:
			return configFileMetadataUnavailableCheck()
		default:
			return configFilePathChangedCheck()
		}
	}
	info, err := stableLstat(s.files, s.cfg.ConfigFile)
	switch {
	case errors.Is(err, fs.ErrNotExist), errors.Is(err, errPathChanged):
		return configFilePathChangedCheck()
	case err != nil:
		return configFileMetadataUnavailableCheck()
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return configFileSymlinkCheck()
	}
	if !info.Mode().IsRegular() {
		return configFilePathChangedCheck()
	}
	if runtime.GOOS != "linux" {
		return configFileACLUnknownCheck()
	}
	if info.Mode().Perm()&0o077 != 0 {
		return configFilePermissionsCheck()
	}
	return configFilePrivateCheck()
}

func (s *scanner) dataDirectoryCheck() Check {
	return s.data.check
}

func (s *scanner) databaseCheck() Check {
	return s.database.check
}

func (s *scanner) secretsCheck() Check {
	if s.data.missing {
		return s.missingSecretsCheck()
	}
	if !s.data.realDir {
		return secretsNotInspectedCheck()
	}
	dir := filepath.Join(s.cfg.DataDir, secrets.DirName)
	dirInfo, err := stableLstat(s.files, dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return s.missingSecretsCheck()
	case errors.Is(err, errPathChanged):
		return secretsPathChangedCheck()
	case err != nil:
		return secretsMetadataUnavailableCheck()
	}
	if dirInfo.Mode()&fs.ModeSymlink != 0 || !dirInfo.IsDir() {
		return secretsDirectoryUnsafeCheck()
	}
	found := &issues{}
	if runtime.GOOS == "linux" && dirInfo.Mode().Perm()&0o077 != 0 {
		found.addAction(70, secretsDirectoryPermissionsCheck())
	}
	for _, name := range instanceKeyFiles {
		path := filepath.Join(dir, name)
		info, err := stableLstat(s.files, path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			switch {
			case s.database.known && s.database.present:
				found.addAction(20, secretsMissingCheck())
			case s.database.known:
				found.addUnknown(90, secretsUninitializedCheck())
			default:
				found.addUnknown(85, secretsDatabaseUnknownCheck())
			}
		case errors.Is(err, errPathChanged):
			found.addUnknown(80, secretsPathChangedCheck())
		case err != nil:
			found.addUnknown(85, secretsMetadataUnavailableCheck())
		case info.Mode()&fs.ModeSymlink != 0:
			found.addAction(30, secretsSymlinkCheck())
		case !info.Mode().IsRegular():
			found.addAction(40, secretsWrongTypeCheck())
		case info.Size() != int64(secrets.KeySize):
			found.addAction(50, secretsSizeCheck())
		case runtime.GOOS == "linux" && info.Mode().Perm()&0o077 != 0:
			found.addAction(60, secretsPermissionsCheck())
		}
	}
	if runtime.GOOS != "linux" {
		found.addUnknown(100, secretsACLUnknownCheck())
	}
	return found.result(secretsMetadataOKCheck())
}

func (s *scanner) missingSecretsCheck() Check {
	switch {
	case s.database.known && s.database.present:
		return secretsMissingCheck()
	case s.database.known:
		return secretsUninitializedCheck()
	default:
		return secretsDatabaseUnknownCheck()
	}
}

func fixedCheck(id, state, code, summary, next string) Check {
	check := Check{ID: id, State: state, Code: code, Summary: summary}
	if next != "" {
		check.Next = text(next)
	}
	return check
}

func configurationValidCheck() Check {
	return fixedCheck("configuration", StateOK, "configuration_valid",
		"The service configuration loaded from the process environment and the optional configuration file.", "")
}

func originHTTPSCheck() Check {
	return fixedCheck("origin", StateOK, "origin_https",
		"The public address is an HTTPS origin with no credentials, path, query or fragment.", "")
}

func originLocalCheck() Check {
	return fixedCheck("origin", StateOK, "origin_local",
		"The public address is a loopback HTTP origin, so links work only on the same computer.", "")
}

func listenLoopbackCheck() Check {
	return fixedCheck("listen", StateOK, "listen_loopback",
		"The server is configured to listen on a loopback address.", "")
}

func listenAllInterfacesCheck() Check {
	return fixedCheck("listen", StateUnknown, "listen_all_interfaces",
		"The server is configured to listen on all interfaces, and this report cannot see container publication or firewall rules.",
		"Confirm that only the intended network can reach the service.")
}

func listenNonLoopbackCheck() Check {
	return fixedCheck("listen", StateUnknown, "listen_non_loopback",
		"The server is configured to listen on a non-loopback address, and this report cannot see firewall or network exposure.",
		"Confirm that only the intended network can reach the service.")
}

func proxyTrustEmptyHTTPSCheck() Check {
	return fixedCheck("proxy_trust", StateUnknown, "proxy_trust_empty",
		"No trusted proxy ranges are configured while the public address uses HTTPS.",
		"Configure proxy ranges only when a proxy terminates TLS in front of the server.")
}

func proxyTrustEmptyLocalCheck() Check {
	return fixedCheck("proxy_trust", StateOK, "proxy_trust_empty",
		"No trusted proxy ranges are configured, and the public address uses local HTTP.", "")
}

func proxyTrustConfiguredCheck() Check {
	return fixedCheck("proxy_trust", StateOK, "proxy_trust_configured",
		"Trusted proxy ranges are configured and all stay inside loopback, private or link-local space.", "")
}

func proxyTrustWideCheck() Check {
	return fixedCheck("proxy_trust", StateAction, "proxy_trust_wide",
		"A trusted proxy range is wider than /24 for IPv4 or /64 for IPv6.",
		"Narrow the trusted proxy ranges to the addresses the proxy uses.")
}

func configFileAbsentCheck() Check {
	return fixedCheck("config_file", StateOK, "config_file_absent",
		"No configuration file is present at the configured location.", "")
}

func configFilePrivateCheck() Check {
	return fixedCheck("config_file", StateOK, "config_file_private",
		"The configuration file is a regular file with no group or other permission bits on this platform.", "")
}

func configFilePermissionsCheck() Check {
	return fixedCheck("config_file", StateAction, "config_file_permissions",
		"The configuration file has group or other permission bits, which can expose a stored mail password.",
		"Remove group and other permission bits from the configuration file.")
}

func configFileSymlinkCheck() Check {
	return fixedCheck("config_file", StateUnknown, "config_file_symlink",
		"The configuration file path is a symbolic link. Its target's permissions were not inspected.",
		"Use a regular configuration file at the configured location.")
}

func configFilePathChangedCheck() Check {
	return fixedCheck("config_file", StateUnknown, "path_changed",
		"The configuration file appeared, changed or disappeared after the configuration was read.",
		"Run the doctor again when the configuration file is stable.")
}

func configFileMetadataUnavailableCheck() Check {
	return fixedCheck("config_file", StateUnknown, "metadata_unavailable",
		"The configuration file metadata could not be read.",
		"Check that the process user can inspect the configuration file metadata.")
}

func configFileACLUnknownCheck() Check {
	return fixedCheck("config_file", StateUnknown, "acl_unverified",
		"The configuration file is a regular file, but this platform's access controls were not verified.",
		"Check the configuration file access controls with the platform tools.")
}

func dataDirectoryPrivateCheck() Check {
	return fixedCheck("data_directory", StateOK, "data_directory_private",
		"The data directory is a real directory with no group or other permission bits on this platform.", "")
}

func dataDirectoryMissingCheck() Check {
	return fixedCheck("data_directory", StateUnknown, "data_directory_missing",
		"The data directory is missing, and a first start may create it.",
		"Start the service once if this is a new installation.")
}

func dataDirectoryWrongTypeCheck() Check {
	return fixedCheck("data_directory", StateAction, "data_directory_wrong_type",
		"The data directory path is not a directory.",
		"Move the path aside and create a data directory for the service user.")
}

func dataDirectorySymlinkCheck() Check {
	return fixedCheck("data_directory", StateUnknown, "data_directory_symlink",
		"The data directory path is a symbolic link. Database and key metadata were not inspected through it.",
		"Use a real directory at the configured location.")
}

func dataDirectoryPermissionsCheck() Check {
	return fixedCheck("data_directory", StateAction, "data_directory_permissions",
		"The data directory has group or other permission bits.",
		"Remove group and other permission bits from the data directory.")
}

func dataDirectoryPathChangedCheck() Check {
	return fixedCheck("data_directory", StateUnknown, "path_changed",
		"The data directory changed while it was being inspected.",
		"Run the doctor again when the data directory is stable.")
}

func dataDirectoryMetadataUnavailableCheck() Check {
	return fixedCheck("data_directory", StateUnknown, "metadata_unavailable",
		"The data directory metadata could not be read.",
		"Check that the process user can inspect the data directory metadata.")
}

func dataDirectoryACLUnknownCheck() Check {
	return fixedCheck("data_directory", StateUnknown, "acl_unverified",
		"The data directory is a real directory, but this platform's access controls were not verified.",
		"Check the data directory access controls with the platform tools.")
}

func secretsMetadataOKCheck() Check {
	return fixedCheck("secrets", StateOK, "secrets_metadata_ok",
		"The secrets directory and key files are real paths with no group or other permission bits and the expected sizes.", "")
}

func secretsMissingCheck() Check {
	return fixedCheck("secrets", StateAction, "secrets_missing",
		"Instance key material is missing while a database exists.",
		"Restore the secrets directory from a backup instead of generating replacement keys.")
}

func secretsUninitializedCheck() Check {
	return fixedCheck("secrets", StateUnknown, "secrets_uninitialized",
		"The secrets directory or key files are missing and no database is present, so this instance has not been initialized.",
		"Start the service once if this is a new installation.")
}

func secretsNotInspectedCheck() Check {
	return fixedCheck("secrets", StateUnknown, "metadata_unavailable",
		"The secrets directory was not inspected because the data directory is unavailable or symbolic.",
		"Inspect the data directory first.")
}

func secretsDatabaseUnknownCheck() Check {
	return fixedCheck("secrets", StateUnknown, "metadata_unavailable",
		"Instance key material is missing and the database presence could not be determined.",
		"Inspect the database metadata first.")
}

func secretsDirectoryUnsafeCheck() Check {
	return fixedCheck("secrets", StateAction, "secrets_directory_unsafe",
		"The secrets directory is not a real directory.",
		"Restore the secrets directory from a backup as a real private directory.")
}

func secretsDirectoryPermissionsCheck() Check {
	return fixedCheck("secrets", StateAction, "secrets_directory_permissions",
		"The secrets directory has group or other permission bits.",
		"Remove group and other permission bits from the secrets directory.")
}

func secretsSymlinkCheck() Check {
	return fixedCheck("secrets", StateAction, "secrets_symlink",
		"An instance key file is a symbolic link, but the server requires a regular file.",
		"Restore the key file from a backup as a regular private file.")
}

func secretsWrongTypeCheck() Check {
	return fixedCheck("secrets", StateAction, "secrets_wrong_type",
		"An instance key file is not a regular file.",
		"Restore the key file from a backup as a regular private file.")
}

func secretsSizeCheck() Check {
	return fixedCheck("secrets", StateAction, "secrets_size",
		fmt.Sprintf("An instance key file does not hold exactly %d bytes.", secrets.KeySize),
		"Restore the key file from a backup instead of generating a replacement.")
}

func secretsPermissionsCheck() Check {
	return fixedCheck("secrets", StateAction, "secrets_permissions",
		"An instance key file has group or other permission bits.",
		"Remove group and other permission bits from the key files.")
}

func secretsPathChangedCheck() Check {
	return fixedCheck("secrets", StateUnknown, "path_changed",
		"The secrets metadata changed while it was being inspected.",
		"Run the doctor again when the secrets directory is stable.")
}

func secretsMetadataUnavailableCheck() Check {
	return fixedCheck("secrets", StateUnknown, "metadata_unavailable",
		"The secrets metadata could not be read.",
		"Check that the process user can inspect the secrets directory metadata.")
}

func secretsACLUnknownCheck() Check {
	return fixedCheck("secrets", StateUnknown, "acl_unverified",
		"The secrets metadata was read, but this platform's access controls were not verified.",
		"Check the secrets access controls with the platform tools.")
}

func databaseMissingCheck() Check {
	return fixedCheck("database", StateUnknown, "database_missing",
		"The database file is missing, and this report did not create it.",
		"Start the service once if this is a new installation.")
}

func databaseNotOpenedCheck() Check {
	return fixedCheck("database", StateUnknown, "database_not_opened",
		"The database file is present and non-empty, but this report did not open it, so schema, integrity, audit and recovery state are not inspected.",
		"Use the server's own check command when you need database integrity proof.")
}

func databaseWrongTypeCheck() Check {
	return fixedCheck("database", StateAction, "database_wrong_type",
		"The database path is not a regular file.",
		"Restore the database from a backup or move the path aside.")
}

func databaseSymlinkCheck() Check {
	return fixedCheck("database", StateAction, "database_symlink",
		"The database path is a symbolic link, so this report did not inspect its target.",
		"Use a regular database file at the configured location.")
}

func databaseEmptyCheck() Check {
	return fixedCheck("database", StateAction, "database_empty",
		"The database file is empty.",
		"Restore the database from a backup instead of starting on an empty file.")
}

func databasePermissionsCheck() Check {
	return fixedCheck("database", StateAction, "database_permissions",
		"The database file has group or other permission bits.",
		"Remove group and other permission bits from the database file.")
}

func databasePathChangedCheck() Check {
	return fixedCheck("database", StateUnknown, "path_changed",
		"The database file changed while it was being inspected.",
		"Run the doctor again when the database is stable.")
}

func databaseMetadataUnavailableCheck() Check {
	return fixedCheck("database", StateUnknown, "metadata_unavailable",
		"The database metadata could not be read.",
		"Check that the process user can inspect the data directory metadata.")
}

func databaseNotInspectedCheck() Check {
	return fixedCheck("database", StateUnknown, "metadata_unavailable",
		"The database was not inspected because the data directory is unavailable or symbolic.",
		"Inspect the data directory first.")
}
