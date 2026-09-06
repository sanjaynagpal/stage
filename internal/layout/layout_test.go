package layout

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootDir(t *testing.T) {
	os.Setenv("ProgramFiles", `C:\Program Files`)
	os.Setenv("LocalAppData", `C:\Users\me\AppData\Local`)

	if got, want := RootDir(ScopeAllUsers, "ABC"), `C:\Program Files\ABC`; got != want {
		t.Errorf("RootDir(AllUsers) = %q, want %q", got, want)
	}
	if got, want := RootDir(ScopePerUser, "ABC"), `C:\Users\me\AppData\Local\ABC`; got != want {
		t.Errorf("RootDir(PerUser) = %q, want %q", got, want)
	}
}

func TestDataDirIgnoresScope(t *testing.T) {
	os.Setenv("AppData", `C:\Users\me\AppData\Roaming`)
	if got, want := DataDir("ABC"), `C:\Users\me\AppData\Roaming\ABC`; got != want {
		t.Errorf("DataDir = %q, want %q", got, want)
	}
}

func TestAppIDFromExePath(t *testing.T) {
	exe := filepath.Join(`C:\Users\me\AppData\Local\ABC`, "rigger.exe")
	if got, want := AppIDFromExePath(exe), "ABC"; got != want {
		t.Errorf("AppIDFromExePath(%q) = %q, want %q", exe, got, want)
	}
}

func TestRootDirFromExePath(t *testing.T) {
	root := `C:\Program Files\ABC`
	exe := filepath.Join(root, "rigger.exe")
	if got := RootDirFromExePath(exe); got != root {
		t.Errorf("RootDirFromExePath(%q) = %q, want %q", exe, got, root)
	}
}

func TestJREAndVersionDirs(t *testing.T) {
	root := `C:\Program Files\ABC`
	if got, want := JREDir(root), filepath.Join(root, "jre"); got != want {
		t.Errorf("JREDir = %q, want %q", got, want)
	}
	if got, want := JREVersionDir(root, "21.0.2+13"), filepath.Join(root, "jre", "21.0.2+13"); got != want {
		t.Errorf("JREVersionDir = %q, want %q", got, want)
	}
	if got, want := VersionDir(root, "1.5.0"), filepath.Join(root, "1.5.0"); got != want {
		t.Errorf("VersionDir = %q, want %q", got, want)
	}
}

func TestIsVersionDir(t *testing.T) {
	for _, reserved := range []string{"jre", "rigger.exe", "unins.exe", "maintain.exe", "manifest.json", "app.ico", "install-record.json"} {
		if IsVersionDir(reserved) {
			t.Errorf("IsVersionDir(%q) = true, want false", reserved)
		}
	}
	for _, version := range []string{"1.5.0", "2.0.0-beta", "1.0.0"} {
		if !IsVersionDir(version) {
			t.Errorf("IsVersionDir(%q) = false, want true", version)
		}
	}
}

func TestStartMenuDirMatchesScope(t *testing.T) {
	os.Setenv("ProgramData", `C:\ProgramData`)
	os.Setenv("AppData", `C:\Users\me\AppData\Roaming`)

	if got, want := StartMenuDir(ScopeAllUsers, "ABC"), filepath.Join(`C:\ProgramData`, "Microsoft", "Windows", "Start Menu", "Programs", "ABC"); got != want {
		t.Errorf("StartMenuDir(AllUsers) = %q, want %q", got, want)
	}
	if got, want := StartMenuDir(ScopePerUser, "ABC"), filepath.Join(`C:\Users\me\AppData\Roaming`, "Microsoft", "Windows", "Start Menu", "Programs", "ABC"); got != want {
		t.Errorf("StartMenuDir(PerUser) = %q, want %q", got, want)
	}
}

func TestDesktopDirMatchesScope(t *testing.T) {
	os.Setenv("Public", `C:\Users\Public`)
	os.Setenv("UserProfile", `C:\Users\me`)

	if got, want := DesktopDir(ScopeAllUsers), filepath.Join(`C:\Users\Public`, "Desktop"); got != want {
		t.Errorf("DesktopDir(AllUsers) = %q, want %q", got, want)
	}
	if got, want := DesktopDir(ScopePerUser), filepath.Join(`C:\Users\me`, "Desktop"); got != want {
		t.Errorf("DesktopDir(PerUser) = %q, want %q", got, want)
	}
}
