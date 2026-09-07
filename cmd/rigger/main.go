// Command rigger is Stage's application launcher. It is a generic,
// prebuilt-once binary with zero per-app compiled state: it derives which
// app it belongs to from its own install folder name, reads that app's
// configuration from the registry, loads (and refreshes) the manifest, and
// execs the JVM per docs/REQUIREMENTS.md §6.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sanjaynagpal/stage/internal/applog"
	"github.com/sanjaynagpal/stage/internal/jarprovision"
	"github.com/sanjaynagpal/stage/internal/javainvoke"
	"github.com/sanjaynagpal/stage/internal/jreprovision"
	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/manifest"
	"github.com/sanjaynagpal/stage/internal/proxydetect"
	"github.com/sanjaynagpal/stage/internal/riggerupdate"
	"github.com/sanjaynagpal/stage/internal/uierror"
	"github.com/sanjaynagpal/stage/internal/uriparse"
	"github.com/sanjaynagpal/stage/internal/winreg"
)

// zoneURIParam is the query-param name the auth server is expected to echo
// the Network Zone back as on a protocol-handler invocation, used only for
// the consistency check below (docs/REQUIREMENTS.md §17-18).
const zoneURIParam = "networkZone"

func main() {
	// --doctor is checked directly against argv[1] (not the flag package)
	// so it doesn't interfere with the protocol-handler URI detection just
	// below, which also inspects argv[1] raw (docs/REQUIREMENTS.md §24).
	if len(os.Args) > 1 && os.Args[1] == "--doctor" {
		if err := runDoctor(); err != nil {
			uierror.Fatalf("Diagnostics Failed", "%v", err)
		}
		return
	}
	// --finish-self-update <appID> [original-args...] is phase two of a
	// self-update, run from the %TEMP% copy internal/riggerupdate.
	// BeginSelfUpdate launches (docs/REQUIREMENTS.md §25) — not the
	// installed rigger.exe itself, which can't overwrite its own running
	// executable file.
	if len(os.Args) > 1 && os.Args[1] == "--finish-self-update" {
		if len(os.Args) < 3 {
			uierror.Fatalf("Update Failed", "rigger: --finish-self-update requires <appID>")
			return
		}
		if err := riggerupdate.FinishSelfUpdate(os.Args[2], os.Args[3:]); err != nil {
			uierror.Fatalf("Update Failed", "%v", err)
		}
		return
	}
	if err := run(); err != nil {
		uierror.Fatalf("Application Launch Failed", "%v", err)
	}
}

