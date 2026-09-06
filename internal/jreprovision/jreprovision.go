// Package jreprovision downloads, verifies, and unpacks a JRE archive
// on demand, and enforces the max-2-retained-versions eviction policy
// (docs/REQUIREMENTS.md §3, §12-14). It is shared by the installer (initial
// bundled JRE — extracted from a local archive via ProvisionLocal, not
// downloaded) and by cmd/rigger itself (on-demand fetch, in-process, of a
// JRE version the manifest wants but that isn't present on disk yet, via
// Provision). Its download-and-verify and eviction primitives are also
// reused by internal/jarprovision, the app-level analogue that fetches jars
// instead of a JRE.
package jreprovision

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sanjaynagpal/stage/internal/archiveutil"
)

// MaxRetainedVersions is the maximum number of JRE versions kept under
// <root>/jre at once (docs/REQUIREMENTS.md §3).
const MaxRetainedVersions = 2

// Provision downloads the archive at url via client, verifies it against
// expectedSHA256Hex, unpacks it to jreDir, and then evicts old JRE versions
// under root/jre beyond MaxRetainedVersions (keeping the most recently
// provisioned ones). jreDir must be a "jre/<version>" directory under root.
// client should be a proxy-aware client (e.g. proxydetect.Client) when
// called from a live Rigger process, mirroring jarprovision.Provision
// (docs/REQUIREMENTS.md §17-18).
func Provision(client *http.Client, root, url, expectedSHA256Hex, jreDir string) error {
	archivePath, err := DownloadVerified(client, url, expectedSHA256Hex)
	if err != nil {
		return err
	}
	defer os.Remove(archivePath)

	return provisionFromVerifiedFile(archivePath, jreDir, root)
}

// ProvisionLocal verifies an archive already on disk (the installer's own
// bundled JRE, extracted from its embedded payload to a temp file) against
// expectedSHA256Hex, then unpacks/evicts identically to Provision. Used by
// the installer at both initial-install and upgrade-in-place time — an
// upgrade-in-place run is just a re-invocation of the installer, so it must
// run the same MaxRetainedVersions eviction Provision does, not skip it
// (docs/REQUIREMENTS.md §9b).
func ProvisionLocal(root, archivePath, expectedSHA256Hex, jreDir string) error {
	if err := verifyChecksum(archivePath, expectedSHA256Hex); err != nil {
		return err
	}
	return provisionFromVerifiedFile(archivePath, jreDir, root)
}

func provisionFromVerifiedFile(archivePath, jreDir, root string) error {
	if err := archiveutil.ExtractZip(archivePath, jreDir); err != nil {
		return err
	}
	return evictOldVersions(root)
}

func verifyChecksum(path, expectedSHA256Hex string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("jreprovision: open %s: %w", path, err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return fmt.Errorf("jreprovision: read %s: %w", path, err)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(sum, expectedSHA256Hex) {
		return fmt.Errorf("jreprovision: checksum mismatch for %s: got %s, want %s", path, sum, expectedSHA256Hex)
	}
	return nil
}

// DownloadVerified downloads the archive at url via client, verifies it
// against expectedSHA256Hex, and returns the path to a temp file holding it
// (the caller must os.Remove it). Shared by JRE and app-jar provisioning —
// both need "fetch a zip and trust it only after checksum verification,"
// routed through whichever *http.Client the caller already has (e.g.
// cmd/rigger's proxy-aware client, docs/REQUIREMENTS.md §17-18).
func DownloadVerified(client *http.Client, url, expectedSHA256Hex string) (string, error) {
	tmp, err := os.CreateTemp("", "stage-download-*.zip")
	if err != nil {
		return "", fmt.Errorf("jreprovision: create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	resp, err := client.Get(url)
	if err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("jreprovision: download %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("jreprovision: download %s: unexpected status %s", url, resp.Status)
	}

	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(tmp, h), resp.Body)
	closeErr := tmp.Close()
	if copyErr != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("jreprovision: write download %s: %w", url, copyErr)
	}
	if closeErr != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("jreprovision: finalize download %s: %w", url, closeErr)
	}

	sum := hex.EncodeToString(h.Sum(nil))
	if !strings.EqualFold(sum, expectedSHA256Hex) {
		os.Remove(tmpPath)
		return "", fmt.Errorf("jreprovision: checksum mismatch for %s: got %s, want %s", url, sum, expectedSHA256Hex)
	}
	return tmpPath, nil
}

// evictOldVersions removes JRE version directories under root/jre beyond
// MaxRetainedVersions, oldest first, using each directory's own
// modification time (set when it was created/last written to) as the
// recency signal. jre/ holds only version directories, so every entry is
// eligible.
func evictOldVersions(root string) error {
	return EvictOldest(filepath.Join(root, "jre"), MaxRetainedVersions, func(string) bool { return true })
}

// EvictOldest removes directories directly under dirRoot beyond keep, oldest
// first by modification time, restricted to entries for which include
// returns true — the shared core behind jre/ eviction here (where every
// entry qualifies) and internal/jarprovision's eviction (where include is
// layout.IsVersionDir, since the install root also holds Rigger's own fixed
// files alongside app-version directories).
func EvictOldest(dirRoot string, keep int, include func(name string) bool) error {
	entries, err := os.ReadDir(dirRoot)
	if err != nil {
		return fmt.Errorf("jreprovision: list %s: %w", dirRoot, err)
	}

	type dirInfo struct {
		path    string
		modTime time.Time
	}
	var dirs []dirInfo
	for _, e := range entries {
		if !e.IsDir() || !include(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, dirInfo{path: filepath.Join(dirRoot, e.Name()), modTime: info.ModTime()})
	}
	if len(dirs) <= keep {
		return nil
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].modTime.After(dirs[j].modTime) })
	for _, d := range dirs[keep:] {
		if err := os.RemoveAll(d.path); err != nil {
			return fmt.Errorf("jreprovision: evict %s: %w", d.path, err)
		}
	}
	return nil
}
