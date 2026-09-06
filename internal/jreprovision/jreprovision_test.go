package jreprovision

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

// writeTestZip creates a zip archive built from entries under t.TempDir()
// and returns its path and sha256 hex.
func writeTestZip(t *testing.T, entries map[string]string) (string, string) {
	t.Helper()

	zipPath := filepath.Join(t.TempDir(), "jre.zip")
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
	return zipPath, hex.EncodeToString(sum[:])
}

// serveTestZip starts an httptest server serving a zip archive built from
// entries, and returns the server, its URL, and the archive's sha256 hex.
func serveTestZip(t *testing.T, entries map[string]string) (*httptest.Server, string, string) {
	t.Helper()

	zipPath, sumHex := writeTestZip(t, entries)
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, srv.URL, sumHex
}

func TestProvisionDownloadsVerifiesAndUnpacks(t *testing.T) {
	_, url, sha := serveTestZip(t, map[string]string{
		"bin/javaw.exe": "fake-exe",
	})

	root := t.TempDir()
	jreDir := filepath.Join(root, "jre", "21.0.2+13")

	if err := Provision(http.DefaultClient, root, url, sha, jreDir); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(jreDir, "bin", "javaw.exe"))
	if err != nil {
		t.Fatalf("expected bin/javaw.exe to exist: %v", err)
	}
	if string(got) != "fake-exe" {
		t.Fatalf("content = %q, want %q", got, "fake-exe")
	}
}

func TestProvisionRejectsChecksumMismatch(t *testing.T) {
	_, url, _ := serveTestZip(t, map[string]string{
		"bin/javaw.exe": "fake-exe",
	})

	root := t.TempDir()
	jreDir := filepath.Join(root, "jre", "21.0.2+13")

	err := Provision(http.DefaultClient, root, url, "0000000000000000000000000000000000000000000000000000000000000000", jreDir)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, statErr := os.Stat(jreDir); statErr == nil {
		t.Fatal("expected jreDir to not be created when the checksum verification fails")
	}
}

func TestProvisionEvictsOldestVersionBeyondMax(t *testing.T) {
	_, url, sha := serveTestZip(t, map[string]string{
		"bin/javaw.exe": "fake-exe",
	})

	root := t.TempDir()
	jreRoot := filepath.Join(root, "jre")

	mkOldVersion := func(name string, age time.Duration) {
		dir := filepath.Join(jreRoot, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatal(err)
		}
	}
	mkOldVersion("17.0.9+9", 2*time.Hour)
	mkOldVersion("20.0.1+9", 1*time.Hour)

	newDir := filepath.Join(jreRoot, "21.0.2+13")
	if err := Provision(http.DefaultClient, root, url, sha, newDir); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	entries, err := os.ReadDir(jreRoot)
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
	if names["17.0.9+9"] {
		t.Fatal("expected the oldest version (17.0.9+9) to have been evicted")
	}
	if !names["20.0.1+9"] || !names["21.0.2+13"] {
		t.Fatalf("expected the two newest versions to be retained, got %v", names)
	}
}

func TestProvisionLocalVerifiesAndUnpacks(t *testing.T) {
	archivePath, sha := writeTestZip(t, map[string]string{
		"bin/javaw.exe": "fake-exe",
	})

	root := t.TempDir()
	jreDir := filepath.Join(root, "jre", "21.0.2+13")

	if err := ProvisionLocal(root, archivePath, sha, jreDir); err != nil {
		t.Fatalf("ProvisionLocal: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(jreDir, "bin", "javaw.exe"))
	if err != nil {
		t.Fatalf("expected bin/javaw.exe to exist: %v", err)
	}
	if string(got) != "fake-exe" {
		t.Fatalf("content = %q, want %q", got, "fake-exe")
	}
}

func TestProvisionLocalRejectsChecksumMismatch(t *testing.T) {
	archivePath, _ := writeTestZip(t, map[string]string{
		"bin/javaw.exe": "fake-exe",
	})

	root := t.TempDir()
	jreDir := filepath.Join(root, "jre", "21.0.2+13")

	err := ProvisionLocal(root, archivePath, "0000000000000000000000000000000000000000000000000000000000000000", jreDir)
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
	if _, statErr := os.Stat(jreDir); statErr == nil {
		t.Fatal("expected jreDir to not be created when the checksum verification fails")
	}
}

func TestProvisionLocalEvictsOldestVersionBeyondMax(t *testing.T) {
	archivePath, sha := writeTestZip(t, map[string]string{
		"bin/javaw.exe": "fake-exe",
	})

	root := t.TempDir()
	jreRoot := filepath.Join(root, "jre")

	mkOldVersion := func(name string, age time.Duration) {
		dir := filepath.Join(jreRoot, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		when := time.Now().Add(-age)
		if err := os.Chtimes(dir, when, when); err != nil {
			t.Fatal(err)
		}
	}
	mkOldVersion("17.0.9+9", 2*time.Hour)
	mkOldVersion("20.0.1+9", 1*time.Hour)

	newDir := filepath.Join(jreRoot, "21.0.2+13")
	if err := ProvisionLocal(root, archivePath, sha, newDir); err != nil {
		t.Fatalf("ProvisionLocal: %v", err)
	}

	entries, err := os.ReadDir(jreRoot)
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
	if names["17.0.9+9"] {
		t.Fatal("expected the oldest version (17.0.9+9) to have been evicted")
	}
	if !names["20.0.1+9"] || !names["21.0.2+13"] {
		t.Fatalf("expected the two newest versions to be retained, got %v", names)
	}
}
