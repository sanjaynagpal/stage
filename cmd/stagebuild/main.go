// Command stagebuild is the per-app build tool: it consumes an app's
// appconfig.AppConfig and produces one self-contained installer exe for a
// chosen (Environment, NetworkZone) pair, analogous to compiling an Inno
// Setup script (docs/REQUIREMENTS.md §9) — it populates
// cmd/installer/payload/ with that app's files, then really compiles
// cmd/installer fresh so the payload is embedded via go:embed.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/sanjaynagpal/stage/internal/appconfig"
	"github.com/sanjaynagpal/stage/internal/manifest"
	"github.com/sanjaynagpal/stage/internal/payload"
	"github.com/sanjaynagpal/stage/internal/riggerupdate"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "stagebuild: error:", err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "", "path to the app's appconfig.json")
	envFlag := flag.String("env", "", "environment to build for (DEV|TEST|PROD)")
	zoneFlag := flag.String("zone", "", "network zone to build for")
	flag.Parse()

	if *configPath == "" || *envFlag == "" || *zoneFlag == "" {
		return fmt.Errorf("usage: stagebuild -config <path/to/appconfig.json> -env <DEV|TEST|PROD> -zone <name>")
	}

	cfg, err := appconfig.Load(*configPath)
	if err != nil {
		return err
	}
	configDir := filepath.Dir(*configPath)

	env := manifest.Environment(*envFlag)
	envCfg, ok := cfg.Environments[env]
	if !ok {
		return fmt.Errorf("stagebuild: environment %q is not declared in %s", env, *configPath)
	}
	zone := manifest.NetworkZone(*zoneFlag)
	zoneCfg, ok := envCfg.Zones[zone]
	if !ok {
		return fmt.Errorf("stagebuild: zone %q is not declared for environment %q in %s", zone, env, *configPath)
	}

	manifestPath := resolvePath(configDir, zoneCfg.ManifestPath)
	m, err := manifest.Load(manifestPath)
	if err != nil {
		return fmt.Errorf("stagebuild: loading manifest: %w", err)
	}
	if m.ManifestServerURL != zoneCfg.ManifestServerURL {
		return fmt.Errorf("stagebuild: manifest %s declares manifestServerUrl %q but appconfig's zone declares %q",
			manifestPath, m.ManifestServerURL, zoneCfg.ManifestServerURL)
	}

	repoRoot, err := findModuleRoot()
	if err != nil {
		return err
	}
	payloadDir := filepath.Join(repoRoot, "cmd", "installer", "payload")
	lockPath := filepath.Join(payloadDir, ".stagebuild.lock")

	lock, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("stagebuild: another build appears to be in progress (%s already exists): %w", lockPath, err)
	}
	lock.Close()
	defer os.Remove(lockPath)

	if err := wipePayload(payloadDir); err != nil {
		return err
	}

	if err := populateAndBuild(repoRoot, payloadDir, configDir, cfg, env, zone, manifestPath, zoneCfg); err != nil {
		fmt.Fprintf(os.Stderr, "stagebuild: build failed, leaving %s populated for inspection\n", payloadDir)
		return err
	}

	if err := wipePayload(payloadDir); err != nil {
		fmt.Fprintf(os.Stderr, "stagebuild: warning: failed to clean up %s after a successful build: %v\n", payloadDir, err)
	}

	fmt.Printf("stagebuild: built %s\n", filepath.Join(repoRoot, "dist", cfg.OutputName))
	return nil
}

