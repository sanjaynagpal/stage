// Command installer is Stage's generated, per-app installer
// (docs/REQUIREMENTS.md §16). stagebuild compiles it fresh for each app
// build with that app's payload embedded via go:embed (see internal/payload
// for the embedded file schema). Its UI defaults to a polished terminal
// wizard (internal/tui); -gui switches to a browser-based wizard
// (internal/wizard) instead — see docs/REQUIREMENTS.md §23 for why an
// embedded WebView2 control (the originally planned §11b approach) was
// abandoned in favor of these two.
package main

import (
	"archive/zip"
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/sanjaynagpal/stage/internal/appconfig"
	"github.com/sanjaynagpal/stage/internal/diskspace"
	"github.com/sanjaynagpal/stage/internal/elevate"
	"github.com/sanjaynagpal/stage/internal/fileassoc"
	"github.com/sanjaynagpal/stage/internal/jreprovision"
	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/manifest"
	"github.com/sanjaynagpal/stage/internal/payload"
	"github.com/sanjaynagpal/stage/internal/procscan"
	"github.com/sanjaynagpal/stage/internal/protocolhandler"
	"github.com/sanjaynagpal/stage/internal/proxydetect"
	"github.com/sanjaynagpal/stage/internal/shortcut"
	"github.com/sanjaynagpal/stage/internal/tui"
	"github.com/sanjaynagpal/stage/internal/winreg"
	"github.com/sanjaynagpal/stage/internal/wizard"
)

//go:embed all:payload
var payloadFS embed.FS

// installerUI is the presentation layer run() drives — internal/tui (the
// default) and internal/wizard (-gui) each implement it, and run()'s
// install logic doesn't care which one it's given.
type installerUI interface {
	Confirm(prompt string, defaultYes bool) bool
	RetryCancel(prompt string, check func() error) error
	ReadLine(prompt string) string
	// Notify posts a discrete, permanent milestone (e.g. "Writing registry
	// values..."). Use Progress instead for a rapidly repeating report of
	// the same ongoing operation (a percentage climbing), so repeated
	// calls update one status line rather than each leaving their own
	// permanent log entry.
	Notify(msg string)
	Progress(msg string)
}

func main() {
	gui := flag.Bool("gui", false, "use the browser-based wizard instead of the terminal UI")
	flag.Parse()

	cfg, err := loadEmbeddedAppConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "installer: error:", err)
		os.Exit(1)
	}

	var ui installerUI
	var closeUI func()
	if *gui {
		w, err := wizard.New(cfg.AppName)
		if err != nil {
			fmt.Fprintln(os.Stderr, "installer: error:", err)
			os.Exit(1)
		}
		ui, closeUI = w, w.Close
	} else {
		t := tui.New(cfg.AppName)
		ui, closeUI = t, t.Close
	}

	runErr := run(ui, cfg)
	if runErr != nil {
		ui.Notify(fmt.Sprintf("Error: %v", runErr))
	}
	closeUI()
	if runErr != nil {
		fmt.Fprintln(os.Stderr, "installer: error:", runErr)
		os.Exit(1)
	}
}

