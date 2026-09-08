// Command rigger is Stage's application launcher. It is a generic,
// prebuilt-once binary with zero per-app compiled state: it derives which
// app it belongs to from its own install folder name, reads that app's
// configuration from the registry, loads (and refreshes) the manifest, and
// execs the JVM per docs/REQUIREMENTS.md §6.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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
	"github.com/sanjaynagpal/stage/internal/wizard"
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
	// "start browser -url <URL>" / "start manifest -url <URL>"
	// (docs/REQUIREMENTS.md §27) — the browser-first auth flow. A Desktop/
	// Start Menu shortcut for an AuthURL zone invokes "start browser" to
	// open the auth page and exit; "start manifest" is a manual/debug entry
	// point, not used by any shortcut — see runStartManifest's doc comment
	// for why trusting its -url as the fetch target is safe there but not
	// on the protocol-handler path below.
	if len(os.Args) > 2 && os.Args[1] == "start" {
		switch os.Args[2] {
		case "browser":
			url, err := parseURLFlag("start browser", os.Args[3:])
			if err != nil {
				uierror.Fatalf("Invalid Arguments", "%v", err)
				return
			}
			if err := openBrowser(url); err != nil {
				uierror.Fatalf("Could Not Open Browser", "%v", err)
			}
			return
		case "manifest":
			url, err := parseURLFlag("start manifest", os.Args[3:])
			if err != nil {
				uierror.Fatalf("Invalid Arguments", "%v", err)
				return
			}
			if err := runStartManifest(url); err != nil {
				uierror.Fatalf("Application Launch Failed", "%v", err)
			}
			return
		default:
			uierror.Fatalf("Invalid Arguments", "rigger: unknown start mode %q (expected \"browser\" or \"manifest\")", os.Args[2])
			return
		}
	}
	if err := run(nil); err != nil {
		uierror.Fatalf("Application Launch Failed", "%v", err)
	}
}

// parseURLFlag parses a required -url flag from args, used by both "start
// browser" and "start manifest".
func parseURLFlag(name string, args []string) (string, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	url := fs.String("url", "", "URL")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *url == "" {
		return "", fmt.Errorf("rigger: %s requires -url", name)
	}
	return *url, nil
}

// openBrowser opens the OS default browser at url and returns immediately —
// rundll32 is the standard way to do this on Windows without shell-quoting
// pitfalls (docs/REQUIREMENTS.md §27).
func openBrowser(url string) error {
	return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
}

// runStartManifest is "start manifest -url <URL>": a manual/debug entry
// point, not invoked by any shortcut. url's own query string supplies
// token/other ${uri.X} params (docs/REQUIREMENTS.md §27), and url itself is
// trusted as the manifest fetch target — safe here because this mode only
// ever runs because a person deliberately typed the command, unlike the
// protocol-handler path in run(), which never trusts an externally
// redirected URL as a fetch target (only the token it carries).
func runStartManifest(rawURL string) error {
	uriParams, err := uriparse.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("rigger: could not parse %q: %w", rawURL, err)
	}
	return run(&manifestOverride{manifestURL: rawURL, uriParams: uriParams})
}

// manifestOverride bypasses run()'s normal argv-based invocation detection
// (shortcut vs. protocol handler) — used only by runStartManifest's "start
// manifest -url" debug entry point.
type manifestOverride struct {
	manifestURL string
	uriParams   map[string]string
}

