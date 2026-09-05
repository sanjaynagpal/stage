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