func populateAndBuild(repoRoot, payloadDir, configDir string, cfg *appconfig.AppConfig, env manifest.Environment, zone manifest.NetworkZone, manifestPath string, zoneCfg appconfig.ZoneConfig) error {
	goBuild := func(outPath, pkg string) error {
		cmd := exec.Command("go", "build", "-o", outPath, pkg)
		cmd.Dir = repoRoot
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("go build -o %s %s: %w\n%s", outPath, pkg, err, out)
		}
		return nil
	}

	riggerExePath := filepath.Join(payloadDir, payload.RiggerExeName)
	if err := goBuild(riggerExePath, "./cmd/rigger"); err != nil {
		return err
	}
	if err := goBuild(filepath.Join(payloadDir, payload.UninsExeName), "./cmd/uninstaller"); err != nil {
		return err
	}
	if err := publishRiggerUpdateArtifact(repoRoot, riggerExePath); err != nil {
		return err
	}

	if err := copyFile(resolvePath(configDir, cfg.InitialJRE.ArchivePath), filepath.Join(payloadDir, payload.JREArchiveName)); err != nil {
		return fmt.Errorf("stagebuild: copy JRE archive: %w", err)
	}
	if !zoneCfg.FetchAtInstall {
		if err := copyFile(manifestPath, filepath.Join(payloadDir, payload.ManifestName)); err != nil {
			return fmt.Errorf("stagebuild: copy manifest: %w", err)
		}
	}

	cfgData, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("stagebuild: marshal appconfig: %w", err)
	}
	if err := os.WriteFile(filepath.Join(payloadDir, payload.AppConfigName), cfgData, 0o644); err != nil {
		return fmt.Errorf("stagebuild: write %s: %w", payload.AppConfigName, err)
	}

	if err := copyFile(resolvePath(configDir, cfg.IconPath), filepath.Join(payloadDir, payload.IconName)); err != nil {
		return fmt.Errorf("stagebuild: copy icon: %w", err)
	}
	if err := copyFile(resolvePath(configDir, cfg.LicensePath), filepath.Join(payloadDir, payload.LicenseName)); err != nil {
		return fmt.Errorf("stagebuild: copy license: %w", err)
	}

	buildMeta := payload.BuildMeta{
		Environment:       env,
		NetworkZone:       zone,
		ManifestServerURL: zoneCfg.ManifestServerURL,
		ManifestBundled:   !zoneCfg.FetchAtInstall,
		AuthURL:           zoneCfg.AuthURL,
	}
	if err := payload.SaveBuildMeta(filepath.Join(payloadDir, payload.BuildMetaName), buildMeta); err != nil {
		return fmt.Errorf("stagebuild: write build metadata: %w", err)
	}

	if cfg.Signing != nil {
		fmt.Fprintln(os.Stderr, "stagebuild: warning: signing config is present but not implemented yet — the produced installer will be unsigned")
	}

	distDir := filepath.Join(repoRoot, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return fmt.Errorf("stagebuild: create %s: %w", distDir, err)
	}
	return goBuild(filepath.Join(distDir, cfg.OutputName), "./cmd/installer")
}

// publishRiggerUpdateArtifact copies the freshly-built rigger.exe to dist/
// under its own compiled-in version and prints the manifest field values
// an operator needs to declare to make Dynamic installs self-update to it
// (docs/REQUIREMENTS.md §25). Stage doesn't upload it anywhere itself —
// jars/JRE archives already work the same way, the operator's own
// deployment pipeline owns the manifest server.
func publishRiggerUpdateArtifact(repoRoot, riggerExePath string) error {
	distDir := filepath.Join(repoRoot, "dist")
	if err := os.MkdirAll(distDir, 0o755); err != nil {
		return fmt.Errorf("stagebuild: create %s: %w", distDir, err)
	}
	outPath := filepath.Join(distDir, fmt.Sprintf("rigger-%s-win-x64.exe", riggerupdate.Version))
	if err := copyFile(riggerExePath, outPath); err != nil {
		return fmt.Errorf("stagebuild: publish rigger update artifact: %w", err)
	}

	data, err := os.ReadFile(outPath)
	if err != nil {
		return fmt.Errorf("stagebuild: read %s: %w", outPath, err)
	}
	sum := sha256.Sum256(data)
	fmt.Printf("stagebuild: built rigger.exe v%s: %s\n", riggerupdate.Version, outPath)
	fmt.Printf("  To enable self-update for Dynamic installs, upload it to your manifest server at\n")
	fmt.Printf("  rigger/%s-win-x64.exe (matching Manifest.RiggerDownloadURL()'s convention) and add\n", riggerupdate.Version)
	fmt.Printf("  to your served manifest: \"rigger\": {\"version\": %q, \"sha256\": %q}\n", riggerupdate.Version, hex.EncodeToString(sum[:]))
	return nil
}

// wipePayload removes everything in payloadDir except the committed
// placeholder and the lock file currently held.
func wipePayload(payloadDir string) error {
	entries, err := os.ReadDir(payloadDir)
	if err != nil {
		return fmt.Errorf("stagebuild: list %s: %w", payloadDir, err)
	}
	for _, e := range entries {
		if e.Name() == "PLACEHOLDER.txt" || e.Name() == ".stagebuild.lock" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(payloadDir, e.Name())); err != nil {
			return fmt.Errorf("stagebuild: remove %s: %w", e.Name(), err)
		}
	}
	return nil
}

func resolvePath(baseDir, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(baseDir, p)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", dst, err)
	}
	return nil
}

// findModuleRoot locates this repo's root regardless of stagebuild's
// working directory, so it can be invoked from anywhere inside the repo.
func findModuleRoot() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("stagebuild: locate module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