func run(override *manifestOverride) (err error) {
	var logger *applog.Logger
	// progressUI is opened lazily, only if a fetch actually turns out to be
	// needed below — an ordinary launch (everything already on disk) never
	// opens a browser window. Both Notify/Progress/Close are nil-receiver
	// safe, so every other call site below doesn't need its own nil check.
	var progressUI *wizard.ProgressUI
	defer func() {
		if err != nil {
			progressUI.Notify(fmt.Sprintf("Failed: %v", err))
		}
		progressUI.Close()
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
	manifestURL := appValues.ManifestServerURL
	if override != nil {
		invocation = "manifest (explicit URL)"
		uriParams = override.uriParams
		manifestURL = override.manifestURL
	} else if len(os.Args) > 1 && uriparse.LooksLikeInvocation(os.Args[1], appValues.ProtocolScheme) {
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
	// below, which are rare but can legitimately take minutes. When this
	// launch carries an auth token (docs/REQUIREMENTS.md §27), both clients
	// also send it as a Bearer header on every request they make — the
	// manifest fetch and the on-demand JRE/jar downloads alike — in
	// addition to it reaching the launched JVM via ${uri.token} as before.
	manifestClient := proxydetect.Client(proxydetect.Result{Host: appValues.ProxyHost, Port: appValues.ProxyPort}, 10*time.Second)
	fetchClient := proxydetect.Client(proxydetect.Result{Host: appValues.ProxyHost, Port: appValues.ProxyPort}, 5*time.Minute)
	if token := uriParams["token"]; token != "" {
		manifestClient = withBearerToken(manifestClient, token)
		fetchClient = withBearerToken(fetchClient, token)
	}

	m, err := loadManifest(root, manifestURL, manifestClient, logger)
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
		if progressUI == nil {
			progressUI = openProgressUI(m.AppName, logger)
		}
		label := "Java runtime " + m.Runtime.JavaVersion
		progressUI.Notify("Downloading " + label + "...")
		download, install := progressReporters(logger, progressUI, label)
		if err := jreprovision.Provision(fetchClient, root, m.DownloadURL(), m.Runtime.SHA256, jreDir, download, install); err != nil {
			return fmt.Errorf("rigger: could not provision required Java runtime %s: %w", m.Runtime.JavaVersion, err)
		}
		logger.Info("rigger: provisioned Java runtime %s at %s", m.Runtime.JavaVersion, jreDir)
		progressUI.Notify(fmt.Sprintf("Installed %s to `%s`.", label, jreDir))
	}
	logger.Info("rigger: using Java %s at %s", m.Runtime.JavaVersion, jreDir)

	versionDir := layout.VersionDir(root, m.Version)
	if _, err := os.Stat(versionDir); err != nil {
		if progressUI == nil {
			progressUI = openProgressUI(m.AppName, logger)
		}
		if len(m.Jars) > 0 {
			if err := fetchJarsIndividually(fetchClient, root, m, versionDir, logger, progressUI); err != nil {
				return fmt.Errorf("rigger: could not fetch application version %s: %w", m.Version, err)
			}
		} else {
			artifactURL := m.ArtifactDownloadURL()
			logger.Info("rigger: application version %s not found locally at %s — fetching from %s", m.Version, versionDir, artifactURL)
			label := "application version " + m.Version
			progressUI.Notify("Downloading " + label + "...")
			download, install := progressReporters(logger, progressUI, label)
			if err := jarprovision.Provision(fetchClient, root, artifactURL, m.ArtifactSHA256, versionDir, download, install); err != nil {
				return fmt.Errorf("rigger: could not fetch application version %s: %w", m.Version, err)
			}
			logger.Info("rigger: fetched and installed application version %s at %s", m.Version, versionDir)
			progressUI.Notify(fmt.Sprintf("Installed %s to `%s`.", label, versionDir))
		}
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
	progressUI.Notify(fmt.Sprintf("Starting %s %s...", m.AppName, m.Version))
	return javainvoke.Launch(javainvoke.LaunchSpec{
		JREDir:     jreDir,
		Classpath:  classpath,
		MainClass:  m.MainClass,
		JVMOptions: jvmOptions,
		Arguments:  arguments,
	})
}

// openProgressUI best-effort opens a browser progress page for a JRE/jar
// fetch that's about to run — rigger.exe has no progress-bar UI of its own
// otherwise (unlike cmd/installer, it deliberately stays free of
// bubbletea/lipgloss so the always-loaded binary doesn't carry that
// weight, docs/REQUIREMENTS.md §24), so without this an operator watching
// a shortcut launch that needs to fetch 100+ MB on a slow connection has
// no visible sign anything is happening at all — rigger.log alone isn't
// something an ordinary user would think to go find. A failure to open
// (e.g. no browser found) is not fatal — it only means progress is
// reported to the log, same as before this existed.
func openProgressUI(appName string, logger *applog.Logger) *wizard.ProgressUI {
	pu, err := wizard.ShowProgress(appName)
	if err != nil {
		logger.Warn("rigger: warning: could not open progress window (%v); continuing with log-only progress", err)
		return nil
	}
	return pu
}

// formatProgress renders one "<verb> <label> — NN% (X.X/Y.Y MB)" line,
// shared by the durable log and the optional progress page so both report
// identically.
func formatProgress(verb, label string, done, total int64) string {
	const mb = 1024 * 1024
	if total > 0 {
		return fmt.Sprintf("%s %s — %.0f%% (%.1f/%.1f MB)", verb, label, float64(done)/float64(total)*100, float64(done)/mb, float64(total)/mb)
	}
	return fmt.Sprintf("%s %s — %.1f MB", verb, label, float64(done)/mb)
}

// progressReporters builds the download/install ProgressFunc pair for one
// on-demand fetch: always logs via applog (the durable trace, throttled by
// jreprovision's/archiveutil's own progressInterval), and also drives the
// progress page's live status line when one is open — pu may be nil (no
// fetch has needed one yet this run, or opening one failed), and its
// methods are nil-receiver safe, so this never needs its own nil check.
func progressReporters(logger *applog.Logger, pu *wizard.ProgressUI, label string) (download, install jreprovision.ProgressFunc) {
	report := func(verb string) jreprovision.ProgressFunc {
		return func(done, total int64) {
			line := formatProgress(verb, label, done, total)
			logger.Info("rigger: %s", line)
			pu.Progress(line)
		}
	}
	return report("downloading"), report("installing")
}

// fetchJarsIndividually is the per-file alternative to jarprovision.Provision's
// single zip archive, used when the manifest declares m.Jars — an app's own
// choice (docs/REQUIREMENTS.md §9, manifest.Manifest.Jars). Unlike the
// single-archive path, each jar's download is a distinct, named event: it
// gets its own Notify milestone (rather than one blanket "Downloading
// application version X..." for the whole fetch) so an operator watching
// the progress page sees exactly which jar is in flight, matching how a
// new release might add/replace individual jars rather than repackaging
// everything into one blob.
func fetchJarsIndividually(client *http.Client, root string, m *manifest.Manifest, versionDir string, logger *applog.Logger, pu *wizard.ProgressUI) error {
	jars := make([]jarprovision.JarSpec, len(m.Jars))
	for i, j := range m.Jars {
		jars[i] = jarprovision.JarSpec{Path: j.Path, SHA256: j.SHA256}
	}
	baseURL := m.JarsBaseURL()
	logger.Info("rigger: application version %s not found locally at %s — fetching %d jar(s) from %s", m.Version, versionDir, len(jars), baseURL)

	onFile := func(path string, index, total int) {
		msg := fmt.Sprintf("Downloading %s (%d/%d)...", path, index, total)
		logger.Info("rigger: %s", msg)
		pu.Notify(msg)
	}
	onProgress := func(path string, done, total int64) {
		line := formatProgress("downloading", path, done, total)
		logger.Info("rigger: %s", line)
		pu.Progress(line)
	}

	if err := jarprovision.ProvisionJars(client, root, baseURL, jars, versionDir, onFile, onProgress); err != nil {
		return err
	}
	logger.Info("rigger: fetched and installed application version %s at %s", m.Version, versionDir)
	pu.Notify(fmt.Sprintf("Installed application version %s to `%s`.", m.Version, versionDir))
	return nil
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

	fresh, fetchErr := manifest.Fetch(fetchURL, client)
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

// bearerTokenTransport injects an Authorization: Bearer header into every
// request, wrapping whatever transport proxydetect.Client already built so
// proxy routing is preserved (docs/REQUIREMENTS.md §27). Rigger still never
// validates the token itself — that stays the server's job, per §10 Round 3.
type bearerTokenTransport struct {
	base  http.RoundTripper
	token string
}

func (t *bearerTokenTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+t.token)
	return t.base.RoundTrip(req)
}

// withBearerToken returns a client identical to client except every request
// it sends also carries token as an Authorization: Bearer header.
func withBearerToken(client *http.Client, token string) *http.Client {
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{Transport: &bearerTokenTransport{base: base, token: token}, Timeout: client.Timeout}
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
