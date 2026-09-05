// Package diskspace checks free disk space at an install target, part of
// the installer's prerequisite check (docs/REQUIREMENTS.md §16 step 1).
package diskspace

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// AvailableBytes returns the free space available to the caller on the
// volume containing path. path need not exist yet — the nearest existing
// ancestor directory is queried instead (relevant when checking a not-yet-
// created install root).
func AvailableBytes(path string) (uint64, error) {
	dir, err := nearestExistingAncestor(path)
	if err != nil {
		return 0, err
	}
	ptr, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, fmt.Errorf("diskspace: encode path %s: %w", dir, err)
	}

	var freeAvailable, total, totalFree uint64
	if err := windows.GetDiskFreeSpaceEx(ptr, &freeAvailable, &total, &totalFree); err != nil {
		return 0, fmt.Errorf("diskspace: GetDiskFreeSpaceEx(%s): %w", dir, err)
	}
	return freeAvailable, nil
}

// HasSpaceFor reports whether at least requiredBytes are free on the volume
// containing path.
func HasSpaceFor(path string, requiredBytes uint64) (bool, error) {
	available, err := AvailableBytes(path)
	if err != nil {
		return false, err
	}
	return available >= requiredBytes, nil
}

func nearestExistingAncestor(path string) (string, error) {
	dir := filepath.Clean(path)
	for {
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("diskspace: no existing ancestor directory found for %s", path)
		}
		dir = parent
	}
}
