// Package jreprovision downloads, verifies, and unpacks a JRE archive
// on demand, and enforces the max-2-retained-versions eviction policy
// (docs/REQUIREMENTS.md §3, §12-14). It is shared by the installer (initial
// bundled JRE — extracted from a local archive, not downloaded) and by
// maintain.exe (on-demand fetch of a JRE the manifest wants but that isn't
// present on disk yet).
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

// Provision downloads the archive at url, verifies it against
// expectedSHA256Hex, unpacks it to jreDir, and then evicts old JRE versions
// under root/jre beyond MaxRetainedVersions (keeping the most recently
// provisioned ones). jreDir must be a "jre/<version>" directory under root.
func Provision(root, url, expectedSHA256Hex, jreDir string) error {
	archivePath, err := download(url, expectedSHA256Hex)
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

func download(url, expectedSHA256Hex string) (string, error) {
	tmp, err := os.CreateTemp("", "stage-jre-*.zip")
	if err != nil {
		return "", fmt.Errorf("jreprovision: create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	client := http.Client{Timeout: 5 * time.Minute}
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
// recency signal.
func evictOldVersions(root string) error {
	jreRoot := filepath.Join(root, "jre")
	entries, err := os.ReadDir(jreRoot)
	if err != nil {
		return fmt.Errorf("jreprovision: list %s: %w", jreRoot, err)
	}

	type versionDir struct {
		path    string
		modTime time.Time
	}
	var dirs []versionDir
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		dirs = append(dirs, versionDir{path: filepath.Join(jreRoot, e.Name()), modTime: info.ModTime()})
	}
	if len(dirs) <= MaxRetainedVersions {
		return nil
	}

	sort.Slice(dirs, func(i, j int) bool { return dirs[i].modTime.After(dirs[j].modTime) })
	for _, d := range dirs[MaxRetainedVersions:] {
		if err := os.RemoveAll(d.path); err != nil {
			return fmt.Errorf("jreprovision: evict old JRE %s: %w", d.path, err)
		}
	}
	return nil
}