func run(ui installerUI, cfg *appconfig.AppConfig) error {
	meta, err := loadEmbeddedBuildMeta()
	if err != nil {
		return err
	}
	m, err := acquireManifest(ui, meta)
	if err != nil {
		return err
	}

	ui.Notify(fmt.Sprintf("%s Setup (version %s) — Publisher: %s", cfg.AppName, m.Version, cfg.Publisher))

	licenseText, err := payloadFS.ReadFile("payload/" + payload.LicenseName)
	if err != nil {
		return fmt.Errorf("installer: read embedded license: %w", err)
	}
	if !ui.Confirm(string(licenseText)+"\n\nDo you accept the license agreement?", false) {
		ui.Notify("Installation cancelled.")
		return nil
	}

	scope := layout.ScopePerUser
	if elevate.IsElevated() {
		scope = layout.ScopeAllUsers
	}
	root := layout.RootDir(scope, cfg.AppID)
	dataDir := layout.DataDir(cfg.AppID)

	requiredBytes, err := estimateRequiredBytes(meta.ManifestBundled)
	if err != nil {
		return err
	}
	if err := checkPrerequisites(ui, root, cfg.AppName, requiredBytes); err != nil {
		return err
	}

	packageMode := winreg.ModeStatic
	if scope == layout.ScopePerUser {
		if ui.Confirm("Allow "+cfg.AppName+" to update its runtime/launcher automatically in the background?", true) {
			packageMode = winreg.ModeDynamic
		}
	}

	proxyHost, proxyPort := resolveProxy(ui, m.ManifestServerURL)

	// §9b: Stage creates the data directory, not left for Rigger to lazily
	// create on first run.
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return fmt.Errorf("installer: create %s: %w", dataDir, err)
	}

	if err := extractPayload(ui, root, cfg, m, meta.ManifestBundled); err != nil {
		return err
	}

	ui.Notify(fmt.Sprintf("Writing registry values to `%s\\Software\\%s`...", hkeyName(scope), cfg.AppID))
	if err := writeRegistry(scope, cfg, m, meta, root, dataDir, packageMode, proxyHost, proxyPort, requiredBytes); err != nil {
		return err
	}

	riggerPath := layout.RiggerExePath(root)
	record := payload.InstallRecord{Scope: scope, ProtocolScheme: cfg.ProtocolScheme}

	ui.Notify(fmt.Sprintf("Registering protocol handler in `%s\\Software\\Classes\\%s`...", hkeyName(scope), cfg.ProtocolScheme))
	if err := protocolhandler.Register(scope, cfg.ProtocolScheme, cfg.AppName, riggerPath); err != nil {
		return fmt.Errorf("installer: register protocol handler: %w", err)
	}

	for _, fa := range cfg.FileAssociations {
		ui.Notify(fmt.Sprintf("Registering file association %s in `%s\\Software\\Classes\\%s`...", fa.Extension, hkeyName(scope), fa.Extension))
		iconPath := ""
		if fa.IconPath != "" {
			iconPath = layout.IconPath(root)
		}
		progID, err := fileassoc.Register(scope, cfg.AppID, fileassoc.Spec{
			Extension:   fa.Extension,
			Description: fa.Description,
			IconPath:    iconPath,
		}, riggerPath)
		if err != nil {
			return fmt.Errorf("installer: register file association %s: %w", fa.Extension, err)
		}
		record.FileAssociations = append(record.FileAssociations, payload.FileAssocEntry{Extension: fa.Extension, ProgID: progID})
	}

	// A zone built with AuthURL set (docs/REQUIREMENTS.md §27) starts with
	// browser-based authentication rather than launching the JVM directly —
	// shortcuts invoke rigger.exe's "start browser" mode, which opens the
	// auth page and exits; the auth server's redirect to this app's
	// registered protocol scheme is what actually launches the JVM, with a
	// token attached.
	shortcutArguments := ""
	if meta.AuthURL != "" {
		shortcutArguments = "start browser -url " + meta.AuthURL
	}

	if m.Shortcut.StartMenu {
		linkPath := filepath.Join(layout.StartMenuDir(scope, cfg.AppName), cfg.AppName+".lnk")
		ui.Notify("Creating Start Menu shortcut at `" + linkPath + "`...")
		if err := shortcut.Create(shortcut.Spec{
			Path: linkPath, TargetPath: riggerPath, Description: m.Shortcut.Description, IconPath: layout.IconPath(root), Arguments: shortcutArguments,
		}); err != nil {
			return fmt.Errorf("installer: create Start Menu shortcut: %w", err)
		}
		record.StartMenuShortcut = linkPath
	}
	if m.Shortcut.Desktop {
		linkPath := filepath.Join(layout.DesktopDir(scope), cfg.AppName+".lnk")
		ui.Notify("Creating Desktop shortcut at `" + linkPath + "`...")
		if err := shortcut.Create(shortcut.Spec{
			Path: linkPath, TargetPath: riggerPath, Description: m.Shortcut.Description, IconPath: layout.IconPath(root), Arguments: shortcutArguments,
		}); err != nil {
			return fmt.Errorf("installer: create Desktop shortcut: %w", err)
		}
		record.DesktopShortcut = linkPath
	}

	if err := payload.SaveInstallRecord(layout.InstallRecordPath(root), record); err != nil {
		return fmt.Errorf("installer: write install record: %w", err)
	}

	ui.Notify("Installation complete.")
	if ui.Confirm("Launch "+cfg.AppName+" now?", true) {
		cmd := exec.Command(riggerPath)
		if err := cmd.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "installer: warning: failed to launch %s: %v\n", riggerPath, err)
		} else {
			cmd.Process.Release()
		}
	}
	return nil
}

