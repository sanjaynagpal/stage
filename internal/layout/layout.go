// Package layout centralizes every filesystem path convention Stage uses, so
// no other package re-derives or hardcodes them (docs/REQUIREMENTS.md §3-4).
package layout

import (
	"os"
	"path/filepath"
)

// Scope identifies whether an install is machine-wide or per-user.
type Scope string

const (
	ScopeAllUsers Scope = "AllUsers"
	ScopePerUser  Scope = "PerUser"
)

// RootDir returns the root application folder for the given scope and app
// id: %ProgramFiles%\<AppID> for all-users, %LocalAppData%\<AppID> for
// per-user (docs/REQUIREMENTS.md §2).
func RootDir(scope Scope, appID string) string {
	if scope == ScopeAllUsers {
		return filepath.Join(os.Getenv("ProgramFiles"), appID)
	}
	return filepath.Join(os.Getenv("LocalAppData"), appID)
}

// DataDir returns the per-app, user-writable data directory
// (%AppData%\<AppID>), distinct from the install root and always
// user-writable even for an all-users install (docs/REQUIREMENTS.md §9b).
func DataDir(appID string) string {
	return filepath.Join(os.Getenv("AppData"), appID)
}

// AppIDFromExePath derives an app id from an installed component's own
// location: the root app folder is always named after the app, so the
// directory containing the exe IS the app id (docs/REQUIREMENTS.md §3-4).
// This lets rigger.exe/unins.exe be generic, prebuilt-once binaries with
// zero per-app compiled state.
func AppIDFromExePath(exePath string) string {
	return filepath.Base(filepath.Dir(exePath))
}

// RootDirFromExePath returns the root app folder containing exePath.
func RootDirFromExePath(exePath string) string {
	return filepath.Dir(exePath)
}

// JREDir returns the jre/ directory under the given root.
func JREDir(root string) string {
	return filepath.Join(root, "jre")
}

// JREVersionDir returns the jre/<version> directory for a specific runtime.
func JREVersionDir(root, version string) string {
	return filepath.Join(root, "jre", version)
}

// VersionDir returns the <version> directory holding an app version's jars.
func VersionDir(root, version string) string {
	return filepath.Join(root, version)
}

// reservedRootEntries lists every non-version-directory item Stage places
// directly under the install root, so eviction logic (internal/jarprovision)
// can tell a stale app-version folder apart from Rigger's own fixed files.
var reservedRootEntries = map[string]bool{
	"jre":                 true,
	"rigger.exe":          true,
	"unins.exe":           true,
	"manifest.json":       true,
	"app.ico":             true,
	"install-record.json": true,
}

// IsVersionDir reports whether name (a direct child of the install root) is
// an app-version directory rather than one of Stage's own fixed files or
// folders — i.e. whether it's safe for jar eviction to remove wholesale.
func IsVersionDir(name string) bool {
	return !reservedRootEntries[name]
}

// ManifestPath returns the local cached manifest.json path under root.
func ManifestPath(root string) string {
	return filepath.Join(root, "manifest.json")
}

// InstallRecordPath returns the path to the installer's receipt of exactly
// what it registered (protocol scheme, file associations, shortcuts) — read
// by the uninstaller so teardown doesn't have to re-derive or guess it.
func InstallRecordPath(root string) string {
	return filepath.Join(root, "install-record.json")
}

// RiggerLogPath returns rigger.exe's own log file path, under the per-app
// DataDir (never the install root — an all-users root under
// %ProgramFiles% isn't writable by an ordinary later launch, the same
// reason DataDir exists at all, docs/REQUIREMENTS.md §9b).
func RiggerLogPath(dataDir string) string {
	return filepath.Join(dataDir, "rigger.log")
}

// RiggerExePath returns rigger.exe's path under root.
func RiggerExePath(root string) string {
	return filepath.Join(root, "rigger.exe")
}

// UninstallerExePath returns the uninstaller's path under root.
func UninstallerExePath(root string) string {
	return filepath.Join(root, "unins.exe")
}

// IconPath returns the branding icon dropped in the install root, used by
// shortcuts (SetIconLocation) and by Rigger's -Dstage.appIcon convention.
func IconPath(root string) string {
	return filepath.Join(root, "app.ico")
}

// StartMenuDir returns the Start Menu Programs folder for appName, matching
// scope: all-users under %ProgramData%, per-user under %AppData%
// (docs/REQUIREMENTS.md §16).
func StartMenuDir(scope Scope, appName string) string {
	if scope == ScopeAllUsers {
		return filepath.Join(os.Getenv("ProgramData"), "Microsoft", "Windows", "Start Menu", "Programs", appName)
	}
	return filepath.Join(os.Getenv("AppData"), "Microsoft", "Windows", "Start Menu", "Programs", appName)
}

// DesktopDir returns the Desktop folder to place a shortcut in, matching
// scope: all-users under %Public%\Desktop, per-user under the current
// user's own Desktop (docs/REQUIREMENTS.md §16).
func DesktopDir(scope Scope) string {
	if scope == ScopeAllUsers {
		return filepath.Join(os.Getenv("Public"), "Desktop")
	}
	return filepath.Join(os.Getenv("UserProfile"), "Desktop")
}
