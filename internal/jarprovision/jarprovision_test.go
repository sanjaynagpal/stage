package jarprovision

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// serveTestZip starts an httptest server serving a zip archive built from
// entries, and returns the server's URL and the archive's sha256 hex.
func serveTestZip(t *testing.T, entries map[string]string) (string, string) {
	t.Helper()

	zipPath := filepath.Join(t.TempDir(), "artifact.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	w := zip.NewWriter(f)
	for name, content := range entries {
		fw, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	sumHex := hex.EncodeToString(sum[:])

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, sumHex
}

func TestProvisionDownloadsVerifiesAndUnpacks(t *testing.T) {
	url, sha := serveTestZip(t, map[string]string{"app.jar": "fake-jar"})

	root := t.TempDir()
	versionDir := filepath.Join(root, "1.5.0")

	if err := Provision(http.DefaultClient, root, url, sha, versionDir, nil, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(versionDir, "app.jar"))
	if err != nil {
		t.Fatalf("expected app.jar to exist: %v", err)
	}
	if string(got) != "fake-jar" {
		t.Fatalf("content = %q, want %q", got, "fake-jar")
	}
}

func TestProvisionRejectsChecksumMismatch(t *testing.T) {
	url, _ := serveTestZip(t, map[string]string{"app.jar": "fake-jar"})

	root := t.TempDir()
	versionDir := filepath.Join(root, "1.5.0")

	err := Provision(http.DefaultClient, root, url, "0000000000000000000000000000000000000000000000000000000000000000", versionDir, nil, nil)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, statErr := os.Stat(versionDir); statErr == nil {
		t.Fatal("expected versionDir to not be created when checksum verification fails")
	}
}

// serveTestFiles starts an httptest server serving files (path -> content)
// each at its own URL, and returns the server's base URL (trailing slash,
// ready to append a Path to, matching manifest.Manifest.JarsBaseURL's
// convention) plus the corresponding []JarSpec.
func serveTestFiles(t *testing.T, files map[string]string) (string, []JarSpec) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		content, ok := files[path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(content))
	}))
	t.Cleanup(srv.Close)

	jars := make([]JarSpec, 0, len(files))
	for path, content := range files {
		sum := sha256.Sum256([]byte(content))
		jars = append(jars, JarSpec{Path: path, SHA256: hex.EncodeToString(sum[:])})
	}
	sort.Slice(jars, func(i, j int) bool { return jars[i].Path < jars[j].Path })
	return srv.URL + "/", jars
}

func TestProvisionJarsDownloadsEachFileToItsPath(t *testing.T) {
	baseURL, jars := serveTestFiles(t, map[string]string{
		"app.jar":           "fake-app",
		"lib/gson-2.10.jar": "fake-gson",
	})

	root := t.TempDir()
	versionDir := filepath.Join(root, "1.5.0")

	if err := ProvisionJars(http.DefaultClient, root, baseURL, jars, versionDir, nil, nil); err != nil {
		t.Fatalf("ProvisionJars: %v", err)
	}

	for path, want := range map[string]string{"app.jar": "fake-app", "lib/gson-2.10.jar": "fake-gson"} {
		got, err := os.ReadFile(filepath.Join(versionDir, filepath.FromSlash(path)))
		if err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
		if string(got) != want {
			t.Fatalf("%s content = %q, want %q", path, got, want)
		}
	}
}

func TestProvisionJarsCallsOnFileForEachJarInOrder(t *testing.T) {
	baseURL, jars := serveTestFiles(t, map[string]string{
		"a.jar": "aaa",
		"b.jar": "bbb",
	})

	root := t.TempDir()
	versionDir := filepath.Join(root, "1.5.0")

	var calls []string
	onFile := func(path string, index, total int) {
		if total != len(jars) {
			t.Errorf("onFile(%q, %d, %d): total = %d, want %d", path, index, total, total, len(jars))
		}
		calls = append(calls, path)
	}
	if err := ProvisionJars(http.DefaultClient, root, baseURL, jars, versionDir, onFile, nil); err != nil {
		t.Fatalf("ProvisionJars: %v", err)
	}

	if len(calls) != 2 || calls[0] != "a.jar" || calls[1] != "b.jar" {
		t.Fatalf("onFile calls = %v, want [a.jar b.jar] in order", calls)
	}
}

