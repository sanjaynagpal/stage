package shortcut

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// wscriptShellReadBack independently reads back a .lnk's properties via
// PowerShell's WScript.Shell COM automation object — a genuinely separate
// code path from this package's own IShellLinkW usage, so a match proves
// the file was written correctly, not just "didn't crash."
func wscriptShellReadBack(t *testing.T, path string) map[string]string {
	t.Helper()
	script := `$s = (New-Object -COM WScript.Shell).CreateShortcut("` + path + `"); ` +
		`Write-Output "TargetPath=$($s.TargetPath)"; ` +
		`Write-Output "Description=$($s.Description)"; ` +
		`Write-Output "IconLocation=$($s.IconLocation)"`
	out, err := exec.Command("powershell", "-NoProfile", "-Command", script).CombinedOutput()
	if err != nil {
		t.Fatalf("powershell readback: %v\n%s", err, out)
	}
	result := make(map[string]string)
	for rawLine := range strings.SplitSeq(strings.TrimSpace(string(out)), "\n") {
		line := strings.TrimSpace(rawLine)
		key, val, ok := strings.Cut(line, "=")
		if ok {
			result[key] = val
		}
	}
	return result
}

func TestCreateProducesAShortcutWScriptShellCanRead(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "ABC.lnk")
	targetPath := filepath.Join(dir, "rigger.exe")
	if err := os.WriteFile(targetPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	iconPath := filepath.Join(dir, "app.ico")
	if err := os.WriteFile(iconPath, []byte("fake-icon"), 0o644); err != nil {
		t.Fatal(err)
	}

	spec := Spec{
		Path:        linkPath,
		TargetPath:  targetPath,
		Description: "Launches ABC",
		IconPath:    iconPath,
		IconIndex:   0,
	}
	if err := Create(spec); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(linkPath); err != nil {
		t.Fatalf("expected %s to exist: %v", linkPath, err)
	}

	got := wscriptShellReadBack(t, linkPath)
	if got["TargetPath"] != targetPath {
		t.Errorf("TargetPath = %q, want %q", got["TargetPath"], targetPath)
	}
	if got["Description"] != spec.Description {
		t.Errorf("Description = %q, want %q", got["Description"], spec.Description)
	}
	wantIcon := iconPath + ",0"
	if got["IconLocation"] != wantIcon {
		t.Errorf("IconLocation = %q, want %q", got["IconLocation"], wantIcon)
	}
}

func TestCreateWithoutIconLeavesDefaultIcon(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "ABC.lnk")
	targetPath := filepath.Join(dir, "rigger.exe")
	if err := os.WriteFile(targetPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Create(Spec{Path: linkPath, TargetPath: targetPath, Description: "d"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(linkPath); err != nil {
		t.Fatalf("expected %s to exist: %v", linkPath, err)
	}
}

func TestCreateMakesParentDirectory(t *testing.T) {
	linkPath := filepath.Join(t.TempDir(), "nested", "dir", "ABC.lnk")
	targetPath := filepath.Join(t.TempDir(), "rigger.exe")

	if err := Create(Spec{Path: linkPath, TargetPath: targetPath, Description: "d"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := os.Stat(linkPath); err != nil {
		t.Fatalf("expected nested parent dirs to be created: %v", err)
	}
}

func TestRemoveDeletesShortcutAndIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	linkPath := filepath.Join(dir, "ABC.lnk")
	if err := Create(Spec{Path: linkPath, TargetPath: `C:\rigger.exe`, Description: "d"}); err != nil {
		t.Fatalf("Create: %v", err)
	}

	if err := Remove(linkPath); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(linkPath); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be gone", linkPath)
	}

	if err := Remove(linkPath); err != nil {
		t.Fatalf("Remove on an already-removed shortcut should not error, got: %v", err)
	}
}