func loadEmbeddedAppConfig() (*appconfig.AppConfig, error) {
	data, err := payloadFS.ReadFile("payload/" + payload.AppConfigName)
	if err != nil {
		return nil, fmt.Errorf("installer: read embedded appconfig: %w", err)
	}
	return appconfig.Parse(data)
}

func loadEmbeddedManifest() (*manifest.Manifest, error) {
	data, err := payloadFS.ReadFile("payload/" + payload.ManifestName)
	if err != nil {
		return nil, fmt.Errorf("installer: read embedded manifest: %w", err)
	}
	return manifest.Parse(data)
}

// acquireManifest gets this install's manifest either from the embedded
// payload (the default) or, for a zone built with fetchAtInstall (§26), by
// fetching the live manifest from meta.ManifestServerURL — trading the
// bundled snapshot's staleness for a hard requirement that the server be
// reachable during install (no cached fallback exists yet at this point,
// unlike Rigger's own post-install refresh, docs/REQUIREMENTS.md §9b).
func acquireManifest(ui installerUI, meta payload.BuildMeta) (*manifest.Manifest, error) {
	if meta.ManifestBundled {
		return loadEmbeddedManifest()
	}

	ui.Notify(fmt.Sprintf("Checking network access to %s...", meta.ManifestServerURL))
	proxy, err := proxydetect.DetectForURL(meta.ManifestServerURL)
	if err != nil {
		proxy = proxydetect.Result{}
	}
	m, err := manifest.Fetch(meta.ManifestServerURL, proxydetect.Client(proxy, 15*time.Second))
	if err != nil {
		return nil, fmt.Errorf("this app requires network access to %s during install, and it could not be reached: %w", meta.ManifestServerURL, err)
	}
	if m.ManifestServerURL != meta.ManifestServerURL {
		return nil, fmt.Errorf("manifest fetched from %s declares a different manifestServerUrl (%q) — refusing to proceed", meta.ManifestServerURL, m.ManifestServerURL)
	}
	return m, nil
}

func loadEmbeddedBuildMeta() (payload.BuildMeta, error) {
	data, err := payloadFS.ReadFile("payload/" + payload.BuildMetaName)
	if err != nil {
		return payload.BuildMeta{}, fmt.Errorf("installer: read embedded build metadata: %w", err)
	}
	return payload.ParseBuildMeta(data)
}

