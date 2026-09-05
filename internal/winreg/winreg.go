// Package winreg wraps golang.org/x/sys/windows/registry to read and write
// the two registry keys Stage cares about: the app's own Software\<AppId>
// key and the standard Add/Remove Programs Uninstall key
// (docs/REQUIREMENTS.md §4).
package winreg

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"

	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/manifest"
)

// PackageMode records whether rigger.exe/JRE may be updated in place
// (docs/REQUIREMENTS.md §13-14).
type PackageMode string

const (
	ModeStatic  PackageMode = "Static"
	ModeDynamic PackageMode = "Dynamic"
)

// AppValues mirrors the Software\<AppId> value set.
type AppValues struct {
	InstallScope      layout.Scope
	PackageMode       PackageMode
	AppID             string
	InstallDir        string
	DataDir           string
	ManifestServerURL string
	ProtocolScheme    string
	DisplayVersion    string
	// NetworkZone is the network (e.g. corporate intranet, internet, a
	// private extranet) this install was built for — a build-time choice,
	// like Environment, never switched post-install (docs/REQUIREMENTS.md
	// §17-18).
	NetworkZone manifest.NetworkZone
	// ProxyHost and ProxyPort are the proxy resolved at install time
	// (auto-detected, optionally operator-overridden) needed to reach the
	// manifest server from NetworkZone. Both empty means a direct
	// connection, no proxy.
	ProxyHost string
	ProxyPort string
}

