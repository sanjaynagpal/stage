// Package doctor implements the diagnostic checks behind rigger.exe's
// --doctor mode (docs/REQUIREMENTS.md §19-20, §24): registry/JRE integrity
// validation, a manifest-server reachability check, and log collection.
// Pure logic, no UI — internal/wizard renders whatever Run and CollectLogs
// produce.
package doctor

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/sanjaynagpal/stage/internal/layout"
	"github.com/sanjaynagpal/stage/internal/manifest"
	"github.com/sanjaynagpal/stage/internal/proxydetect"
	"github.com/sanjaynagpal/stage/internal/winreg"
)

// Status is a check's outcome.
type Status int

const (
	StatusOK Status = iota
	StatusWarn
	StatusFail
)

// String matches the CSS class names internal/wizard's doctor template
// switches on.
func (s Status) String() string {
	switch s {
	case StatusOK:
		return "ok"
	case StatusWarn:
		return "warn"
	default:
		return "fail"
	}
}

// Check is one diagnostic check's name, outcome, and human-readable detail.
type Check struct {
	Name   string
	Status Status
	Detail string
}

// Report is the full result of Run: every check performed, plus whatever
// was learned along the way that CollectLogs and the "Contact Support"
// link need (DataDir, SupportEmail).
type Report struct {
	AppID        string
	Checks       []Check
	DataDir      string // "" if the registry check failed — nothing else could be located
	SupportEmail string
}

// Run performs every diagnostic check for appID, degrading gracefully at
// each step: a failed registry read (the worst case doctor mode exists to
// help with) still returns a Report with that one check marked failed,
// rather than an error — there is always something to show the operator.
func Run(appID string) Report {
	r := Report{AppID: appID}

	appValues, err := winreg.ReadAppValues(appID)
	if err != nil {
		r.Checks = append(r.Checks, Check{
			Name:   "Registry",
			Status: StatusFail,
			Detail: fmt.Sprintf("Software\\%s is missing or corrupt (%v) — reinstalling %s should fix this.", appID, err, appID),
		})
		return r
	}
	r.Checks = append(r.Checks, Check{
		Name:   "Registry",
		Status: StatusOK,
		Detail: fmt.Sprintf("Install scope: %s, package mode: %s", appValues.InstallScope, appValues.PackageMode),
	})
	r.DataDir = appValues.DataDir
	r.SupportEmail = "" // filled in below once the manifest is known, if it declares one

	r.Checks = append(r.Checks, checkDir("Install directory", appValues.InstallDir))
	r.Checks = append(r.Checks, checkDir("Data directory", appValues.DataDir))

	m, mErr := manifest.Load(layout.ManifestPath(appValues.InstallDir))
	if mErr != nil {
		r.Checks = append(r.Checks, Check{
			Name:   "Manifest",
			Status: StatusFail,
			Detail: fmt.Sprintf("could not load the cached manifest: %v", mErr),
		})
	} else {
		r.Checks = append(r.Checks, Check{
			Name:   "Manifest",
			Status: StatusOK,
			Detail: fmt.Sprintf("version %s, requires Java %s", m.Version, m.Runtime.JavaVersion),
		})
		r.Checks = append(r.Checks, checkJRE(appValues.InstallDir, m.Runtime.Path))
		r.SupportEmail = m.SupportEmail
	}

	r.Checks = append(r.Checks, checkNetwork(appValues.ManifestServerURL, appValues.ProxyHost, appValues.ProxyPort))
	r.Checks = append(r.Checks, Check{
		Name:   "Proxy",
		Status: StatusOK,
		Detail: proxyDetail(appValues.ProxyHost, appValues.ProxyPort),
	})
	r.Checks = append(r.Checks, Check{
		Name:   "Network zone",
		Status: StatusOK,
		Detail: string(appValues.NetworkZone),
	})

	return r
}

func checkDir(name, path string) Check {
	if path == "" {
		return Check{Name: name, Status: StatusFail, Detail: "not set in the registry"}
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		return Check{Name: name, Status: StatusFail, Detail: fmt.Sprintf("%s does not exist", path)}
	}
	return Check{Name: name, Status: StatusOK, Detail: path}
}

