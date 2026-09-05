package procscan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRunningUnderFindsOwnProcess(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	root := filepath.Dir(self)

	matches, err := RunningUnder(root)
	if err != nil {
		t.Fatalf("RunningUnder: %v", err)
	}

	pid := uint32(os.Getpid())
	found := false
	for _, m := range matches {
		if m.PID == pid {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected RunningUnder(%q) to include this test process (pid %d), got %+v", root, pid, matches)
	}
}

func TestRunningUnderFindsNothingForUnrelatedRoot(t *testing.T) {
	matches, err := RunningUnder(`Z:\definitely\not\a\real\install\root`)
	if err != nil {
		t.Fatalf("RunningUnder: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no matches for an unrelated root, got %+v", matches)
	}
}
