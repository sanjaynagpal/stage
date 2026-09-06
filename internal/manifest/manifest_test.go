package manifest

import (
	"strings"
	"testing"
)

func validManifest() *Manifest {
	return &Manifest{
		AppID:             "ABC",
		AppName:           "ABC",
		Version:           "1.5.0",
		Environment:       EnvProd,
		ManifestServerURL: "https://example.com/abc/prod/manifest.json",
		ArtifactSHA256:    strings.Repeat("b", 64),
		Runtime: RuntimeSpec{
			JavaVersion: "21.0.2+13",
			Path:        "jre/21.0.2+13",
			SHA256:      strings.Repeat("a", 64),
		},
		Classpath: []string{"1.5.0/app.jar"},
		MainClass: "com.example.abc.Main",
		JVMOptions: []string{
			"-Xmx512m",
			"-Dabc.env=${environment}",
			"-Dabc.token=${uri.token}",
			"-Dabc.zone=${networkZone}",
			"-Dabc.proxyHost=${proxyHost}",
			"-Dabc.proxyPort=${proxyPort}",
		},
		Arguments:      []string{"--install-dir", "${installDir}", "--mode", "standard"},
		ProtocolParams: []string{"token", "param1"},
		Shortcut: ShortcutSpec{
			Name:        "ABC",
			Description: "Launches ABC",
			StartMenu:   true,
			Desktop:     true,
		},
	}
}

func TestValidateAcceptsWellFormedManifest(t *testing.T) {
	if err := validManifest().Validate(); err != nil {
		t.Fatalf("expected valid manifest, got error: %v", err)
	}
}

func TestValidateRejectsMissingRequiredFields(t *testing.T) {
	m := &Manifest{}
	err := m.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty manifest")
	}
	for _, want := range []string{"appId", "appName", "version", "environment", "manifestServerUrl", "runtime.javaVersion", "runtime.path", "mainClass", "classpath", "shortcut.name"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("expected validation error to mention %q, got: %v", want, err)
		}
	}
}

func TestValidateRejectsBadAppID(t *testing.T) {
	m := validManifest()
	m.AppID = "123-bad"
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for appId starting with a digit")
	}
	m2 := validManifest()
	m2.AppID = "has spaces"
	if err := m2.Validate(); err == nil {
		t.Fatal("expected error for appId containing spaces")
	}
}

func TestValidateRejectsAbsolutePaths(t *testing.T) {
	m := validManifest()
	m.Runtime.Path = `C:\jre\21`
	if err := m.Validate(); err == nil {
		t.Fatal("expected error for absolute runtime.path")
	}

	m2 := validManifest()
	m2.Classpath = []string{`C:\app.jar`}
	if err := m2.Validate(); err == nil {
		t.Fatal("expected error for absolute classpath entry")
	}
}

func TestValidateRejectsUndeclaredURIPlaceholder(t *testing.T) {
	m := validManifest()
	m.ProtocolParams = []string{"param1"} // "token" no longer declared
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "protocolParams") {
		t.Fatalf("expected error about undeclared protocolParams, got: %v", err)
	}
}

func TestValidateRejectsUnknownPlaceholder(t *testing.T) {
	m := validManifest()
	m.Arguments = append(m.Arguments, "--bogus=${notARealPlaceholder}")
	err := m.Validate()
	if err == nil || !strings.Contains(err.Error(), "unknown placeholder") {
		t.Fatalf("expected error about unknown placeholder, got: %v", err)
	}
}

func TestParseRoundTrip(t *testing.T) {
	data := []byte(`{
		"appId": "ABC",
		"appName": "ABC",
		"version": "1.5.0",
		"environment": "PROD",
		"manifestServerUrl": "https://example.com/abc/prod/manifest.json",
		"runtime": {"javaVersion": "21.0.2+13", "path": "jre/21.0.2+13"},
		"classpath": ["1.5.0/app.jar"],
		"mainClass": "com.example.abc.Main",
		"shortcut": {"name": "ABC", "description": "Launches ABC", "startMenu": true, "desktop": true}
	}`)
	m, err := Parse(data)
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if m.AppID != "ABC" || m.Runtime.JavaVersion != "21.0.2+13" {
		t.Fatalf("unexpected parsed manifest: %+v", m)
	}
}

