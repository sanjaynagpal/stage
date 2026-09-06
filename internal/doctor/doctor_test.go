package doctor

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckDirMissing(t *testing.T) {
	c := checkDir("Install directory", filepath.Join(t.TempDir(), "nope"))
	if c.Status != StatusFail {
		t.Fatalf("Status = %v, want StatusFail", c.Status)
	}
}

func TestCheckDirEmptyPath(t *testing.T) {
	c := checkDir("Install directory", "")
	if c.Status != StatusFail {
		t.Fatalf("Status = %v, want StatusFail for an unset path", c.Status)
	}
}

func TestCheckDirExists(t *testing.T) {
	c := checkDir("Install directory", t.TempDir())
	if c.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK", c.Status)
	}
}

func TestCheckJREMissingJavaw(t *testing.T) {
	c := checkJRE(t.TempDir(), "jre/21.0.2+13")
	if c.Status != StatusFail {
		t.Fatalf("Status = %v, want StatusFail when javaw.exe is missing", c.Status)
	}
}

func TestCheckJREPresent(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "jre", "21.0.2+13", "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(binDir, "javaw.exe"), []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := checkJRE(root, "jre/21.0.2+13")
	if c.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK when javaw.exe is present", c.Status)
	}
}

func TestCheckNetworkNoURL(t *testing.T) {
	c := checkNetwork("", "", "")
	if c.Status != StatusFail {
		t.Fatalf("Status = %v, want StatusFail for an empty URL", c.Status)
	}
}

func TestCheckNetworkUnreachable(t *testing.T) {
	c := checkNetwork("http://127.0.0.1:1/nope", "", "")
	if c.Status != StatusFail {
		t.Fatalf("Status = %v, want StatusFail for an unreachable server", c.Status)
	}
}

func TestCheckNetworkValidManifest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"appId": "ABC", "appName": "ABC", "version": "1.0.0", "environment": "PROD",
			"manifestServerUrl": "` + r.Host + `",
			"runtime": {"javaVersion": "21.0.2+13", "path": "jre/21.0.2+13"},
			"classpath": ["1.0.0/app.jar"], "mainClass": "com.example.abc.Main",
			"shortcut": {"name": "ABC", "description": "d", "startMenu": true, "desktop": true}
		}`))
	}))
	defer srv.Close()

	c := checkNetwork(srv.URL, "", "")
	if c.Status != StatusOK {
		t.Fatalf("Status = %v, want StatusOK for a reachable, valid manifest; detail: %s", c.Status, c.Detail)
	}
}

func TestCheckNetworkUnexpectedStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := checkNetwork(srv.URL, "", "")
	if c.Status != StatusFail {
		t.Fatalf("Status = %v, want StatusFail for a non-200 response", c.Status)
	}
}

func TestCheckNetworkInvalidManifestBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c := checkNetwork(srv.URL, "", "")
	if c.Status != StatusWarn {
		t.Fatalf("Status = %v, want StatusWarn for a reachable server with a bad body", c.Status)
	}
}

func TestProxyDetail(t *testing.T) {
	if got := proxyDetail("", ""); got != "none (direct connection)" {
		t.Fatalf("proxyDetail(empty) = %q", got)
	}
	if got := proxyDetail("proxy.example.com", "8080"); got != "proxy.example.com:8080" {
		t.Fatalf("proxyDetail(host,port) = %q", got)
	}
}

func TestCollectLogsBundlesEveryLogFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "rigger.log"), []byte("rigger log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "abc-launch.log"), []byte("app log"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "not-a-log.txt"), []byte("ignore me"), 0o644); err != nil {
		t.Fatal(err)
	}

	zipPath, err := CollectLogs(dir)
	if err != nil {
		t.Fatalf("CollectLogs: %v", err)
	}
	if _, err := os.Stat(zipPath); err != nil {
		t.Fatalf("expected zip to exist at %s: %v", zipPath, err)
	}
}

func TestCollectLogsErrorsWhenNoLogsExist(t *testing.T) {
	if _, err := CollectLogs(t.TempDir()); err == nil {
		t.Fatal("expected an error when no *.log files exist")
	}
}
