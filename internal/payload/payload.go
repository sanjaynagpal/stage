// Package payload defines the schema shared between stagebuild (which
// writes cmd/installer's embedded payload directory) and the installer and
// uninstaller (which read it) — so the three never hardcode divergent
// filenames or duplicate the shapes of the small JSON artifacts they pass
// to each other (docs/REQUIREMENTS.md §16-18).
package payload

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/manifest"
)

// Filenames inside cmd/installer/payload/.
const (
	RiggerExeName  = "rigger.exe"
	UninsExeName   = "unins.exe"
	JREArchiveName = "jre-archive.zip"
	ManifestName   = "manifest.json"
	AppConfigName  = "appconfig.json"
	IconName       = "app.ico"
	LicenseName    = "license.txt"
	BuildMetaName  = "build.json"
)

// BuildMeta records which single (Environment, NetworkZone) pair this
// specific installer was built for — distinct from appconfig.json, which
// enumerates every zone the app could ever target (docs/REQUIREMENTS.md
// §17-18). Written by stagebuild, read by the installer to know its own
// registry values.
type BuildMeta struct {
	Environment manifest.Environment `json:"environment"`
	NetworkZone manifest.NetworkZone `json:"networkZone"`
	// ManifestServerURL is this build's zone's manifest server, written
	// regardless of ManifestBundled — when the manifest isn't bundled, this
	// is the bootstrap URL the installer fetches it from before it has any
	// other way to know where to look (docs/REQUIREMENTS.md §26).
	ManifestServerURL string `json:"manifestServerUrl"`
	// ManifestBundled reports whether payload/manifest.json is present. When
	// false, the installer fetches the manifest from ManifestServerURL at
	// install time instead of reading an embedded file.
	ManifestBundled bool `json:"manifestBundled"`
	// AuthURL, when non-empty, is this zone's browser-first auth page
	// (docs/REQUIREMENTS.md §27) — the installer bakes it into the
	// shortcuts it creates as `start browser -url <AuthURL>` arguments.
	AuthURL string `json:"authUrl,omitempty"`
}

// FileAssocEntry is one file-type association actually registered by the
// installer (extension + the ProgID it was registered under).
type FileAssocEntry struct {
	Extension string `json:"extension"`
	ProgID    string `json:"progId"`
}

// InstallRecord is written by the installer to layout.InstallRecordPath at
// the end of a successful install: a precise receipt of what was
// registered, so the uninstaller doesn't have to re-derive or guess it.
type InstallRecord struct {
	Scope             layout.Scope     `json:"scope"`
	ProtocolScheme    string           `json:"protocolScheme"`
	FileAssociations  []FileAssocEntry `json:"fileAssociations,omitempty"`
	StartMenuShortcut string           `json:"startMenuShortcut,omitempty"` // "" if not created
	DesktopShortcut   string           `json:"desktopShortcut,omitempty"`   // "" if not created
}

// SaveBuildMeta marshals m to path.
func SaveBuildMeta(path string, m BuildMeta) error {
	return saveJSON(path, m)
}

// LoadBuildMeta reads and parses a BuildMeta from path.
func LoadBuildMeta(path string) (BuildMeta, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return BuildMeta{}, fmt.Errorf("payload: read %s: %w", path, err)
	}
	return ParseBuildMeta(data)
}

// ParseBuildMeta decodes a BuildMeta from JSON bytes — e.g. bytes read
// from cmd/installer's embedded payload, without needing a temp file.
func ParseBuildMeta(data []byte) (BuildMeta, error) {
	var m BuildMeta
	if err := json.Unmarshal(data, &m); err != nil {
		return BuildMeta{}, fmt.Errorf("payload: parse build metadata: %w", err)
	}
	return m, nil
}

// SaveInstallRecord marshals r to path.
func SaveInstallRecord(path string, r InstallRecord) error {
	return saveJSON(path, r)
}

// LoadInstallRecord reads and parses an InstallRecord from path.
func LoadInstallRecord(path string) (InstallRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return InstallRecord{}, fmt.Errorf("payload: read %s: %w", path, err)
	}
	return ParseInstallRecord(data)
}

// ParseInstallRecord decodes an InstallRecord from JSON bytes.
func ParseInstallRecord(data []byte) (InstallRecord, error) {
	var r InstallRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return InstallRecord{}, fmt.Errorf("payload: parse install record: %w", err)
	}
	return r, nil
}

func saveJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("payload: marshal %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("payload: write %s: %w", path, err)
	}
	return nil
}