// checkJRE goes beyond "does the directory exist" — it confirms javaw.exe
// is actually there, per the "deep validation" decision (docs/REQUIREMENTS.md §24).
func checkJRE(installDir, runtimePath string) Check {
	jreDir := filepath.Join(installDir, runtimePath)
	javaw := filepath.Join(jreDir, "bin", "javaw.exe")
	if _, err := os.Stat(javaw); err != nil {
		return Check{Name: "Java runtime", Status: StatusFail, Detail: fmt.Sprintf("javaw.exe not found at %s", javaw)}
	}
	return Check{Name: "Java runtime", Status: StatusOK, Detail: jreDir}
}

// checkNetwork performs a full HTTP GET against the manifest server and
// parses the response as a manifest — not just a bare TCP connect — using
// the registry's already-resolved proxy rather than re-detecting live
// (docs/REQUIREMENTS.md §18's "Rigger never re-detects" principle, §24).
func checkNetwork(manifestServerURL, proxyHost, proxyPort string) Check {
	if manifestServerURL == "" {
		return Check{Name: "Manifest server", Status: StatusFail, Detail: "no manifestServerUrl on record"}
	}
	client := proxydetect.Client(proxydetect.Result{Host: proxyHost, Port: proxyPort}, 10*time.Second)

	start := time.Now()
	resp, err := client.Get(manifestServerURL)
	elapsed := time.Since(start)
	if err != nil {
		return Check{Name: "Manifest server", Status: StatusFail, Detail: fmt.Sprintf("%s: %v", manifestServerURL, err)}
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return Check{Name: "Manifest server", Status: StatusFail, Detail: fmt.Sprintf("%s: reading response: %v", manifestServerURL, err)}
	}
	if resp.StatusCode != http.StatusOK {
		return Check{Name: "Manifest server", Status: StatusFail, Detail: fmt.Sprintf("%s: HTTP %s", manifestServerURL, resp.Status)}
	}
	if _, err := manifest.Parse(data); err != nil {
		return Check{
			Name:   "Manifest server",
			Status: StatusWarn,
			Detail: fmt.Sprintf("%s: reachable (%dms) but the response is not a valid manifest: %v", manifestServerURL, elapsed.Milliseconds(), err),
		}
	}
	return Check{
		Name:   "Manifest server",
		Status: StatusOK,
		Detail: fmt.Sprintf("%s: reachable, valid manifest (%dms)", manifestServerURL, elapsed.Milliseconds()),
	}
}

func proxyDetail(host, port string) string {
	if host == "" {
		return "none (direct connection)"
	}
	return fmt.Sprintf("%s:%s", host, port)
}

// CollectLogs zips every *.log file directly under dataDir (rigger.log,
// plus whatever the app itself writes there, without hardcoding any
// app-specific filename — Stage supports arbitrary apps) into a new file
// under dataDir, returning its path.
func CollectLogs(dataDir string) (string, error) {
	matches, err := filepath.Glob(filepath.Join(dataDir, "*.log"))
	if err != nil {
		return "", fmt.Errorf("doctor: list logs in %s: %w", dataDir, err)
	}
	if len(matches) == 0 {
		return "", fmt.Errorf("doctor: no *.log files found in %s", dataDir)
	}

	outPath := filepath.Join(dataDir, fmt.Sprintf("diagnostics-%s.zip", time.Now().Format("20060102-150405")))
	out, err := os.Create(outPath)
	if err != nil {
		return "", fmt.Errorf("doctor: create %s: %w", outPath, err)
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	for _, path := range matches {
		if err := addFileToZip(zw, path); err != nil {
			zw.Close()
			return "", err
		}
	}
	if err := zw.Close(); err != nil {
		return "", fmt.Errorf("doctor: finalize %s: %w", outPath, err)
	}
	return outPath, nil
}

func addFileToZip(zw *zip.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("doctor: open %s: %w", path, err)
	}
	defer f.Close()

	w, err := zw.Create(filepath.Base(path))
	if err != nil {
		return fmt.Errorf("doctor: add %s to zip: %w", path, err)
	}
	if _, err := io.Copy(w, f); err != nil {
		return fmt.Errorf("doctor: write %s to zip: %w", path, err)
	}
	return nil
}
