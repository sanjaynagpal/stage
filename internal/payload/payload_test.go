package payload

import (
	"path/filepath"
	"testing"

	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/manifest"
)

func TestBuildMetaRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "build.json")
	want := BuildMeta{Environment: manifest.EnvProd, NetworkZone: "Internet"}

	if err := SaveBuildMeta(path, want); err != nil {
		t.Fatalf("SaveBuildMeta: %v", err)
	}
	got, err := LoadBuildMeta(path)
	if err != nil {
		t.Fatalf("LoadBuildMeta: %v", err)
	}
	if got != want {
		t.Fatalf("LoadBuildMeta = %+v, want %+v", got, want)
	}
}

func TestInstallRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "install-record.json")
	want := InstallRecord{
		Scope:             layout.ScopePerUser,
		ProtocolScheme:    "acme-abc",
		FileAssociations:  []FileAssocEntry{{Extension: ".abc", ProgID: "ABC.abc"}},
		StartMenuShortcut: `C:\ProgramData\...\ABC.lnk`,
		DesktopShortcut:   "",
	}

	if err := SaveInstallRecord(path, want); err != nil {
		t.Fatalf("SaveInstallRecord: %v", err)
	}
	got, err := LoadInstallRecord(path)
	if err != nil {
		t.Fatalf("LoadInstallRecord: %v", err)
	}
	if got.Scope != want.Scope || got.ProtocolScheme != want.ProtocolScheme ||
		got.StartMenuShortcut != want.StartMenuShortcut || got.DesktopShortcut != want.DesktopShortcut {
		t.Fatalf("LoadInstallRecord = %+v, want %+v", got, want)
	}
	if len(got.FileAssociations) != 1 || got.FileAssociations[0] != want.FileAssociations[0] {
		t.Fatalf("FileAssociations = %+v, want %+v", got.FileAssociations, want.FileAssociations)
	}
}

func TestLoadBuildMetaMissingFileErrors(t *testing.T) {
	if _, err := LoadBuildMeta(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
