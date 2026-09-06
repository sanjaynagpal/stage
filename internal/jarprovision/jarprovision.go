// Package jarprovision downloads, verifies, and unpacks an application's
// jar archive on demand when Rigger finds the manifest's current version
// missing on disk, evicting old version folders beyond MaxRetainedVersions
// — the app-level analogue of internal/jreprovision, whose download-and-
// verify and eviction primitives it reuses directly rather than duplicating
// them (docs/REQUIREMENTS.md §3, §9). Unlike JRE/rigger.exe self-update,
// this fetch runs regardless of PackageMode — it's the always-on channel
// per docs/REQUIREMENTS.md §9b/§13-14.
package jarprovision

import (
	"fmt"
	"net/http"
	"os"

	"github.com/sanjaynagpal/stage/internal/archiveutil"
	"github.com/sanjaynagpal/stage/internal/jreprovision"
	"github.com/sanjaynagpal/stage/internal/layout"
)

// MaxRetainedVersions is the maximum number of app-version folders kept
// directly under the install root at once, mirroring
// jreprovision.MaxRetainedVersions (docs/REQUIREMENTS.md §3).
const MaxRetainedVersions = 2

// Provision downloads the archive at url via client, verifies it against
// expectedSHA256Hex, unpacks it to versionDir, and evicts version folders
// under root beyond MaxRetainedVersions (keeping the most recently
// provisioned ones). versionDir must be a "<root>/<version>" directory.
// client should be a proxy-aware client (e.g. proxydetect.Client) since,
// unlike jreprovision's installer-side callers, this runs from a live
// Rigger launch (docs/REQUIREMENTS.md §17-18).
func Provision(client *http.Client, root, url, expectedSHA256Hex, versionDir string) error {
	archivePath, err := jreprovision.DownloadVerified(client, url, expectedSHA256Hex)
	if err != nil {
		return fmt.Errorf("jarprovision: %w", err)
	}
	defer os.Remove(archivePath)

	if err := archiveutil.ExtractZip(archivePath, versionDir); err != nil {
		return fmt.Errorf("jarprovision: %w", err)
	}
	return jreprovision.EvictOldest(root, MaxRetainedVersions, layout.IsVersionDir)
}
