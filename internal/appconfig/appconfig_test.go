package appconfig

import (
	"strings"
	"testing"

	"github.com/sanjaynagpal/stage/internal/manifest"
)

func validConfig() *AppConfig {
	return &AppConfig{
		AppID:          "ABC",
		AppName:        "ABC",
		Publisher:      "Acme Corp",
		ProtocolScheme: "acme-abc",
		IconPath:       "app.ico",
		LicensePath:    "LICENSE.rtf",
		Environments: map[manifest.Environment]EnvironmentConfig{
			manifest.EnvProd: {
				Zones: map[manifest.NetworkZone]ZoneConfig{
					"Internet": {
						ManifestPath:      "manifests/prod-internet.json",
						ManifestServerURL: "https://example.com/abc/prod/internet/manifest.json",
					},
				},
			},
		},
		InitialJRE: JREBundleSpec{
			Version:     "21.0.2+13",
			ArchivePath: "jre/21.0.2+13-win-x64.zip",
			SHA256:      strings.Repeat("a", 64),
		},
		OutputName: "ABCSetup.exe",
	}
}

func TestValidateAcceptsWellFormedConfig(t *testing.T) {
	if err := validConfig().Validate(); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidateRejectsMissingRequiredFields(t *testing.T) {
	c := &AppConfig{}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty config")
	}
	for _, want := range []string{"appId", "appName", "publisher", "protocolScheme", "iconPath", "licensePath", "environments", "initialJre.version", "initialJre.archivePath", "initialJre.sha256", "outputName"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected validation error to mention %q, got: %v", want, err)
		}
	}
}

func TestValidateRejectsBadProtocolScheme(t *testing.T) {
	c := validConfig()
	c.ProtocolScheme = "1bad"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for protocolScheme starting with a digit")
	}
	c2 := validConfig()
	c2.ProtocolScheme = "has space"
	if err := c2.Validate(); err == nil {
		t.Fatal("expected error for protocolScheme containing a space")
	}
}

func TestValidateRejectsOutputNameWithoutExe(t *testing.T) {
	c := validConfig()
	c.OutputName = "ABCSetup"
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for outputName missing .exe suffix")
	}
}

func TestValidateRejectsBadFileAssociation(t *testing.T) {
	c := validConfig()
	c.FileAssociations = []FileAssocSpec{{Extension: "abc", Description: "ABC Document"}}
	if err := c.Validate(); err == nil {
		t.Fatal("expected error for extension missing leading dot")
	}
}

func TestValidateAcceptsFileAssociationWithDot(t *testing.T) {
	c := validConfig()
	c.FileAssociations = []FileAssocSpec{{Extension: ".abc", Description: "ABC Document"}}
	if err := c.Validate(); err != nil {
		t.Fatalf("expected valid config with dotted extension, got: %v", err)
	}
}

func TestParseRoundTrip(t *testing.T) {
	data := []byte(`{
		"appId": "ABC",
		"appName": "ABC",
		"publisher": "Acme Corp",
		"protocolScheme": "acme-abc",
		"iconPath": "app.ico",
		"licensePath": "LICENSE.rtf",
		"environments": {"PROD": {"zones": {"Internet": {"manifestPath": "manifests/prod.json", "manifestServerUrl": "https://example.com/abc/prod/manifest.json"}}}},
		"initialJre": {"version": "21.0.2+13", "archivePath": "jre/21.0.2+13-win-x64.zip", "sha256": "` + strings.Repeat("a", 64) + `"},
		"outputName": "ABCSetup.exe"
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if c.AppID != "ABC" || c.Environments[manifest.EnvProd].Zones["Internet"].ManifestPath != "manifests/prod.json" {
		t.Fatalf("unexpected parsed config: %+v", c)
	}
}

func TestValidateRejectsEnvironmentWithNoZones(t *testing.T) {
	c := validConfig()
	c.Environments[manifest.EnvProd] = EnvironmentConfig{}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "zones must declare at least one network zone") {
		t.Fatalf("expected error about missing zones, got: %v", err)
	}
}

func TestValidateRejectsBadZoneKey(t *testing.T) {
	c := validConfig()
	c.Environments[manifest.EnvProd] = EnvironmentConfig{
		Zones: map[manifest.NetworkZone]ZoneConfig{
			"1bad": {ManifestPath: "m.json", ManifestServerURL: "https://example.com/m.json"},
		},
	}
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), `zones key "1bad"`) {
		t.Fatalf("expected error about bad zone key, got: %v", err)
	}
}

func TestParseRoundTripsFetchAtInstall(t *testing.T) {
	data := []byte(`{
		"appId": "ABC",
		"appName": "ABC",
		"publisher": "Acme Corp",
		"protocolScheme": "acme-abc",
		"iconPath": "app.ico",
		"licensePath": "LICENSE.rtf",
		"environments": {"PROD": {"zones": {"Radianz": {"manifestPath": "manifests/prod.json", "manifestServerUrl": "https://example.com/abc/prod/manifest.json", "fetchAtInstall": true}}}},
		"initialJre": {"version": "21.0.2+13", "archivePath": "jre/21.0.2+13-win-x64.zip", "sha256": "` + strings.Repeat("a", 64) + `"},
		"outputName": "ABCSetup.exe"
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if !c.Environments[manifest.EnvProd].Zones["Radianz"].FetchAtInstall {
		t.Fatalf("expected fetchAtInstall to round-trip as true, got: %+v", c.Environments[manifest.EnvProd].Zones["Radianz"])
	}
}

func TestParseRoundTripsAuthURL(t *testing.T) {
	data := []byte(`{
		"appId": "ABC",
		"appName": "ABC",
		"publisher": "Acme Corp",
		"protocolScheme": "acme-abc",
		"iconPath": "app.ico",
		"licensePath": "LICENSE.rtf",
		"environments": {"PROD": {"zones": {"Radianz": {"manifestPath": "manifests/prod.json", "manifestServerUrl": "https://example.com/abc/prod/manifest.json", "authUrl": "https://example.com/abc/authentication.html"}}}},
		"initialJre": {"version": "21.0.2+13", "archivePath": "jre/21.0.2+13-win-x64.zip", "sha256": "` + strings.Repeat("a", 64) + `"},
		"outputName": "ABCSetup.exe"
	}`)
	c, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	want := "https://example.com/abc/authentication.html"
	if got := c.Environments[manifest.EnvProd].Zones["Radianz"].AuthURL; got != want {
		t.Fatalf("AuthURL = %q, want %q", got, want)
	}
}

func TestAuthURLDefaultsEmpty(t *testing.T) {
	c := validConfig()
	if c.Environments[manifest.EnvProd].Zones["Internet"].AuthURL != "" {
		t.Fatal("expected AuthURL to default to empty when omitted")
	}
}

func TestFetchAtInstallDefaultsFalse(t *testing.T) {
	c := validConfig()
	if c.Environments[manifest.EnvProd].Zones["Internet"].FetchAtInstall {
		t.Fatal("expected FetchAtInstall to default to false when omitted")
	}
}

func TestValidateRejectsMissingZoneManifestFields(t *testing.T) {
	c := validConfig()
	c.Environments[manifest.EnvProd] = EnvironmentConfig{
		Zones: map[manifest.NetworkZone]ZoneConfig{
			"Internet": {},
		},
	}
	err := c.Validate()
	if err == nil {
		t.Fatal("expected error for missing zone manifest fields")
	}
	for _, want := range []string{"zones[Internet].manifestPath", "zones[Internet].manifestServerUrl"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected validation error to mention %q, got: %v", want, err)
		}
	}
}
