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
	"flag"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

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
	Notify(msg string)
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
	m, err := loadEmbeddedManifest()
	if err != nil {
		return err
	}
	meta, err := loadEmbeddedBuildMeta()
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

	requiredBytes, err := estimateRequiredBytes()
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

	ui.Notify("Installing...")
	if err := extractPayload(root, cfg); err != nil {
		return err
	}

	if err := writeRegistry(scope, cfg, m, meta, root, dataDir, packageMode, proxyHost, proxyPort, requiredBytes); err != nil {
		return err
	}

	riggerPath := layout.RiggerExePath(root)
	record := payload.InstallRecord{Scope: scope, ProtocolScheme: cfg.ProtocolScheme}

	if err := protocolhandler.Register(scope, cfg.ProtocolScheme, cfg.AppName, riggerPath); err != nil {
		return fmt.Errorf("installer: register protocol handler: %w", err)
	}

	for _, fa := range cfg.FileAssociations {
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

	if m.Shortcut.StartMenu {
		linkPath := filepath.Join(layout.StartMenuDir(scope, cfg.AppName), cfg.AppName+".lnk")
		if err := shortcut.Create(shortcut.Spec{
			Path: linkPath, TargetPath: riggerPath, Description: m.Shortcut.Description, IconPath: layout.IconPath(root),
		}); err != nil {
			return fmt.Errorf("installer: create Start Menu shortcut: %w", err)
		}
		record.StartMenuShortcut = linkPath
	}
	if m.Shortcut.Desktop {
		linkPath := filepath.Join(layout.DesktopDir(scope), cfg.AppName+".lnk")
		if err := shortcut.Create(shortcut.Spec{
			Path: linkPath, TargetPath: riggerPath, Description: m.Shortcut.Description, IconPath: layout.IconPath(root),
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

func loadEmbeddedBuildMeta() (payload.BuildMeta, error) {
	data, err := payloadFS.ReadFile("payload/" + payload.BuildMetaName)
	if err != nil {
		return payload.BuildMeta{}, fmt.Errorf("installer: read embedded build metadata: %w", err)
	}
	return payload.ParseBuildMeta(data)
}

// estimateRequiredBytes sums the embedded files' sizes (the JRE archive's
// *uncompressed* entries, read directly out of the embedded zip bytes) plus
// a 20% safety margin, for the disk-space prerequisite check.
func estimateRequiredBytes() (uint64, error) {
	var total uint64
	for _, name := range []string{payload.RiggerExeName, payload.UninsExeName, payload.ManifestName, payload.IconName} {
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
	result, err := proxydetect.DetectForURL(targetURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "installer: warning: proxy detection failed (%v); assuming a direct connection\n", err)
		result = proxydetect.Result{}
	}
	if result.Empty() {
		ui.Notify("Detected proxy: none (direct connection)")
	} else {
		ui.Notify(fmt.Sprintf("Detected proxy: %s:%s", result.Host, result.Port))
	}

	override := ui.ReadLine("Press Enter to accept, enter a host:port to override, or type 'none' for a direct connection: ")
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

func extractPayload(root string, cfg *appconfig.AppConfig) error {
	if err := writeEmbeddedFile(payload.RiggerExeName, layout.RiggerExePath(root)); err != nil {
		return err
	}
	if err := writeEmbeddedFile(payload.UninsExeName, layout.UninstallerExePath(root)); err != nil {
		return err
	}
	if err := writeEmbeddedFile(payload.IconName, layout.IconPath(root)); err != nil {
		return err
	}
	if err := writeEmbeddedFile(payload.ManifestName, layout.ManifestPath(root)); err != nil {
		return err
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
	if err := jreprovision.ProvisionLocal(root, tmpPath, cfg.InitialJRE.SHA256, jreDir); err != nil {
		return fmt.Errorf("installer: provision JRE: %w", err)
	}
	return nil
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
