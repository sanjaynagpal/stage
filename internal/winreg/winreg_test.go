package winreg

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/sanjaynagpal/stage/internal/layout"
)

// testAppID returns a disposable app id unique to this test run, so tests
// never collide with a real install or with each other.
func testAppID(t *testing.T) string {
	t.Helper()
	id := fmt.Sprintf("StageTest%d", rand.Int63())
	t.Cleanup(func() {
		_ = DeleteAppKey(layout.ScopePerUser, id)
		_ = DeleteUninstallValues(layout.ScopePerUser, id)
	})
	return id
}

func TestWriteAndReadAppValuesRoundTrip(t *testing.T) {
	appID := testAppID(t)
	want := AppValues{
		InstallScope:      layout.ScopePerUser,
		PackageMode:       ModeDynamic,
		AppID:             appID,
		InstallDir:        `C:\Users\me\AppData\Local\` + appID,
		DataDir:           `C:\Users\me\AppData\Roaming\` + appID,
		ManifestServerURL: "https://example.com/abc/prod/manifest.json",
		ProtocolScheme:    "acme-abc",
		DisplayVersion:    "1.5.0",
		NetworkZone:       "Internet",
		ProxyHost:         "proxy.example.com",
		ProxyPort:         "8080",
	}

	if err := WriteAppValues(want); err != nil {
		t.Fatalf("WriteAppValues: %v", err)
	}

	got, err := ReadAppValues(appID)
	if err != nil {
		t.Fatalf("ReadAppValues: %v", err)
	}
	if got != want {
		t.Fatalf("ReadAppValues = %+v, want %+v", got, want)
	}
}

func TestReadAppValuesFallsBackToHKLM(t *testing.T) {
	t.Skip("requires elevation to write HKLM; exercised manually/in CI running as admin")
}

func TestReadAppValuesMissingKeyReturnsError(t *testing.T) {
	if _, err := ReadAppValues("StageTestDoesNotExist"); err == nil {
		t.Fatal("expected error for a nonexistent app key")
	}
}

func TestDeleteAppKeyIsIdempotent(t *testing.T) {
	appID := testAppID(t)
	if err := DeleteAppKey(layout.ScopePerUser, appID); err != nil {
		t.Fatalf("DeleteAppKey on a nonexistent key should not error, got: %v", err)
	}
}

func TestWriteAndDeleteUninstallValues(t *testing.T) {
	appID := testAppID(t)
	v := UninstallValues{
		AppID:           appID,
		InstallScope:    layout.ScopePerUser,
		DisplayName:     "ABC",
		DisplayVersion:  "1.5.0",
		Publisher:       "Acme Corp",
		InstallLocation: `C:\Users\me\AppData\Local\` + appID,
		UninstallString: `C:\Users\me\AppData\Local\` + appID + `\unins.exe`,
		DisplayIcon:     `C:\Users\me\AppData\Local\` + appID + `\app.ico`,
		EstimatedSizeKB: 51200,
	}
	if err := WriteUninstallValues(v); err != nil {
		t.Fatalf("WriteUninstallValues: %v", err)
	}
	if err := DeleteUninstallValues(layout.ScopePerUser, appID); err != nil {
		t.Fatalf("DeleteUninstallValues: %v", err)
	}
	if err := DeleteUninstallValues(layout.ScopePerUser, appID); err != nil {
		t.Fatalf("DeleteUninstallValues should be idempotent, got: %v", err)
	}
}