// estimateRequiredBytes sums the embedded files' sizes (the JRE archive's
// *uncompressed* entries, read directly out of the embedded zip bytes) plus
// a 20% safety margin, for the disk-space prerequisite check. bundled
// reports whether payload/manifest.json exists to be stat'd — a
// fetch-at-install build (§26) has no such file.
func estimateRequiredBytes(bundled bool) (uint64, error) {
	var total uint64
	names := []string{payload.RiggerExeName, payload.UninsExeName, payload.IconName}
	if bundled {
		names = append(names, payload.ManifestName)
	}
	for _, name := range names {
		info, err := fs.Stat(payloadFS, "payload/"+name)
		if err != nil {
			return 0, fmt.Errorf("installer: stat embedded %s: %w", name, err)
		}
		total += uint64(info.Size())
	}

	jreData, err := payloadFS.ReadFile("payload/" + payload.JREArchiveName)
	if err != nil {
		return 0, fmt.Errorf("installer: read embedded JRE archive: %w", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(jreData), int64(len(jreData)))
	if err != nil {
		return 0, fmt.Errorf("installer: read embedded JRE archive as zip: %w", err)
	}
	for _, f := range zr.File {
		total += f.UncompressedSize64
	}

	return total + total/5, nil
}

func checkPrerequisites(ui installerUI, root, appName string, requiredBytes uint64) error {
	if _, err := os.Stat(root); err == nil {
		// An existing install root: this is an upgrade-in-place, so make
		// sure nothing is still running under it before touching anything
		// (docs/REQUIREMENTS.md §16 step 1).
		err := ui.RetryCancel(appName+" appears to be running — please close it", func() error {
			running, err := procscan.RunningUnder(root)
			if err != nil {
				return err
			}
			if len(running) > 0 {
				return fmt.Errorf("%d process(es) still running under %s", len(running), root)
			}
			return nil
		})
		if err != nil {
			return fmt.Errorf("installer: %w", err)
		}
	}

	ok, err := diskspace.HasSpaceFor(root, requiredBytes)
	if err != nil {
		return fmt.Errorf("installer: checking free disk space: %w", err)
	}
	if !ok {
		return fmt.Errorf("installer: not enough free disk space (need at least %d MB)", requiredBytes/(1024*1024)+1)
	}
	return nil
}

func resolveProxy(ui installerUI, targetURL string) (host, port string) {
	ui.Notify("Proxy configuration")
	result, err := proxydetect.DetectForURL(targetURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "installer: warning: proxy detection failed (%v); assuming a direct connection\n", err)
		result = proxydetect.Result{}
	}
	defaultDesc := "direct connection — no proxy required"
	if result.Empty() {
		ui.Notify("Detected proxy: none — the default is a direct connection; no proxy is required.")
	} else {
		defaultDesc = fmt.Sprintf("proxy %s:%s", result.Host, result.Port)
		ui.Notify(fmt.Sprintf("Detected proxy: `%s:%s`", result.Host, result.Port))
	}

	override := ui.ReadLine(fmt.Sprintf("Proxy configuration — press Enter to accept the detected default (%s), enter a host:port to use a specific proxy, or type 'none' for a direct connection: ", defaultDesc))
	switch {
	case override == "":
		return result.Host, result.Port
	case strings.EqualFold(override, "none"):
		return "", ""
	default:
		host, port, err := net.SplitHostPort(override)
		if err != nil {
			fmt.Fprintf(os.Stderr, "installer: warning: could not parse %q as host:port (%v); using the detected value instead\n", override, err)
			return result.Host, result.Port
		}
		return host, port
	}
}