func run() (err error) {
	var logger *applog.Logger
	defer func() {
		logger.LogFatal(err)
		logger.Close()
	}()

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("rigger: could not determine my own location: %w", err)
	}
	appID := layout.AppIDFromExePath(exePath)
	root := layout.RootDirFromExePath(exePath)

	appValues, err := winreg.ReadAppValues(appID)
	if err != nil {
		return fmt.Errorf("rigger: this installation of %s appears corrupt (%w) — please reinstall", appID, err)
	}

	if l, openErr := applog.Open(layout.RiggerLogPath(appValues.DataDir)); openErr != nil {
		fmt.Fprintf(os.Stderr, "rigger: warning: could not open log file: %v\n", openErr)
	} else {
		logger = l
	}

	invocation := "shortcut"
	var uriParams map[string]string
	if len(os.Args) > 1 && uriparse.LooksLikeInvocation(os.Args[1], appValues.ProtocolScheme) {
		invocation = "protocol handler"
		uriParams, err = uriparse.Parse(os.Args[1])
		if err != nil {
			return fmt.Errorf("rigger: could not parse invocation %q: %w", os.Args[1], err)
		}
	}
	logger.Info("rigger: launching %s v%s (invoked via %s)", appID, appValues.DisplayVersion, invocation)

	if returnedZone, ok := uriParams[zoneURIParam]; ok && returnedZone != string(appValues.NetworkZone) {
		logger.Warn("rigger: warning: auth response Network Zone %q does not match this install's configured zone %q", returnedZone, appValues.NetworkZone)
	}

	// A short-timeout client for the manifest poll (every launch — a slow
	// manifest server shouldn't stall an ordinary launch for long) and a
	// separate long-timeout client for the two on-demand archive fetches
	// below, which are rare but can legitimately take minutes.
	manifestClient := proxydetect.Client(proxydetect.Result{Host: appValues.ProxyHost, Port: appValues.ProxyPort}, 10*time.Second)
	fetchClient := proxydetect.Client(proxydetect.Result{Host: appValues.ProxyHost, Port: appValues.ProxyPort}, 5*time.Minute)

	m, err := loadManifest(root, appValues.ManifestServerURL, manifestClient, logger)
	if err != nil {
		return fmt.Errorf("rigger: could not load application manifest: %w", err)
	}

	if appValues.PackageMode == winreg.ModeDynamic && riggerupdate.NeedsUpdate(m.Rigger.Version) {
		if m.Rigger.SHA256 == "" {
			logger.Warn("rigger: warning: manifest declares rigger version %s but no checksum — skipping self-update", m.Rigger.Version)
		} else {
			logger.Info("rigger: new launcher version %s available (running %s) — updating", m.Rigger.Version, riggerupdate.Version)
			if err := riggerupdate.BeginSelfUpdate(appID, os.Args[1:]); err != nil {
				logger.Warn("rigger: warning: could not start self-update (%v); continuing with the current version", err)
			} else {
				logger.Info("rigger: handing off to the updated launcher")
				return nil
			}
		}
	}

	jreDir := filepath.Join(root, m.Runtime.Path)
	if _, err := os.Stat(jreDir); err != nil {
		if appValues.PackageMode != winreg.ModeDynamic {
			// Static installs (and an all-users install is always forced
			// Static) never self-update the runtime — fail clearly rather
			// than launching with a mismatched/older one, per §12-14.
			return fmt.Errorf("required Java runtime %s is not installed at %s — reinstall or upgrade %s to fix this", m.Runtime.JavaVersion, jreDir, appID)
		}
		logger.Info("rigger: required Java runtime %s not found at %s — fetching from %s", m.Runtime.JavaVersion, jreDir, m.DownloadURL())
		if err := jreprovision.Provision(fetchClient, root, m.DownloadURL(), m.Runtime.SHA256, jreDir); err != nil {
			return fmt.Errorf("rigger: could not provision required Java runtime %s: %w", m.Runtime.JavaVersion, err)
		}
		logger.Info("rigger: provisioned Java runtime %s", m.Runtime.JavaVersion)
	}
	logger.Info("rigger: using Java %s at %s", m.Runtime.JavaVersion, jreDir)

	versionDir := layout.VersionDir(root, m.Version)
	if _, err := os.Stat(versionDir); err != nil {
		artifactURL := m.ArtifactDownloadURL()
		logger.Info("rigger: application version %s not found locally at %s — fetching from %s", m.Version, versionDir, artifactURL)
		if err := jarprovision.Provision(fetchClient, root, artifactURL, m.ArtifactSHA256, versionDir); err != nil {
			return fmt.Errorf("rigger: could not fetch application version %s: %w", m.Version, err)
		}
		logger.Info("rigger: fetched and installed application version %s", m.Version)
	}

	classpath, err := javainvoke.ResolveClasspath(root, m.Classpath)
	if err != nil {
		return fmt.Errorf("rigger: %w", err)
	}

	jvmOptions, arguments := m.Resolve(manifest.PlaceholderContext{
		InstallDir:  root,
		DataDir:     appValues.DataDir,
		Environment: string(m.Environment),
		NetworkZone: string(appValues.NetworkZone),
		ProxyHost:   appValues.ProxyHost,
		ProxyPort:   appValues.ProxyPort,
		URIParams:   uriParams,
	})
	jvmOptions = append(jvmOptions, "-Dstage.appIcon="+layout.IconPath(root))

	logger.Info("rigger: launching %s %s (mainClass=%s)", m.AppName, m.Version, m.MainClass)
	return javainvoke.Launch(javainvoke.LaunchSpec{
		JREDir:     jreDir,
		Classpath:  classpath,
		MainClass:  m.MainClass,
		JVMOptions: jvmOptions,
		Arguments:  arguments,
	})
}

// loadManifest loads the locally cached manifest, then best-effort refreshes
// it from the server. A successful fetch replaces the cache and is used; a
// failed fetch (offline, server down, invalid content) silently falls back
// to the last-good cache, since connectivity is only required to check for
// updates, never to launch (docs/REQUIREMENTS.md §9b). Only when neither a
// fetch nor a valid cache is available does this return an error — that can
// only genuinely happen on a first run with no network.
func loadManifest(root, bootstrapURL string, client *http.Client, logger *applog.Logger) (*manifest.Manifest, error) {
	cachePath := layout.ManifestPath(root)
	cached, cacheErr := manifest.Load(cachePath)

	fetchURL := bootstrapURL
	if cached != nil && cached.ManifestServerURL != "" {
		fetchURL = cached.ManifestServerURL
	}

	fresh, fetchErr := fetchManifest(fetchURL, client)
	if fetchErr == nil {
		if err := writeManifestCache(cachePath, fresh); err != nil {
			logger.Warn("rigger: warning: failed to update local manifest cache: %v", err)
		}
		logger.Info("rigger: loaded manifest v%s (fresh fetch from %s)", fresh.Version, fetchURL)
		return fresh, nil
	}

	if cached != nil {
		logger.Info("rigger: loaded manifest v%s (using cached copy; fetch failed: %v)", cached.Version, fetchErr)
		return cached, nil
	}
	return nil, fmt.Errorf("fetching the manifest failed (%v) and no valid local cache exists (%v)", fetchErr, cacheErr)
}

func fetchManifest(url string, client *http.Client) (*manifest.Manifest, error) {
	if url == "" {
		return nil, fmt.Errorf("no manifest server URL available")
	}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP status %s", resp.Status)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return manifest.Parse(data)
}

func writeManifestCache(path string, m *manifest.Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
