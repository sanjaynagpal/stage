// Package uierror reports a fatal error to the end user from a
// windowsgui-subsystem binary (rigger.exe, maintain.exe, uninstaller.exe)
// that has no console to print to. It shows a native message box so a
// launch failure is never silent, and also writes to stderr, which is
// harmless (goes nowhere) when no console is attached but helpful during
// development/manual testing.
package uierror

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// Fatalf reports a fatal error via a native message box titled by caption,
// then exits the process with status 1. It never returns.
func Fatalf(caption, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintln(os.Stderr, caption+": "+msg)
	showMessageBox(caption, msg)
	os.Exit(1)
}

func showMessageBox(caption, msg string) {
	captionPtr, err := windows.UTF16PtrFromString(caption)
	if err != nil {
		return
	}
	msgPtr, err := windows.UTF16PtrFromString(msg)
	if err != nil {
		return
	}
	_, _ = windows.MessageBox(0, msgPtr, captionPtr, windows.MB_OK|windows.MB_ICONERROR)
}
