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
	"path/filepath"

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
// Rigger launch (docs/REQUIREMENTS.md §17-18). onDownloadProgress/
// onExtractProgress, if non-nil, are reported through to
// jreprovision.DownloadVerified/archiveutil.ExtractZip unchanged — see
// their doc comments; either may be nil.
func Provision(client *http.Client, root, url, expectedSHA256Hex, versionDir string, onDownloadProgress, onExtractProgress jreprovision.ProgressFunc) error {
	archivePath, err := jreprovision.DownloadVerified(client, url, expectedSHA256Hex, onDownloadProgress)
	if err != nil {
		return fmt.Errorf("jarprovision: %w", err)
	}
	defer os.Remove(archivePath)

	if err := archiveutil.ExtractZip(archivePath, versionDir, onExtractProgress); err != nil {
		return fmt.Errorf("jarprovision: %w", err)
	}
	return jreprovision.EvictOldest(root, MaxRetainedVersions, layout.IsVersionDir)
}

// JarSpec identifies one individually-downloadable jar file — the per-file
// alternative to Provision's single zip archive (manifest.Manifest.Jars).
// Deliberately a local type, not manifest.JarSpec: this package doesn't
// need to import manifest just for this shape (mirroring
// internal/wizard.CheckResult's decoupling from internal/doctor.Check) —
// callers convert.
type JarSpec struct {
	// Path is the jar's location relative to versionDir (e.g. "app.jar" or
	// "lib/gson-2.10.jar") — also appended to baseURL to derive its
	// download URL.
	Path   string
	SHA256 string
}

// FileProgressFunc reports byte-level progress for one named file within a
// multi-file operation — done/total are that file's own byte counts, not
// aggregated across every file in jars (unlike Provision's single-archive
// onExtractProgress, which does aggregate, since there's exactly one
// thing in flight at a time there too — extracting a zip's many internal
// files as one continuous operation. Here each jar is its own separate
// download, so per-file identity is the more useful signal).
type FileProgressFunc func(path string, done, total int64)

// ProvisionJars downloads each of jars individually — baseURL+jar.Path per
// file — into versionDir, verifies each against its own checksum, and
// evicts old version folders exactly like Provision. An app chooses this
// or Provision via manifest.Manifest.Jars/ArtifactSHA256, never both
// (enforced by manifest.Validate). onFile, if non-nil, is called once per
// jar immediately before it starts downloading (index/total are
// 1-based/inclusive, e.g. onFile("app.jar", 1, 3)) — the natural place for
// a caller to log/display which jar is currently in flight, distinct from
// onProgress's byte-level report for whichever file that is.
func ProvisionJars(client *http.Client, root, baseURL string, jars []JarSpec, versionDir string, onFile func(path string, index, total int), onProgress FileProgressFunc) error {
	for i, j := range jars {
		if onFile != nil {
			onFile(j.Path, i+1, len(jars))
		}

		var perFile jreprovision.ProgressFunc
		if onProgress != nil {
			perFile = func(done, total int64) { onProgress(j.Path, done, total) }
		}

		archivePath, err := jreprovision.DownloadVerified(client, baseURL+j.Path, j.SHA256, perFile)
		if err != nil {
			return fmt.Errorf("jarprovision: download %s: %w", j.Path, err)
		}
		installErr := installVerifiedFile(archivePath, filepath.Join(versionDir, j.Path))
		os.Remove(archivePath)
		if installErr != nil {
			return installErr
		}
	}
	return jreprovision.EvictOldest(root, MaxRetainedVersions, layout.IsVersionDir)
}

// installVerifiedFile copies a downloaded-and-checksum-verified temp file
// to its final destination within versionDir, creating any parent
// directory a nested Path (e.g. "lib/gson-2.10.jar") needs. A plain
// os.Rename would be simpler but isn't safe here: the temp file
// (os.CreateTemp("", ...)) and versionDir aren't guaranteed to be on the
// same volume (e.g. an all-users install root under a different drive
// than %TEMP%), and Rename fails across volumes.
func installVerifiedFile(archivePath, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return fmt.Errorf("jarprovision: create %s: %w", filepath.Dir(dest), err)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("jarprovision: read downloaded %s: %w", archivePath, err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("jarprovision: write %s: %w", dest, err)
	}
	return nil
}
