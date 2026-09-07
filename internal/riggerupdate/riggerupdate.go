// Package riggerupdate implements rigger.exe's own binary self-update
// (docs/REQUIREMENTS.md §13/§14/§22, §25): comparing the manifest's
// optional declared version against this build's own compiled-in Version,
// and — for Dynamic-mode installs only — replacing the installed
// rigger.exe via the same self-copy-to-%TEMP%-and-relaunch pattern
// cmd/uninstaller already uses on itself (docs/DESIGN.md §2.9c's
// self-copy-relaunch note), since a running process can't overwrite its
// own executable file.
package riggerupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sanjaynagpal/stage/internal/jreprovision"
	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/manifest"
	"github.com/sanjaynagpal/stage/internal/proxydetect"
	"github.com/sanjaynagpal/stage/internal/winreg"
)

// Version is this rigger.exe build's own version. Bumped manually per
// Stage release — this repo doesn't use git tags/ldflags versioning today,
// and introducing that infra wasn't needed just for this.
const Version = "1.0.0"

// NeedsUpdate reports whether manifestVersion (Manifest.Rigger.Version)
// names a build other than this one. An empty manifestVersion means the
// manifest declares no update.
func NeedsUpdate(manifestVersion string) bool {
	return manifestVersion != "" && manifestVersion != Version
}

// BeginSelfUpdate is phase one, run by the currently-installed rigger.exe:
// copies itself to %TEMP% and re-execs the copy with --finish-self-update,
// then returns so the caller can exit without launching the JVM itself —
// the relaunched, updated rigger.exe (via FinishSelfUpdate) redoes the
// full launch flow once it's in place. forwardArgs is the original
// argv[1:] (e.g. a protocol-handler URI), preserved across the handoff.
func BeginSelfUpdate(appID string, forwardArgs []string) error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("riggerupdate: determine my own location: %w", err)
	}
	data, err := os.ReadFile(exePath)
	if err != nil {
		return fmt.Errorf("riggerupdate: read self: %w", err)
	}
	tempCopy := filepath.Join(os.TempDir(), "stage-rigger-update-"+appID+".exe")
	if err := os.WriteFile(tempCopy, data, 0o755); err != nil {
		return fmt.Errorf("riggerupdate: write %s: %w", tempCopy, err)
	}

	args := append([]string{"--finish-self-update", appID}, forwardArgs...)
	cmd := exec.Command(tempCopy, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("riggerupdate: launch %s: %w", tempCopy, err)
	}
	return cmd.Process.Release()
}

// FinishSelfUpdate is phase two, run by the %TEMP% copy BeginSelfUpdate
// launched: downloads and verifies the new rigger.exe, overwrites the real
// one in the install root, and relaunches it with forwardArgs. A failure
// at any point here falls back to relaunching the existing, unmodified
// rigger.exe rather than failing the launch outright — an update attempt
// should never be why the app doesn't open.
func FinishSelfUpdate(appID string, forwardArgs []string) error {
	appValues, err := winreg.ReadAppValues(appID)
	if err != nil {
		return fmt.Errorf("riggerupdate: read registry values: %w", err)
	}
	riggerPath := layout.RiggerExePath(appValues.InstallDir)

	if err := applyUpdate(appValues, riggerPath); err != nil {
		fmt.Fprintf(os.Stderr, "rigger: warning: self-update failed (%v); launching the current version instead\n", err)
	}

	cmd := exec.Command(riggerPath, forwardArgs...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("riggerupdate: relaunch %s: %w", riggerPath, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("riggerupdate: release relaunched process: %w", err)
	}

	scheduleSelfDelete()
	return nil
}

func applyUpdate(appValues winreg.AppValues, riggerPath string) error {
	m, err := manifest.Load(layout.ManifestPath(appValues.InstallDir))
	if err != nil {
		return fmt.Errorf("load manifest: %w", err)
	}
	if m.Rigger.Version == "" || m.Rigger.SHA256 == "" {
		return fmt.Errorf("manifest no longer declares a rigger update with a checksum")
	}

	client := proxydetect.Client(proxydetect.Result{Host: appValues.ProxyHost, Port: appValues.ProxyPort}, 5*time.Minute)
	tmpPath, err := jreprovision.DownloadVerified(client, m.RiggerDownloadURL(), m.Rigger.SHA256)
	if err != nil {
		return fmt.Errorf("download update: %w", err)
	}
	defer os.Remove(tmpPath)

	data, err := os.ReadFile(tmpPath)
	if err != nil {
		return fmt.Errorf("read downloaded update: %w", err)
	}
	if err := os.WriteFile(riggerPath, data, 0o755); err != nil {
		return fmt.Errorf("install update to %s: %w", riggerPath, err)
	}
	return nil
}

// scheduleSelfDelete marks this temporary copy of rigger.exe (running as
// this very process) for deletion on next reboot — it can't delete itself
// while running, same as cmd/uninstaller's own temp copy.
func scheduleSelfDelete() {
	selfPath, err := os.Executable()
	if err != nil {
		return
	}
	p, err := windows.UTF16PtrFromString(selfPath)
	if err != nil {
		return
	}
	_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
}
