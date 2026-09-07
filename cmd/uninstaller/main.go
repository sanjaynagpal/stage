// Command unins is Stage's generated uninstaller (docs/REQUIREMENTS.md
// §16). Like rigger.exe, it is a generic, prebuilt-once binary with zero
// per-app compiled state — it derives its app identity from its own
// install folder name.
//
// Deleting an install directory while unins.exe itself runs from inside it
// is impossible on Windows (you can't remove a running exe's own file) —
// the standard approach, used here, is self-copy-and-relaunch: the first
// invocation copies itself to %TEMP% and re-execs the copy with
// --finish-uninstall, which does the actual teardown from outside root.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"golang.org/x/sys/windows"

	"github.com/sanjaynagpal/stage/internal/console"
	"github.com/sanjaynagpal/stage/internal/elevate"
	"github.com/sanjaynagpal/stage/internal/fileassoc"
	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/payload"
	"github.com/sanjaynagpal/stage/internal/procscan"
	"github.com/sanjaynagpal/stage/internal/protocolhandler"
	"github.com/sanjaynagpal/stage/internal/shortcut"
	"github.com/sanjaynagpal/stage/internal/winreg"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "unins: error:", err)
		console.ReadLine("\nPress Enter to exit... ")
		os.Exit(1)
	}
}

func run() error {
	finish := flag.Bool("finish-uninstall", false, "internal: run the second phase of uninstall from outside the install directory")
	flag.Parse()

	if *finish {
		if flag.NArg() < 2 {
			return fmt.Errorf("unins: --finish-uninstall requires <root> <appID>")
		}
		return finishUninstall(flag.Arg(0), flag.Arg(1))
	}
	return startUninstall()
}

func startUninstall() error {
	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("unins: could not determine my own location: %w", err)
	}
	appID := layout.AppIDFromExePath(exePath)
	root := layout.RootDirFromExePath(exePath)

	if elevateNeeded(appID) {
		fmt.Println("This app was installed for all users — requesting administrator privileges...")
		if err := elevate.Relaunch(exePath); err != nil {
			return fmt.Errorf("unins: this app must be uninstalled as Administrator: %w", err)
		}
		return nil
	}

	prompt := fmt.Sprintf("Uninstall %s? This will remove:\n  %s\n  %s", appID, root, layout.DataDir(appID))
	if !console.Confirm(prompt, false) {
		fmt.Println("Uninstall cancelled.")
		console.ReadLine("\nPress Enter to exit... ")
		return nil
	}

	tempCopy := filepath.Join(os.TempDir(), "stage-uninstall-"+appID+".exe")
	data, err := os.ReadFile(exePath)
	if err != nil {
		return fmt.Errorf("unins: read self: %w", err)
	}
	if err := os.WriteFile(tempCopy, data, 0o755); err != nil {
		return fmt.Errorf("unins: write %s: %w", tempCopy, err)
	}

	cmd := exec.Command(tempCopy, "--finish-uninstall", root, appID)
	// exec.Cmd redirects an unset Stdin/Stdout/Stderr to the null device —
	// without these, finishUninstall's prompts/warnings/success message
	// would silently go nowhere and its RetryCancel would busy-loop on
	// instant EOF instead of waiting for real input.
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("unins: launch %s: %w", tempCopy, err)
	}
	return cmd.Process.Release()
}

// elevateNeeded reports whether appID's install is AllUsers-scoped (under
// Program Files/HKLM) and the current process isn't already elevated —
// removing that install's registry keys and directory will fail partway
// through otherwise. A registry read failure here is left for the normal
// flow below to hit and report; this check is best-effort only.
func elevateNeeded(appID string) bool {
	if elevate.IsElevated() {
		return false
	}
	appValues, err := winreg.ReadAppValues(appID)
	if err != nil {
		return false
	}
	return appValues.InstallScope == layout.ScopeAllUsers
}

func finishUninstall(root, appID string) error {
	// The original unins.exe (phase one, above) is exiting concurrently
	// with this process starting — this check may transiently see it as
	// still "running under root" for a moment; a Retry (or just pressing
	// Enter) resolves it almost immediately in practice.
	if err := console.RetryCancel(appID+" appears to be running — please close it", func() error {
		running, err := procscan.RunningUnder(root)
		if err != nil {
			return err
		}
		if len(running) > 0 {
			return fmt.Errorf("%d process(es) still running under %s", len(running), root)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("unins: %w", err)
	}

	warnings := 0
	warn := func(format string, args ...any) {
		warnings++
		fmt.Fprintf(os.Stderr, "unins: warning: "+format+"\n", args...)
	}

	appValues, err := winreg.ReadAppValues(appID)
	if err != nil {
		return fmt.Errorf("unins: read registry values: %w", err)
	}
	record, err := payload.LoadInstallRecord(layout.InstallRecordPath(root))
	if err != nil {
		warn("could not read install record (%v); some registrations may be left behind", err)
	}

	if record.StartMenuShortcut != "" {
		if err := shortcut.Remove(record.StartMenuShortcut); err != nil {
			warn("%v", err)
		}
	}
	if record.DesktopShortcut != "" {
		if err := shortcut.Remove(record.DesktopShortcut); err != nil {
			warn("%v", err)
		}
	}
	if record.ProtocolScheme != "" {
		if err := protocolhandler.Unregister(appValues.InstallScope, record.ProtocolScheme); err != nil {
			warn("%v", err)
		}
	}
	for _, fa := range record.FileAssociations {
		if err := fileassoc.Unregister(appValues.InstallScope, fa.Extension, fa.ProgID); err != nil {
			warn("%v", err)
		}
	}

	if err := winreg.DeleteAppKey(appValues.InstallScope, appID); err != nil {
		warn("%v", err)
	}
	if err := winreg.DeleteUninstallValues(appValues.InstallScope, appID); err != nil {
		warn("%v", err)
	}

	if err := removeDirWithRetry(root); err != nil {
		return fmt.Errorf("unins: remove %s: %w", root, err)
	}

	// DataDir is always the current user's own profile regardless of
	// InstallScope (layout.DataDir ignores scope) — for an AllUsers install
	// this only cleans up the user running the uninstall, not every
	// profile that ever launched the app, matching how per-user app data
	// is scoped everywhere else in this codebase. Uses the same retry as
	// root: observed empty-handed on a real elevated run, most likely a
	// transient hold from something else (AV/indexing) rather than a real
	// permission issue — a plain non-admin RemoveAll on the same path
	// right afterward succeeded immediately.
	if err := removeDirWithRetry(layout.DataDir(appID)); err != nil {
		warn("could not remove data directory: %v", err)
	}

	// Best-effort: only the temp copy of unins.exe itself needs delayed
	// cleanup (it's still running as this very process, so it can't
	// delete itself) — the whole app has already been removed above.
	if selfPath, err := os.Executable(); err == nil {
		if p, err := windows.UTF16PtrFromString(selfPath); err == nil {
			_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
		}
	}

	if warnings > 0 {
		fmt.Printf("%s has been uninstalled, with %d warning(s) — see above for details.\n", appID, warnings)
	} else {
		fmt.Printf("%s has been uninstalled.\n", appID)
	}
	console.ReadLine("\nPress Enter to exit... ")
	return nil
}

// removeDirWithRetry retries os.RemoveAll briefly against path: the process
// that launched this one (phase one, above, or an app process that just
// exited) may still transiently hold a file under it open for a moment.
func removeDirWithRetry(path string) error {
	var lastErr error
	for range 10 {
		if err := os.RemoveAll(path); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return lastErr
}