func TestParseRejectsInvalidJSON(t *testing.T) {
	if _, err := Parse([]byte("not json")); err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestResolveSubstitutesBuiltins(t *testing.T) {
	m := validManifest()
	ctx := PlaceholderContext{
		InstallDir:  `C:\Users\me\AppData\Local\ABC`,
		DataDir:     `C:\Users\me\AppData\Roaming\ABC`,
		Environment: "PROD",
		NetworkZone: "Internet",
		ProxyHost:   "proxy.example.com",
		ProxyPort:   "8080",
		URIParams:   map[string]string{"token": "secret123"},
	}
	jvmOptions, arguments := m.Resolve(ctx)

	wantJVM := []string{
		"-Xmx512m",
		"-Dabc.env=PROD",
		"-Dabc.token=secret123",
		"-Dabc.zone=Internet",
		"-Dabc.proxyHost=proxy.example.com",
		"-Dabc.proxyPort=8080",
	}
	if !equalSlices(jvmOptions, wantJVM) {
		t.Fatalf("jvmOptions = %v, want %v", jvmOptions, wantJVM)
	}

	wantArgs := []string{"--install-dir", ctx.InstallDir, "--mode", "standard"}
	if !equalSlices(arguments, wantArgs) {
		t.Fatalf("arguments = %v, want %v", arguments, wantArgs)
	}
}

func TestResolveDropsTokenWhenURIParamMissing(t *testing.T) {
	m := validManifest()
	ctx := PlaceholderContext{
		InstallDir:  `C:\install`,
		Environment: "PROD",
		NetworkZone: "Internet",
		// No URIParams: a normal shortcut launch, not a protocol invocation.
		// No ProxyHost/ProxyPort: a direct-connection install.
	}
	jvmOptions, _ := m.Resolve(ctx)

	for _, opt := range jvmOptions {
		if strings.Contains(opt, "uri.token") || strings.Contains(opt, "abc.token") {
			t.Fatalf("expected the abc.token option to be dropped entirely, got jvmOptions = %v", jvmOptions)
		}
	}
	wantJVM := []string{"-Xmx512m", "-Dabc.env=PROD", "-Dabc.zone=Internet"}
	if !equalSlices(jvmOptions, wantJVM) {
		t.Fatalf("jvmOptions = %v, want %v", jvmOptions, wantJVM)
	}
}

func TestResolveDropsProxyPlaceholdersWhenNoProxyConfigured(t *testing.T) {
	m := validManifest()
	ctx := PlaceholderContext{
		InstallDir:  `C:\install`,
		Environment: "PROD",
		NetworkZone: "Internet",
		// ProxyHost/ProxyPort left empty: a direct-connection install.
	}
	jvmOptions, _ := m.Resolve(ctx)

	for _, opt := range jvmOptions {
		if strings.Contains(opt, "proxyHost") || strings.Contains(opt, "proxyPort") {
			t.Fatalf("expected the proxy options to be dropped entirely, got jvmOptions = %v", jvmOptions)
		}
	}
	wantJVM := []string{"-Xmx512m", "-Dabc.env=PROD", "-Dabc.zone=Internet"}
	if !equalSlices(jvmOptions, wantJVM) {
		t.Fatalf("jvmOptions = %v, want %v", jvmOptions, wantJVM)
	}
}

func TestDownloadURLConvention(t *testing.T) {
	m := validManifest()
	got := m.DownloadURL()
	want := "https://example.com/abc/prod/jre/21.0.2+13-win-x64.zip"
	if got != want {
		t.Fatalf("DownloadURL() = %q, want %q", got, want)
	}
}

func TestArtifactDownloadURLConvention(t *testing.T) {
	m := validManifest()
	got := m.ArtifactDownloadURL()
	want := "https://example.com/abc/prod/artifacts/1.5.0.zip"
	if got != want {
		t.Fatalf("ArtifactDownloadURL() = %q, want %q", got, want)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