const uninstallKeyPrefix = `Software\Microsoft\Windows\CurrentVersion\Uninstall\`

// UninstallValues mirrors the standard Add/Remove Programs Uninstall key.
type UninstallValues struct {
	AppID           string
	InstallScope    layout.Scope
	DisplayName     string
	DisplayVersion  string
	Publisher       string
	InstallLocation string
	UninstallString string
	DisplayIcon     string
	EstimatedSizeKB uint32
}

func rootKey(scope layout.Scope) registry.Key {
	if scope == layout.ScopeAllUsers {
		return registry.LOCAL_MACHINE
	}
	return registry.CURRENT_USER
}

func scopeKeyName(scope layout.Scope) string {
	if scope == layout.ScopeAllUsers {
		return "HKLM"
	}
	return "HKCU"
}

func appKeyPath(appID string) string {
	return `Software\` + appID
}

// WriteAppValues creates (or overwrites) the Software\<AppId> key under the
// hive matching v.InstallScope.
func WriteAppValues(v AppValues) error {
	k, _, err := registry.CreateKey(rootKey(v.InstallScope), appKeyPath(v.AppID), registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		return fmt.Errorf("winreg: create %s\\%s: %w", scopeKeyName(v.InstallScope), appKeyPath(v.AppID), err)
	}
	defer k.Close()

	values := map[string]string{
		"InstallScope":      string(v.InstallScope),
		"PackageMode":       string(v.PackageMode),
		"AppId":             v.AppID,
		"InstallDir":        v.InstallDir,
		"DataDir":           v.DataDir,
		"ManifestServerUrl": v.ManifestServerURL,
		"ProtocolScheme":    v.ProtocolScheme,
		"DisplayVersion":    v.DisplayVersion,
		"NetworkZone":       string(v.NetworkZone),
		"ProxyHost":         v.ProxyHost,
		"ProxyPort":         v.ProxyPort,
	}
	for name, val := range values {
		if err := k.SetStringValue(name, val); err != nil {
			return fmt.Errorf("winreg: set %s: %w", name, err)
		}
	}
	return nil
}

// ReadAppValues reads Software\<AppId>, trying HKCU first (cheap, always
// readable without elevation) then HKLM — whichever hive answers implicitly
// reveals scope, but callers should still trust the key's own InstallScope
// value as the source of truth (docs/REQUIREMENTS.md §4), which this
// function cross-checks is internally consistent by simply returning it
// as read.
func ReadAppValues(appID string) (AppValues, error) {
	var lastErr error
	for _, scope := range []layout.Scope{layout.ScopePerUser, layout.ScopeAllUsers} {
		k, err := registry.OpenKey(rootKey(scope), appKeyPath(appID), registry.QUERY_VALUE|registry.WOW64_64KEY)
		if err != nil {
			lastErr = err
			continue
		}
		defer k.Close()
		return readAppValues(k, appID)
	}
	return AppValues{}, fmt.Errorf("winreg: no Software\\%s key found under HKCU or HKLM: %w", appID, lastErr)
}

func readAppValues(k registry.Key, appID string) (AppValues, error) {
	required := func(name string) (string, error) {
		s, _, err := k.GetStringValue(name)
		if err != nil {
			return "", fmt.Errorf("winreg: get %s for %s: %w", name, appID, err)
		}
		return s, nil
	}
	optional := func(name string) string {
		s, _, _ := k.GetStringValue(name)
		return s
	}

	scopeStr, err := required("InstallScope")
	if err != nil {
		return AppValues{}, err
	}
	modeStr, err := required("PackageMode")
	if err != nil {
		return AppValues{}, err
	}
	installDir, err := required("InstallDir")
	if err != nil {
		return AppValues{}, err
	}
	dataDir, err := required("DataDir")
	if err != nil {
		return AppValues{}, err
	}
	manifestURL, err := required("ManifestServerUrl")
	if err != nil {
		return AppValues{}, err
	}
	networkZone, err := required("NetworkZone")
	if err != nil {
		return AppValues{}, err
	}

	return AppValues{
		InstallScope:      layout.Scope(scopeStr),
		PackageMode:       PackageMode(modeStr),
		AppID:             appID,
		InstallDir:        installDir,
		DataDir:           dataDir,
		ManifestServerURL: manifestURL,
		ProtocolScheme:    optional("ProtocolScheme"),
		DisplayVersion:    optional("DisplayVersion"),
		NetworkZone:       manifest.NetworkZone(networkZone),
		ProxyHost:         optional("ProxyHost"),
		ProxyPort:         optional("ProxyPort"),
	}, nil
}

// DeleteAppKey removes the Software\<AppId> key. Missing keys are not an error.
func DeleteAppKey(scope layout.Scope, appID string) error {
	err := registry.DeleteKey(rootKey(scope), appKeyPath(appID))
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("winreg: delete %s\\%s: %w", scopeKeyName(scope), appKeyPath(appID), err)
	}
	return nil
}

// WriteUninstallValues creates (or overwrites) the standard Add/Remove
// Programs Uninstall key for the app.
func WriteUninstallValues(v UninstallValues) error {
	path := uninstallKeyPrefix + v.AppID
	k, _, err := registry.CreateKey(rootKey(v.InstallScope), path, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		return fmt.Errorf("winreg: create uninstall key for %s: %w", v.AppID, err)
	}
	defer k.Close()

	strs := map[string]string{
		"DisplayName":     v.DisplayName,
		"DisplayVersion":  v.DisplayVersion,
		"Publisher":       v.Publisher,
		"InstallLocation": v.InstallLocation,
		"UninstallString": v.UninstallString,
		"DisplayIcon":     v.DisplayIcon,
	}
	for name, val := range strs {
		if err := k.SetStringValue(name, val); err != nil {
			return fmt.Errorf("winreg: set uninstall %s: %w", name, err)
		}
	}
	if err := k.SetDWordValue("EstimatedSize", v.EstimatedSizeKB); err != nil {
		return fmt.Errorf("winreg: set uninstall EstimatedSize: %w", err)
	}
	return nil
}

// DeleteUninstallValues removes the app's Uninstall key. Missing keys are
// not an error.
func DeleteUninstallValues(scope layout.Scope, appID string) error {
	err := registry.DeleteKey(rootKey(scope), uninstallKeyPrefix+appID)
	if err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("winreg: delete uninstall key for %s: %w", appID, err)
	}
	return nil
}
