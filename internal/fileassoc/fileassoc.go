// Package fileassoc registers (and unregisters) optional file-type
// associations declared in an app's build config (docs/REQUIREMENTS.md §16
// step 3) — an empty-by-default list; this package is only ever invoked
// when the app declares at least one extension.
//
// Registering an association here is mechanically correct, but rigger.exe
// has no concept yet of "launched to open a file": internal/uriparse's
// LooksLikeInvocation only recognizes the app's protocol scheme, so a file
// path passed as argv[1] on double-click falls through to the plain
// shortcut-launch branch today and is silently dropped, never reaching the
// JVM. Making a double-click on an associated file actually do something
// needs a corresponding ${openedFile}-style placeholder and argv branch in
// cmd/rigger — out of scope here, same as the TODO(Phase 8) JRE-provisioning
// gap already marked in cmd/rigger/main.go.
package fileassoc

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/windows/registry"

	"github.com/sanjaynagpal/stage/internal/layout"
)

// Spec describes one file-type association to register.
type Spec struct {
	Extension   string // e.g. ".abc"
	Description string
	IconPath    string // optional
}

func rootKey(scope layout.Scope) registry.Key {
	if scope == layout.ScopeAllUsers {
		return registry.LOCAL_MACHINE
	}
	return registry.CURRENT_USER
}

// progID derives the ProgID Register uses for an app+extension pair, e.g.
// appID "ABC" and extension ".abc" -> "ABC.abc".
func progID(appID, extension string) string {
	return appID + "." + strings.TrimPrefix(extension, ".")
}

// Register associates spec.Extension with a new ProgID
// ("<appID>.<ext-without-dot>") that opens via riggerExePath "%1", and
// returns the ProgID actually registered (recorded by the installer so the
// uninstaller can remove exactly this association later).
func Register(scope layout.Scope, appID string, spec Spec, riggerExePath string) (string, error) {
	root := rootKey(scope)
	id := progID(appID, spec.Extension)

	extKey, _, err := registry.CreateKey(root, `Software\Classes\`+spec.Extension, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		return "", fmt.Errorf("fileassoc: create Software\\Classes\\%s: %w", spec.Extension, err)
	}
	defer extKey.Close()
	if err := extKey.SetStringValue("", id); err != nil {
		return "", fmt.Errorf("fileassoc: set %s default value: %w", spec.Extension, err)
	}

	progKey, _, err := registry.CreateKey(root, `Software\Classes\`+id, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		return "", fmt.Errorf("fileassoc: create Software\\Classes\\%s: %w", id, err)
	}
	defer progKey.Close()
	if err := progKey.SetStringValue("", spec.Description); err != nil {
		return "", fmt.Errorf("fileassoc: set %s default value: %w", id, err)
	}

	if spec.IconPath != "" {
		iconKey, _, err := registry.CreateKey(root, `Software\Classes\`+id+`\DefaultIcon`, registry.ALL_ACCESS|registry.WOW64_64KEY)
		if err != nil {
			return "", fmt.Errorf("fileassoc: create %s\\DefaultIcon: %w", id, err)
		}
		defer iconKey.Close()
		if err := iconKey.SetStringValue("", spec.IconPath); err != nil {
			return "", fmt.Errorf("fileassoc: set %s\\DefaultIcon: %w", id, err)
		}
	}

	cmdKey, _, err := registry.CreateKey(root, `Software\Classes\`+id+`\shell\open\command`, registry.ALL_ACCESS|registry.WOW64_64KEY)
	if err != nil {
		return "", fmt.Errorf("fileassoc: create %s\\shell\\open\\command: %w", id, err)
	}
	defer cmdKey.Close()
	command := fmt.Sprintf(`"%s" "%%1"`, riggerExePath)
	if err := cmdKey.SetStringValue("", command); err != nil {
		return "", fmt.Errorf("fileassoc: set %s\\shell\\open\\command: %w", id, err)
	}

	return id, nil
}

// Unregister removes the extension's association and the ProgID's own
// keys, deleting leaf-first. Missing keys are not an error.
func Unregister(scope layout.Scope, extension, progID string) error {
	root := rootKey(scope)

	for _, path := range []string{
		`Software\Classes\` + progID + `\shell\open\command`,
		`Software\Classes\` + progID + `\shell\open`,
		`Software\Classes\` + progID + `\shell`,
		`Software\Classes\` + progID + `\DefaultIcon`,
		`Software\Classes\` + progID,
		`Software\Classes\` + extension,
	} {
		if err := registry.DeleteKey(root, path); err != nil && !errors.Is(err, registry.ErrNotExist) {
			return fmt.Errorf("fileassoc: delete %s: %w", path, err)
		}
	}
	return nil
}
