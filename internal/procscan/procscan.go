// Package procscan enumerates running processes to detect whether an
// install target path is locked by an already-running rigger.exe/java.exe
// instance, part of the installer's prerequisite check
// (docs/REQUIREMENTS.md §16 step 1).
package procscan

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ProcessInfo describes one running process found during a scan.
type ProcessInfo struct {
	PID     uint32
	ExePath string // full image path; empty if it could not be resolved
}

// RunningUnder returns every running process whose executable path is
// located at or under root (case-insensitive, matching Windows path
// semantics). Processes whose image path can't be resolved (e.g. protected
// system processes) are silently skipped rather than reported as false
// positives.
func RunningUnder(root string) ([]ProcessInfo, error) {
	all, err := snapshotProcesses()
	if err != nil {
		return nil, err
	}

	rootClean := strings.ToLower(filepath.Clean(root))
	var matches []ProcessInfo
	for _, p := range all {
		if p.ExePath == "" {
			continue
		}
		exeClean := strings.ToLower(filepath.Clean(p.ExePath))
		if exeClean == rootClean || strings.HasPrefix(exeClean, rootClean+string(filepath.Separator)) {
			matches = append(matches, p)
		}
	}
	return matches, nil
}

func snapshotProcesses() ([]ProcessInfo, error) {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil, fmt.Errorf("procscan: CreateToolhelp32Snapshot: %w", err)
	}
	defer windows.CloseHandle(snap)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))

	var result []ProcessInfo
	err = windows.Process32First(snap, &entry)
	for err == nil {
		pid := entry.ProcessID
		result = append(result, ProcessInfo{PID: pid, ExePath: queryExePath(pid)})
		err = windows.Process32Next(snap, &entry)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return nil, fmt.Errorf("procscan: Process32Next: %w", err)
	}
	return result, nil
}

// queryExePath best-effort resolves a process's full image path. Processes
// this caller lacks access to (protected/system processes, or ones that
// exited between snapshotting and querying) resolve to "" rather than
// erroring the whole scan.
func queryExePath(pid uint32) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(h)

	buf := make([]uint16, windows.MAX_PATH)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(h, 0, &buf[0], &size); err != nil {
		return ""
	}
	return windows.UTF16ToString(buf[:size])
}
