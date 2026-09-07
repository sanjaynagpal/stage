package riggerupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/winreg"
)

func TestNeedsUpdate(t *testing.T) {
	cases := []struct {
		manifestVersion string
		want            bool
	}{
		{"", false},
		{Version, false},
		{"9.9.9", true},
	}
	for _, c := range cases {
		if got := NeedsUpdate(c.manifestVersion); got != c.want {
			t.Errorf("NeedsUpdate(%q) = %v, want %v", c.manifestVersion, got, c.want)
		}
	}
}

func writeManifest(t *testing.T, root, riggerVersion, riggerSHA256 string) {
	t.Helper()
	content := `{
		"appId": "ABC", "appName": "ABC", "version": "1.0.0", "environment": "PROD",
		"manifestServerUrl": "http://example.com/abc/manifest.json",
		"runtime": {"javaVersion": "21.0.2+13", "path": "jre/21.0.2+13"},
		"classpath": ["1.0.0/app.jar"], "mainClass": "com.example.abc.Main",
		"shortcut": {"name": "ABC", "description": "d", "startMenu": true, "desktop": true},
		"rigger": {"version": "` + riggerVersion + `", "sha256": "` + riggerSHA256 + `"}
	}`
	if err := os.WriteFile(layout.ManifestPath(root), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestApplyUpdateDownloadsVerifiesAndOverwrites(t *testing.T) {
	root := t.TempDir()
	newBytes := []byte("new rigger.exe bytes")
	sum := sha256.Sum256(newBytes)
	sumHex := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(newBytes)
	}))
	defer srv.Close()

	// The manifest's manifestServerUrl's directory is what RiggerDownloadURL
	// derives the update URL from, so point it at our test server.
	content := `{
		"appId": "ABC", "appName": "ABC", "version": "1.0.0", "environment": "PROD",
		"manifestServerUrl": "` + srv.URL + `/abc/manifest.json",
		"runtime": {"javaVersion": "21.0.2+13", "path": "jre/21.0.2+13"},
		"classpath": ["1.0.0/app.jar"], "mainClass": "com.example.abc.Main",
		"shortcut": {"name": "ABC", "description": "d", "startMenu": true, "desktop": true},
		"rigger": {"version": "9.9.9", "sha256": "` + sumHex + `"}
	}`
	if err := os.WriteFile(layout.ManifestPath(root), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	riggerPath := filepath.Join(root, "rigger.exe")
	if err := os.WriteFile(riggerPath, []byte("old rigger.exe bytes"), 0o755); err != nil {
		t.Fatal(err)
	}

	appValues := winreg.AppValues{InstallDir: root}
	if err := applyUpdate(appValues, riggerPath); err != nil {
		t.Fatalf("applyUpdate: %v", err)
	}

	got, err := os.ReadFile(riggerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(newBytes) {
		t.Fatalf("rigger.exe content = %q, want %q", got, newBytes)
	}
}

func TestApplyUpdateRejectsChecksumMismatch(t *testing.T) {
	root := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("new rigger.exe bytes"))
	}))
	defer srv.Close()

	content := `{
		"appId": "ABC", "appName": "ABC", "version": "1.0.0", "environment": "PROD",
		"manifestServerUrl": "` + srv.URL + `/abc/manifest.json",
		"runtime": {"javaVersion": "21.0.2+13", "path": "jre/21.0.2+13"},
		"classpath": ["1.0.0/app.jar"], "mainClass": "com.example.abc.Main",
		"shortcut": {"name": "ABC", "description": "d", "startMenu": true, "desktop": true},
		"rigger": {"version": "9.9.9", "sha256": "0000000000000000000000000000000000000000000000000000000000000000"}
	}`
	if err := os.WriteFile(layout.ManifestPath(root), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	riggerPath := filepath.Join(root, "rigger.exe")
	original := []byte("old rigger.exe bytes")
	if err := os.WriteFile(riggerPath, original, 0o755); err != nil {
		t.Fatal(err)
	}

	appValues := winreg.AppValues{InstallDir: root}
	if err := applyUpdate(appValues, riggerPath); err == nil {
		t.Fatal("expected an error for a checksum mismatch")
	}

	got, err := os.ReadFile(riggerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("expected rigger.exe to be left untouched after a failed update")
	}
}

func TestApplyUpdateRejectsMissingChecksum(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "9.9.9", "")

	riggerPath := filepath.Join(root, "rigger.exe")
	if err := os.WriteFile(riggerPath, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}

	appValues := winreg.AppValues{InstallDir: root}
	if err := applyUpdate(appValues, riggerPath); err == nil {
		t.Fatal("expected an error when the manifest declares a version with no checksum")
	}
}
