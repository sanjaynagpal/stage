// Package protocolhandler registers (and unregisters) the custom URI
// scheme a Stage-built app uses for its token/auth launch flow
// (docs/REQUIREMENTS.md §11), routing an incoming "acme-abc://..." URI to
// rigger.exe via the standard Windows protocol-handler registry
// convention.
package protocolhandler

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows/registry"

	"github.com/sanjaynagpal/stage/internal/layout"
)

func rootKey(scope layout.Scope) registry.Key {
	if scope == layout.ScopeAllUsers {
		return registry.LOCAL_MACHINE
	}
	return registry.CURRENT_USER
}

func schemeKeyPath(scheme string) string {
	return `Software\Classes\` + scheme
}

// Register writes the registry entries that make Windows route an
// "<scheme>://..." URI to riggerExePath "%1": the scheme's own key
// (marked with the conventional "URL Protocol" value so other shell
// components recognize it as a real URI-scheme handler, not just an
// arbitrary ShellExecute target) and its shell\open\command.
func Register(scope layout.Scope, scheme, appName, riggerExePath string) error {
	root := rootKey(scope)
	path := schemeKeyPath(scheme)

	k, _, err := registry.CreateKey(root, path, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		return fmt.Errorf("protocolhandler: create %s: %w", path, err)
	}
	defer k.Close()

	if err := k.SetStringValue("", fmt.Sprintf("URL:%s Protocol", appName)); err != nil {
		return fmt.Errorf("protocolhandler: set %s default value: %w", path, err)
	}
	if err := k.SetStringValue("URL Protocol", ""); err != nil {
		return fmt.Errorf("protocolhandler: set %s URL Protocol marker: %w", path, err)
	}

	cmdPath := path + `\shell\open\command`
	cmdKey, _, err := registry.CreateKey(root, cmdPath, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		return fmt.Errorf("protocolhandler: create %s: %w", cmdPath, err)
	}
	defer cmdKey.Close()

	command := fmt.Sprintf(`"%s" "%%1"`, riggerExePath)
	if err := cmdKey.SetStringValue("", command); err != nil {
		return fmt.Errorf("protocolhandler: set %s default value: %w", cmdPath, err)
	}
	return nil
}

// Unregister removes the scheme's registry entries, deleting leaf-first
// (registry.DeleteKey only deletes a key with no subkeys). Missing keys
// are not an error.
func Unregister(scope layout.Scope, scheme string) error {
	root := rootKey(scope)
	base := schemeKeyPath(scheme)

	for _, path := range []string{
		base + `\shell\open\command`,
		base + `\shell\open`,
		base + `\shell`,
		base,
	} {
		if err := registry.DeleteKey(root, path); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("protocolhandler: delete %s: %w", path, err)
		}
	}
	return nil
}
