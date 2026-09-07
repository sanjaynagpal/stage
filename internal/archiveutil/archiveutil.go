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
	"time"
)

// ProgressFunc reports bytes processed so far against a known total — used
// both for a download in flight (internal/jreprovision.DownloadVerified) and
// for extraction here, so the two phases of provisioning a JRE/jar archive
// report progress the same way. total is non-positive when it isn't known
// (a download with no Content-Length; never the case for extraction, since
// a zip's uncompressed sizes are always in its central directory).
type ProgressFunc func(done, total int64)

// progressInterval throttles ProgressFunc calls so extracting many small
// files (a JRE archive easily has thousands) doesn't flood the caller —
// callers of ExtractZip typically log or otherwise render each call.
const progressInterval = 500 * time.Millisecond

// extractProgress accumulates bytes written across every file in the
// archive (not just the current one), so onProgress reports overall
// extraction progress rather than restarting at zero per file.
type extractProgress struct {
	total      int64
	written    int64
	lastReport time.Time
	onProgress ProgressFunc
}

func (p *extractProgress) Write(b []byte) (int, error) {
	n := len(b)
	p.written += int64(n)
	if p.onProgress != nil && (p.lastReport.IsZero() || time.Since(p.lastReport) >= progressInterval) {
		p.onProgress(p.written, p.total)
		p.lastReport = time.Now()
	}
	return n, nil
}

// ExtractZip extracts the zip archive at archivePath into destDir, which is
// created if it doesn't already exist. onProgress, if non-nil, is called
// periodically (at most every progressInterval) as bytes are written,
// keyed to the archive's total uncompressed size, plus once more after
// extraction completes so a final 100%-equivalent call is always
// delivered; may be nil.
func ExtractZip(archivePath, destDir string, onProgress ProgressFunc) error {
	r, err := zip.OpenReader(archivePath)
	if err != nil {
		return fmt.Errorf("archiveutil: open %s: %w", archivePath, err)
	}
	defer r.Close()

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("archiveutil: create %s: %w", destDir, err)
	}
	destClean := filepath.Clean(destDir)

	var total int64
	for _, f := range r.File {
		if !f.FileInfo().IsDir() {
			total += int64(f.UncompressedSize64)
		}
	}
	progress := &extractProgress{total: total, onProgress: onProgress}

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
		if err := extractFile(f, target, progress); err != nil {
			return err
		}
	}
	if onProgress != nil {
		onProgress(progress.written, total)
	}
	return nil
}

func extractFile(f *zip.File, target string, progress *extractProgress) error {
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

	if _, err := io.Copy(io.MultiWriter(out, progress), rc); err != nil {
		return fmt.Errorf("archiveutil: write %s: %w", target, err)
	}
	return nil
}
