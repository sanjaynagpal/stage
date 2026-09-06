package jarprovision

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

	if err := Provision(http.DefaultClient, root, url, sha, versionDir); err != nil {
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

	err := Provision(http.DefaultClient, root, url, "0000000000000000000000000000000000000000000000000000000000000000", versionDir)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, statErr := os.Stat(versionDir); statErr == nil {
		t.Fatal("expected versionDir to not be created when checksum verification fails")
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
	if err := Provision(http.DefaultClient, root, url, sha, newDir); err != nil {
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
