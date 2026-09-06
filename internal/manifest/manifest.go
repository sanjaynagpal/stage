// Package manifest defines the JSON manifest that tells Rigger how to launch
// a Java application: which JRE to use, the classpath and main class, JVM
// options and program arguments, and shortcut metadata. See
// docs/REQUIREMENTS.md §5 for the full design rationale.
package manifest

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Environment identifies which deployment environment a manifest targets.
// It is baked into the manifest at build time and never switched at runtime.
type Environment string

const (
	EnvDev  Environment = "DEV"
	EnvTest Environment = "TEST"
	EnvProd Environment = "PROD"
)

func (e Environment) valid() bool {
	switch e {
	case EnvDev, EnvTest, EnvProd:
		return true
	}
	return false
}

// Manifest is the full application manifest read by Rigger.
type Manifest struct {
	AppID             string      `json:"appId"`
	AppName           string      `json:"appName"`
	Version           string      `json:"version"`
	Environment       Environment `json:"environment"`
	ManifestServerURL string      `json:"manifestServerUrl"`
	// ArtifactSHA256 verifies the app-jars archive Rigger fetches on demand
	// when the version directory named by Version isn't present on disk yet
	// (internal/jarprovision) — the app-level analogue of Runtime.SHA256.
	// Required whenever on-demand jar delivery is possible (docs/REQUIREMENTS.md §9).
	ArtifactSHA256 string `json:"artifactSha256,omitempty"`
	Runtime           RuntimeSpec `json:"runtime"`
	Classpath         []string    `json:"classpath"`
	MainClass         string      `json:"mainClass"`
	JVMOptions        []string    `json:"jvmOptions,omitempty"`
	Arguments         []string    `json:"arguments,omitempty"`
	// ProtocolParams lists the query-param names from a protocol-handler
	// invocation URI that may be substituted via ${uri.<name>} placeholders
	// in JVMOptions/Arguments. Any other query param is ignored.
	ProtocolParams []string     `json:"protocolParams,omitempty"`
	Shortcut       ShortcutSpec `json:"shortcut"`
	// SupportEmail, if set, is the recipient for doctor mode's "Contact
	// Support" mailto: link (docs/REQUIREMENTS.md §20/§24). Lives in the
	// manifest rather than the registry so it can change without a
	// reinstall, like everything else ManifestServerURL polling refreshes.
	// Empty disables the button entirely — not a required field.
	SupportEmail string `json:"supportEmail,omitempty"`
}

// RuntimeSpec identifies the JRE this manifest requires.
type RuntimeSpec struct {
	// JavaVersion is the version string (e.g. "21.0.2+13"), also used as the
	// jre/<version> directory name and in the on-demand download convention.
	JavaVersion string `json:"javaVersion"`
	// Path is the JRE's location relative to the root app folder
	// (e.g. "jre/21.0.2+13"). Never absolute.
	Path string `json:"path"`
	// SHA256 verifies the JRE archive fetched on demand when Path doesn't
	// exist on disk. Required whenever on-demand provisioning is possible.
	SHA256 string `json:"sha256,omitempty"`
}

// ShortcutSpec carries the metadata needed to create Start Menu/Desktop
// shortcuts. Rigger itself never reads this; it's consumed by the installer.
type ShortcutSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	StartMenu   bool   `json:"startMenu"`
	Desktop     bool   `json:"desktop"`
}

var appIDPattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// ValidAppID reports whether id is safe to use as both a filesystem folder
// name and a registry key name, which Stage requires (docs/REQUIREMENTS.md §3).
func ValidAppID(id string) bool {
	return appIDPattern.MatchString(id)
}

// NetworkZone identifies a network the install runs on (e.g. a corporate
// intranet, the open internet, a private extranet). Unlike Environment,
// zone names name one deployment's specific network topology rather than a
// generic release stage, so this is deliberately not a closed enum — any
// app reusing Stage supplies its own zone names (docs/REQUIREMENTS.md §17).
type NetworkZone string

func (z NetworkZone) valid() bool {
	return z != "" && appIDPattern.MatchString(string(z))
}

// ValidNetworkZone reports whether z is safe to use as a JSON map key, a
// registry value, and a query parameter.
func ValidNetworkZone(z NetworkZone) bool {
	return z.valid()
}

var placeholderRe = regexp.MustCompile(`\$\{([a-zA-Z0-9_.]+)\}`)

// Load reads and parses a manifest from a local file path.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("manifest: read %s: %w", path, err)
	}
	return Parse(data)
}

