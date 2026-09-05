package archiveutil

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// writeTestZip creates a zip archive at a temp path containing the given
// name->content entries, verbatim (no prefix manipulation), and returns its
// path.
func writeTestZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.zip")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

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
	return path
}

func TestExtractZipFlatArchive(t *testing.T) {
	archive := writeTestZip(t, map[string]string{
		"bin/javaw.exe": "fake-exe",
		"lib/foo.txt":   "hello",
	})
	dest := filepath.Join(t.TempDir(), "out")

	if err := ExtractZip(archive, dest); err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "bin", "javaw.exe"))
	if err != nil {
		t.Fatalf("expected bin/javaw.exe to exist: %v", err)
	}
	if string(got) != "fake-exe" {
		t.Fatalf("bin/javaw.exe content = %q, want %q", got, "fake-exe")
	}
}

func TestExtractZipDoesNotStripWrapperDirectory(t *testing.T) {
	// Stage's archive convention is flat, with no vendor-style wrapper
	// directory — a wrapped archive extracts verbatim, wrapper included.
	archive := writeTestZip(t, map[string]string{
		"jdk-21.0.2+13-jre/bin/javaw.exe": "fake-exe",
	})
	dest := filepath.Join(t.TempDir(), "out")

	if err := ExtractZip(archive, dest); err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dest, "jdk-21.0.2+13-jre", "bin", "javaw.exe"))
	if err != nil {
		t.Fatalf("expected the archive to extract verbatim, wrapper directory included: %v", err)
	}
	if string(got) != "fake-exe" {
		t.Fatalf("content = %q, want %q", got, "fake-exe")
	}
}

func TestExtractZipRejectsPathTraversal(t *testing.T) {
	archive := writeTestZip(t, map[string]string{
		"safe/file.txt": "fine",
		"../evil.txt":   "malicious",
	})
	dest := filepath.Join(t.TempDir(), "out")

	err := ExtractZip(archive, dest)
	if err == nil {
		t.Fatal("expected an error for a path-traversal zip entry")
	}
	if _, statErr := os.Stat(filepath.Join(filepath.Dir(dest), "evil.txt")); statErr == nil {
		t.Fatal("path-traversal entry was written outside the destination directory")
	}
}

func TestExtractZipFlatMultiDirArchive(t *testing.T) {
	archive := writeTestZip(t, map[string]string{
		"a/file1.txt": "one",
		"b/file2.txt": "two",
	})
	dest := filepath.Join(t.TempDir(), "out")

	if err := ExtractZip(archive, dest); err != nil {
		t.Fatalf("ExtractZip: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "a", "file1.txt")); err != nil {
		t.Fatalf("expected a/file1.txt to exist: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, "b", "file2.txt")); err != nil {
		t.Fatalf("expected b/file2.txt to exist: %v", err)
	}
}