func extractPayload(ui installerUI, root string, cfg *appconfig.AppConfig, m *manifest.Manifest, manifestBundled bool) error {
	ui.Notify("Extracting application files to `" + root + "`...")
	if err := writeEmbeddedFile(payload.RiggerExeName, layout.RiggerExePath(root)); err != nil {
		return err
	}
	if err := writeEmbeddedFile(payload.UninsExeName, layout.UninstallerExePath(root)); err != nil {
		return err
	}
	if err := writeEmbeddedFile(payload.IconName, layout.IconPath(root)); err != nil {
		return err
	}
	// A FetchAtInstall zone (docs/REQUIREMENTS.md §26) has no
	// payload/manifest.json to copy — write the manifest already fetched
	// by acquireManifest instead, so Rigger still has an initial on-disk
	// cache to fall back to on a later launch with no network (§9b).
	if manifestBundled {
		if err := writeEmbeddedFile(payload.ManifestName, layout.ManifestPath(root)); err != nil {
			return err
		}
	} else {
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return fmt.Errorf("installer: marshal fetched manifest: %w", err)
		}
		if err := os.WriteFile(layout.ManifestPath(root), data, 0o644); err != nil {
			return fmt.Errorf("installer: write %s: %w", layout.ManifestPath(root), err)
		}
	}

	jreData, err := payloadFS.ReadFile("payload/" + payload.JREArchiveName)
	if err != nil {
		return fmt.Errorf("installer: read embedded JRE archive: %w", err)
	}
	tmp, err := os.CreateTemp("", "stage-jre-*.zip")
	if err != nil {
		return fmt.Errorf("installer: create temp file for JRE archive: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(jreData); err != nil {
		tmp.Close()
		return fmt.Errorf("installer: write temp JRE archive: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("installer: finalize temp JRE archive: %w", err)
	}

	jreDir := layout.JREVersionDir(root, cfg.InitialJRE.Version)
	ui.Notify(fmt.Sprintf("Installing Java runtime %s to `%s`...", cfg.InitialJRE.Version, jreDir))
	if err := jreprovision.ProvisionLocal(root, tmpPath, cfg.InitialJRE.SHA256, jreDir, jreExtractNotifier(ui, cfg.InitialJRE.Version)); err != nil {
		return fmt.Errorf("installer: provision JRE: %w", err)
	}
	return nil
}

// jreExtractNotifier builds a jreprovision.ProgressFunc that posts unzip
// percentage as it goes via Progress (updating one status line) rather than
// Notify (which would leave every tick as its own permanent log entry) —
// deduped to one call per whole percentage point, since the underlying
// archiveutil throttle is time-based and can still fire more than once for
// the same rounded percent.
func jreExtractNotifier(ui installerUI, version string) jreprovision.ProgressFunc {
	lastPct := -1
	return func(done, total int64) {
		if total <= 0 {
			return
		}
		pct := int(float64(done) / float64(total) * 100)
		if pct == lastPct {
			return
		}
		lastPct = pct
		ui.Progress(fmt.Sprintf("Installing Java runtime %s... %d%%", version, pct))
	}
}

func writeEmbeddedFile(name, destPath string) error {
	data, err := payloadFS.ReadFile("payload/" + name)
	if err != nil {
		return fmt.Errorf("installer: read embedded %s: %w", name, err)
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("installer: create %s: %w", filepath.Dir(destPath), err)
	}
	if err := os.WriteFile(destPath, data, 0o644); err != nil {
		return fmt.Errorf("installer: write %s: %w", destPath, err)
	}
	return nil
}

// hkeyName names the registry hive a scope writes to, for display in
// Notify messages only — internal/winreg, internal/protocolhandler, and
// internal/fileassoc each independently derive the same root key from
// scope for the actual writes; this doesn't need to (and can't cleanly,
// without exporting internals three packages already keep private) share
// that logic, it just needs to describe it to the operator.
func hkeyName(scope layout.Scope) string {
	if scope == layout.ScopeAllUsers {
		return "HKEY_LOCAL_MACHINE"
	}
	return "HKEY_CURRENT_USER"
}

func writeRegistry(scope layout.Scope, cfg *appconfig.AppConfig, m *manifest.Manifest, meta payload.BuildMeta,
	root, dataDir string, packageMode winreg.PackageMode, proxyHost, proxyPort string, estimatedSizeBytes uint64) error {
	if err := winreg.WriteAppValues(winreg.AppValues{
		InstallScope:      scope,
		PackageMode:       packageMode,
		AppID:             cfg.AppID,
		InstallDir:        root,
		DataDir:           dataDir,
		ManifestServerURL: m.ManifestServerURL,
		ProtocolScheme:    cfg.ProtocolScheme,
		DisplayVersion:    m.Version,
		NetworkZone:       meta.NetworkZone,
		ProxyHost:         proxyHost,
		ProxyPort:         proxyPort,
	}); err != nil {
		return fmt.Errorf("installer: write registry values: %w", err)
	}

	if err := winreg.WriteUninstallValues(winreg.UninstallValues{
		AppID:           cfg.AppID,
		InstallScope:    scope,
		DisplayName:     cfg.AppName,
		DisplayVersion:  m.Version,
		Publisher:       cfg.Publisher,
		InstallLocation: root,
		UninstallString: layout.UninstallerExePath(root),
		DisplayIcon:     layout.IconPath(root),
		EstimatedSizeKB: uint32(estimatedSizeBytes / 1024),
	}); err != nil {
		return fmt.Errorf("installer: write uninstall registry values: %w", err)
	}
	return nil
}