// Parse decodes and validates a manifest from JSON bytes.
func Parse(data []byte) (*Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest: parse: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks that the manifest is structurally sound: required fields
// are present, paths are relative, and every ${...} placeholder used in
// JVMOptions/Arguments is either a known built-in or a declared protocol
// param.
func (m *Manifest) Validate() error {
	var problems []string

	if m.AppID == "" {
		problems = append(problems, "appId is required")
	} else if !ValidAppID(m.AppID) {
		problems = append(problems, fmt.Sprintf("appId %q must start with a letter and contain only letters, digits, '_' or '-'", m.AppID))
	}
	if m.AppName == "" {
		problems = append(problems, "appName is required")
	}
	if m.Version == "" {
		problems = append(problems, "version is required")
	}
	if !m.Environment.valid() {
		problems = append(problems, fmt.Sprintf("environment must be DEV, TEST, or PROD, got %q", m.Environment))
	}
	if m.ManifestServerURL == "" {
		problems = append(problems, "manifestServerUrl is required")
	}

	if m.Runtime.JavaVersion == "" {
		problems = append(problems, "runtime.javaVersion is required")
	}
	if m.Runtime.Path == "" {
		problems = append(problems, "runtime.path is required")
	} else if filepath.IsAbs(m.Runtime.Path) {
		problems = append(problems, fmt.Sprintf("runtime.path %q must be relative to the install root, not absolute", m.Runtime.Path))
	}

	if m.MainClass == "" {
		problems = append(problems, "mainClass is required")
	}
	if len(m.Classpath) == 0 {
		problems = append(problems, "classpath must have at least one entry")
	}
	for _, cp := range m.Classpath {
		if filepath.IsAbs(cp) {
			problems = append(problems, fmt.Sprintf("classpath entry %q must be relative to the install root, not absolute", cp))
		}
	}

	declared := make(map[string]bool, len(m.ProtocolParams))
	for _, p := range m.ProtocolParams {
		declared[p] = true
	}
	for _, s := range m.JVMOptions {
		if err := validatePlaceholders(s, declared); err != nil {
			problems = append(problems, err.Error())
		}
	}
	for _, s := range m.Arguments {
		if err := validatePlaceholders(s, declared); err != nil {
			problems = append(problems, err.Error())
		}
	}

	if m.Shortcut.Name == "" {
		problems = append(problems, "shortcut.name is required")
	}

	if len(problems) > 0 {
		return fmt.Errorf("manifest validation failed:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func validatePlaceholders(s string, declaredParams map[string]bool) error {
	for _, m := range placeholderRe.FindAllStringSubmatch(s, -1) {
		name := m[1]
		switch {
		case name == "installDir", name == "dataDir", name == "environment",
			name == "networkZone", name == "proxyHost", name == "proxyPort":
			continue
		case strings.HasPrefix(name, "uri."):
			param := strings.TrimPrefix(name, "uri.")
			if !declaredParams[param] {
				return fmt.Errorf("placeholder ${uri.%s} used in %q but %q is not listed in protocolParams", param, s, param)
			}
		default:
			return fmt.Errorf("unknown placeholder ${%s} in %q", name, s)
		}
	}
	return nil
}

// PlaceholderContext carries the runtime values available for substitution
// into a manifest's JVMOptions/Arguments.
type PlaceholderContext struct {
	InstallDir string
	DataDir    string
	// NetworkZone, ProxyHost, and ProxyPort are install-time-resolved facts
	// read from the registry (like InstallDir/DataDir), never from manifest
	// content (docs/REQUIREMENTS.md §17-18). ProxyHost/ProxyPort are empty
	// when the install has no configured proxy (a direct connection).
	NetworkZone string
	ProxyHost   string
	ProxyPort   string
	Environment string
	// URIParams holds only the query params actually present on a protocol
	// handler invocation URI (empty/nil for a normal shortcut launch).
	URIParams map[string]string
}

// Resolve substitutes ${...} placeholders in JVMOptions and Arguments using
// ctx. A token containing a ${uri.X} placeholder for which X was not
// supplied in ctx.URIParams is dropped entirely from the result (never
// substituted with an empty string), keeping the resulting JVM command line
// free of dangling/empty flags.
func (m *Manifest) Resolve(ctx PlaceholderContext) (jvmOptions, arguments []string) {
	return resolveList(m.JVMOptions, ctx), resolveList(m.Arguments, ctx)
}

func resolveList(tokens []string, ctx PlaceholderContext) []string {
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if resolved, ok := resolveToken(tok, ctx); ok {
			out = append(out, resolved)
		}
	}
	return out
}

func resolveToken(tok string, ctx PlaceholderContext) (string, bool) {
	drop := false
	result := placeholderRe.ReplaceAllStringFunc(tok, func(match string) string {
		name := match[2 : len(match)-1]
		switch {
		case name == "installDir":
			return ctx.InstallDir
		case name == "dataDir":
			return ctx.DataDir
		case name == "environment":
			return ctx.Environment
		case name == "networkZone":
			return ctx.NetworkZone
		case name == "proxyHost":
			if ctx.ProxyHost == "" {
				drop = true
				return ""
			}
			return ctx.ProxyHost
		case name == "proxyPort":
			if ctx.ProxyPort == "" {
				drop = true
				return ""
			}
			return ctx.ProxyPort
		case strings.HasPrefix(name, "uri."):
			param := strings.TrimPrefix(name, "uri.")
			if v, ok := ctx.URIParams[param]; ok {
				return v
			}
			drop = true
			return ""
		default:
			// Validate rejects unknown placeholders before a manifest is
			// ever used, so this should be unreachable in practice.
			drop = true
			return ""
		}
	})
	if drop {
		return "", false
	}
	return result, true
}

// DownloadURL derives the on-demand JRE archive location from
// ManifestServerURL by convention: the manifest's own final path segment is
// replaced with "jre/<javaVersion>-win-x64.zip" (docs/REQUIREMENTS.md §13).
func (m *Manifest) DownloadURL() string {
	base := m.ManifestServerURL[:strings.LastIndex(m.ManifestServerURL, "/")+1]
	return base + "jre/" + m.Runtime.JavaVersion + "-win-x64.zip"
}

// ArtifactDownloadURL derives the on-demand app-jars archive location from
// ManifestServerURL by the same convention as DownloadURL: the manifest's
// own final path segment is replaced with "artifacts/<version>.zip"
// (docs/REQUIREMENTS.md §9).
func (m *Manifest) ArtifactDownloadURL() string {
	base := m.ManifestServerURL[:strings.LastIndex(m.ManifestServerURL, "/")+1]
	return base + "artifacts/" + m.Version + ".zip"
}
