// Package archiveutil extracts zip archives safely, rejecting zip-slip path
// traversal. Archives are extracted verbatim (flat) — Stage's own archive
// convention requires JRE/artifact zips to already have their real content
// (bin/, lib/, jars, ...) directly at the archive root, with no vendor-style
// wrapper directory (e.g. NOT "jdk-21.0.2+13-jre/bin/..."). Whoever prepares
// an archive for Stage (a build script, stagebuild's own JRE-bundling step)
// is responsible for stripping any such wrapper before publishing it —
// auto-detecting a wrapper directory here would be ambiguous (a single-file
// archive, or one that legitimately only contains files under one real
// subdirectory, looks indistinguishable from a wrapped one).
package archiveutil

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractZip extracts the zip archive at archivePath into destDir, which is
// created if it doesn't already exist.
func ExtractZip(archivePath, destDir string) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("archiveutil: open %s: %w", archivePath, err)
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("archiveutil: create %s: %w", destDir, err)
	}
	destClean := filepath.Clean(destDir)

	for _, f := range r.File {
		target := filepath.Join(destClean, filepath.FromSlash(f.Name))
		if target != destClean && !strings.HasPrefix(target, destClean+string(os.PathSeparator)) {
			return fmt.Errorf("archiveutil: entry %q in %s escapes the destination directory", f.Name, archivePath)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("archiveutil: create %s: %w", target, err)
			}
			continue
		}
		if err := extractFile(f, target); err != nil {
			return err
		}
	}
	return nil
}

func extractFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return fmt.Errorf("archiveutil: create %s: %w", filepath.Dir(target), err)
	}

	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("archiveutil: open entry %s: %w", f.Name, err)
	}
	defer rc.Close()

	mode := f.Mode()
	if mode == 0 {
		mode = 0o644
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("archiveutil: create %s: %w", target, err)
	}
	defer out.Close()

	if _, err := io.Copy(out, rc); err != nil {
		return fmt.Errorf("archiveutil: write %s: %w", target, err)
	}
	return nil
}
