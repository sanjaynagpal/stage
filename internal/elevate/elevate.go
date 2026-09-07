// Package elevate detects whether the current process is running with
// administrator privileges, which drives install scope
// (docs/REQUIREMENTS.md §2): elevated → all-users, otherwise → per-user.
package elevate

import "golang.org/x/sys/windows"

// IsElevated reports whether the current process token has administrator
// privileges. GetCurrentProcessToken returns a pseudo-token that does not
// need to be closed.
func IsElevated() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// Relaunch re-execs exePath under an elevated token via the shell's "runas"
// verb, which triggers the UAC consent prompt. It does not wait for the new
// process — the caller is expected to exit immediately afterward and let
// the elevated instance do the actual work. Returns an error (e.g. the user
// declined the UAC prompt) without launching anything.
func Relaunch(exePath string) error {
	verb, err := windows.UTF16PtrFromString("runas")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(exePath)
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, nil, windows.SW_NORMAL)
}
