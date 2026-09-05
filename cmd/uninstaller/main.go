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

	if !console.Confirm(fmt.Sprintf("Uninstall %s?", appID), false) {
		fmt.Println("Uninstall cancelled.")
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
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("unins: launch %s: %w", tempCopy, err)
	}
	return cmd.Process.Release()
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

	appValues, err := winreg.ReadAppValues(appID)
	if err != nil {
		return fmt.Errorf("unins: read registry values: %w", err)
	}
	record, err := payload.LoadInstallRecord(layout.InstallRecordPath(root))
	if err != nil {
		fmt.Fprintf(os.Stderr, "unins: warning: could not read install record (%v); some registrations may be left behind\n", err)
	}

	if record.StartMenuShortcut != "" {
		if err := shortcut.Remove(record.StartMenuShortcut); err != nil {
			fmt.Fprintf(os.Stderr, "unins: warning: %v\n", err)
		}
	}
	if record.DesktopShortcut != "" {
		if err := shortcut.Remove(record.DesktopShortcut); err != nil {
			fmt.Fprintf(os.Stderr, "unins: warning: %v\n", err)
		}
	}
	if record.ProtocolScheme != "" {
		if err := protocolhandler.Unregister(appValues.InstallScope, record.ProtocolScheme); err != nil {
			fmt.Fprintf(os.Stderr, "unins: warning: %v\n", err)
		}
	}
	for _, fa := range record.FileAssociations {
		if err := fileassoc.Unregister(appValues.InstallScope, fa.Extension, fa.ProgID); err != nil {
			fmt.Fprintf(os.Stderr, "unins: warning: %v\n", err)
		}
	}

	if err := winreg.DeleteAppKey(appValues.InstallScope, appID); err != nil {
		fmt.Fprintf(os.Stderr, "unins: warning: %v\n", err)
	}
	if err := winreg.DeleteUninstallValues(appValues.InstallScope, appID); err != nil {
		fmt.Fprintf(os.Stderr, "unins: warning: %v\n", err)
	}

	if err := removeDirWithRetry(root); err != nil {
		return fmt.Errorf("unins: remove %s: %w", root, err)
	}

	// Best-effort: only the temp copy of unins.exe itself needs delayed
	// cleanup (it's still running as this very process, so it can't
	// delete itself) — the whole app has already been removed above.
	if selfPath, err := os.Executable(); err == nil {
		if p, err := windows.UTF16PtrFromString(selfPath); err == nil {
			_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
		}
	}

	fmt.Printf("%s has been uninstalled.\n", appID)
	return nil
}

// removeDirWithRetry retries os.RemoveAll briefly: the process that
// launched this one (phase one, above, or an app process that just exited)
// may still transiently hold a file under root open for a moment.
func removeDirWithRetry(root string) error {
	var lastErr error
	for range 10 {
		if err := os.RemoveAll(root); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(300 * time.Millisecond)
	}
	return lastErr
}
