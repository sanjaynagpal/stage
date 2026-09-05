// Package appconfig defines stagebuild's input schema: the per-app
// configuration a developer supplies to produce a distributable installer
// (docs/REQUIREMENTS.md §14, "Finalized App Build-Config Schema").
package appconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/sanjaynagpal/stage/internal/manifest"
)

// AppConfig is stagebuild's input: everything needed to produce one
// environment's installer for an app.
type AppConfig struct {
	AppID            string                                     `json:"appId"`
	AppName          string                                     `json:"appName"`
	Publisher        string                                     `json:"publisher"`
	ProtocolScheme   string                                     `json:"protocolScheme"`
	IconPath         string                                     `json:"iconPath"`
	LicensePath      string                                     `json:"licensePath"`
	Environments     map[manifest.Environment]EnvironmentConfig `json:"environments"`
	InitialJRE       JREBundleSpec                              `json:"initialJre"`
	FileAssociations []FileAssocSpec                            `json:"fileAssociations,omitempty"`
	OutputName       string                                     `json:"outputName"`
	Signing          *SigningConfig                             `json:"signing,omitempty"`
}

// EnvironmentConfig declares, for one environment (DEV/TEST/PROD), the
// per-network-zone manifests available in it — every (Environment,
// NetworkZone) pair needs its own manifest server, since the server
// reachable from a zone can differ (docs/REQUIREMENTS.md §17-18).
type EnvironmentConfig struct {
	Zones map[manifest.NetworkZone]ZoneConfig `json:"zones"`
}

// ZoneConfig points to the initial manifest bundled for one (Environment,
// NetworkZone) pair, verbatim, plus the server URL it was fetched from
// (cross-checked against the manifest's own ManifestServerURL field).
type ZoneConfig struct {
	ManifestPath      string `json:"manifestPath"`
	ManifestServerURL string `json:"manifestServerUrl"`
}

// JREBundleSpec identifies the JRE archive physically extracted at install
// time (the "bundled" JRE, as opposed to one fetched later on demand).
type JREBundleSpec struct {
	Version     string `json:"version"`
	ArchivePath string `json:"archivePath"`
	SHA256      string `json:"sha256"`
}

// FileAssocSpec declares one optional file-type association. Omitted
// entirely (the default) means the installer registers none.
type FileAssocSpec struct {
	Extension   string `json:"extension"`
	Description string `json:"description"`
	IconPath    string `json:"iconPath,omitempty"`
}

// SigningConfig identifies the Authenticode certificate stagebuild should
// use to sign the produced installer.
type SigningConfig struct {
	CertThumbprint string `json:"certThumbprint,omitempty"`
	PfxPath        string `json:"pfxPath,omitempty"`
	TimestampURL   string `json:"timestampUrl,omitempty"`
}

// Load reads and validates an app config from a local JSON file.
func Load(path string) (*AppConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("appconfig: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes and validates an app config from JSON bytes.
func Parse(data []byte) (*AppConfig, error) {
	var c AppConfig
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("appconfig: parse: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

var protocolSchemePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9+.-]*$`)

// Validate checks required fields and basic formatting. It does not check
// that referenced file paths (IconPath, LicensePath, ArchivePath, ...)
// actually exist — that's stagebuild's job at build time, once it knows the
// config file's base directory to resolve relative paths against.
func (c *AppConfig) Validate() error {
	var problems []string

	if c.AppID == "" {
		problems = append(problems, "appId is required")
	} else if !manifest.ValidAppID(c.AppID) {
		problems = append(problems, fmt.Sprintf("appId %q must start with a letter and contain only letters, digits, '_' or '-'", c.AppID))
	}
	if c.AppName == "" {
		problems = append(problems, "appName is required")
	}
	if c.Publisher == "" {
		problems = append(problems, "publisher is required")
	}
	if c.ProtocolScheme == "" {
		problems = append(problems, "protocolScheme is required (the installer always registers a protocol handler)")
	} else if !protocolSchemePattern.MatchString(c.ProtocolScheme) {
		problems = append(problems, fmt.Sprintf("protocolScheme %q must start with a letter and contain only letters, digits, '+', '.', or '-'", c.ProtocolScheme))
	}
	if c.IconPath == "" {
		problems = append(problems, "iconPath is required")
	}
	if c.LicensePath == "" {
		problems = append(problems, "licensePath is required")
	}

	if len(c.Environments) == 0 {
		problems = append(problems, "environments must declare at least one of DEV, TEST, or PROD")
	}
	for env, ec := range c.Environments {
		switch env {
		case manifest.EnvDev, manifest.EnvTest, manifest.EnvProd:
		default:
			problems = append(problems, fmt.Sprintf("environments key %q must be DEV, TEST, or PROD", env))
		}
		if len(ec.Zones) == 0 {
			problems = append(problems, fmt.Sprintf("environments[%s].zones must declare at least one network zone", env))
		}
		for zone, zc := range ec.Zones {
			if !manifest.ValidNetworkZone(zone) {
				problems = append(problems, fmt.Sprintf("environments[%s].zones key %q must start with a letter and contain only letters, digits, '_' or '-'", env, zone))
			}
			if zc.ManifestPath == "" {
				problems = append(problems, fmt.Sprintf("environments[%s].zones[%s].manifestPath is required", env, zone))
			}
			if zc.ManifestServerURL == "" {
				problems = append(problems, fmt.Sprintf("environments[%s].zones[%s].manifestServerUrl is required", env, zone))
			}
		}
	}

	if c.InitialJRE.Version == "" {
		problems = append(problems, "initialJre.version is required")
	}
	if c.InitialJRE.ArchivePath == "" {
		problems = append(problems, "initialJre.archivePath is required")
	}
	if c.InitialJRE.SHA256 == "" {
		problems = append(problems, "initialJre.sha256 is required")
	}

	for i, fa := range c.FileAssociations {
		if !strings.HasPrefix(fa.Extension, ".") {
			problems = append(problems, fmt.Sprintf("fileAssociations[%d].extension %q must start with '.'", i, fa.Extension))
		}
		if fa.Description == "" {
			problems = append(problems, fmt.Sprintf("fileAssociations[%d].description is required", i))
		}
	}

	if c.OutputName == "" {
		problems = append(problems, "outputName is required")
	} else if !strings.HasSuffix(strings.ToLower(c.OutputName), ".exe") {
		problems = append(problems, fmt.Sprintf("outputName %q must end in .exe", c.OutputName))
	}

	if len(problems) > 0 {
		return fmt.Errorf("appconfig validation failed:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}