func TestProvisionJarsReportsPerFileProgress(t *testing.T) {
	baseURL, jars := serveTestFiles(t, map[string]string{"app.jar": "fake-app-contents"})

	root := t.TempDir()
	versionDir := filepath.Join(root, "1.5.0")

	var gotPath string
	var lastDone, lastTotal int64
	onProgress := func(path string, done, total int64) {
		gotPath = path
		lastDone, lastTotal = done, total
	}
	if err := ProvisionJars(http.DefaultClient, root, baseURL, jars, versionDir, nil, onProgress); err != nil {
		t.Fatalf("ProvisionJars: %v", err)
	}

	if gotPath != "app.jar" {
		t.Fatalf("onProgress path = %q, want %q", gotPath, "app.jar")
	}
	if lastDone != lastTotal || lastTotal != int64(len("fake-app-contents")) {
		t.Fatalf("final onProgress call = (%d, %d), want (%d, %d)", lastDone, lastTotal, len("fake-app-contents"), len("fake-app-contents"))
	}
}

func TestProvisionJarsRejectsChecksumMismatch(t *testing.T) {
	baseURL, jars := serveTestFiles(t, map[string]string{"app.jar": "fake-app"})
	jars[0].SHA256 = "0000000000000000000000000000000000000000000000000000000000000000"

	root := t.TempDir()
	versionDir := filepath.Join(root, "1.5.0")

	if err := ProvisionJars(http.DefaultClient, root, baseURL, jars, versionDir, nil, nil); err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, statErr := os.Stat(versionDir); statErr == nil {
		t.Fatal("expected versionDir to not be created when checksum verification fails")
	}
}

func TestProvisionJarsEvictsOldestVersionBeyondMax(t *testing.T) {
	baseURL, jars := serveTestFiles(t, map[string]string{"app.jar": "fake-app"})

	root := t.TempDir()
	mkEntry := func(name string, age time.Duration) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatal(err)
		}
	}
	mkEntry("1.3.0", 2*time.Hour)
	mkEntry("1.4.0", 1*time.Hour)

	newDir := filepath.Join(root, "1.5.0")
	if err := ProvisionJars(http.DefaultClient, root, baseURL, jars, newDir, nil, nil); err != nil {
		t.Fatalf("ProvisionJars: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}
	if len(names) != MaxRetainedVersions {
		t.Fatalf("expected exactly %d retained versions, got %v", MaxRetainedVersions, names)
	}
	if names["1.3.0"] {
		t.Fatal("expected the oldest version (1.3.0) to have been evicted")
	}
}

func TestProvisionEvictsOldestVersionBeyondMaxButKeepsReservedEntries(t *testing.T) {
	url, sha := serveTestZip(t, map[string]string{"app.jar": "fake-jar"})

	root := t.TempDir()

	mkEntry := func(name string, age time.Duration) {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatal(err)
		}
	}
	mkEntry("1.3.0", 3*time.Hour)
	mkEntry("1.4.0", 2*time.Hour)
	// "jre" is a reserved root entry, not a version directory: it must
	// survive eviction no matter how old it is.
	mkEntry("jre", 100*time.Hour)

	newDir := filepath.Join(root, "1.5.0")
	if err := Provision(http.DefaultClient, root, url, sha, newDir, nil, nil); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make(map[string]bool, len(entries))
	for _, e := range entries {
		names[e.Name()] = true
	}

	if !names["jre"] {
		t.Fatal("expected the reserved \"jre\" entry to survive eviction")
	}
	if names["1.3.0"] {
		t.Fatal("expected the oldest version (1.3.0) to have been evicted")
	}
	if !names["1.4.0"] || !names["1.5.0"] {
		t.Fatalf("expected the two newest versions to be retained, got %v", names)
	}
}
