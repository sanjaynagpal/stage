package wizard

import (
	"os"
	"os/exec"
	"path/filepath"
)

// edgePaths are msedge.exe's well-known install locations, checked in
// order. Edge is guaranteed present on the Windows 10 1803+/11 target —
// the same Runtime-presence assumption docs/REQUIREMENTS.md §11b already
// made for WebView2 — so this is the preferred, chrome-less path.
var edgePaths = []string{
	filepath.Join(os.Getenv("ProgramFiles(x86)"), "Microsoft", "Edge", "Application", "msedge.exe"),
	filepath.Join(os.Getenv("ProgramFiles"), "Microsoft", "Edge", "Application", "msedge.exe"),
	filepath.Join(os.Getenv("LocalAppData"), "Microsoft", "Edge", "Application", "msedge.exe"),
}

// openBrowser opens url in a chrome-less Edge "app window" if msedge.exe is
// found at one of its known install paths, falling back to the OS default
// browser (normal tab chrome) otherwise. Both are fully functional; only
// the chrome-less polish differs.
func openBrowser(url string) error {
	for _, p := range edgePaths {
		if _, err := os.Stat(p); err == nil {
			return exec.Command(p, "--app="+url, "--window-size=760,640").Start()
		}
	}
	return exec.Command("cmd", "/c", "start", "", url).Start()
}
