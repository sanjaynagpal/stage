package diskspace

import (
	"path/filepath"
	"testing"
)

func TestAvailableBytesOnExistingDir(t *testing.T) {
	dir := t.TempDir()
	available, err := AvailableBytes(dir)
	if err != nil {
		t.Fatalf("AvailableBytes: %v", err)
	}
	if available == 0 {
		t.Fatal("expected nonzero available disk space")
	}
}

func TestAvailableBytesResolvesToExistingAncestor(t *testing.T) {
	dir := t.TempDir()
	notYetCreated := filepath.Join(dir, "ABC", "1.5.0")
	available, err := AvailableBytes(notYetCreated)
	if err != nil {
		t.Fatalf("AvailableBytes on not-yet-created path: %v", err)
	}
	if available == 0 {
		t.Fatal("expected nonzero available disk space")
	}
}

func TestHasSpaceFor(t *testing.T) {
	dir := t.TempDir()
	ok, err := HasSpaceFor(dir, 1)
	if err != nil {
		t.Fatalf("HasSpaceFor: %v", err)
	}
	if !ok {
		t.Fatal("expected at least 1 byte free")
	}

	ok, err = HasSpaceFor(dir, ^uint64(0))
	if err != nil {
		t.Fatalf("HasSpaceFor: %v", err)
	}
	if ok {
		t.Fatal("expected max-uint64 bytes to exceed available space")
	}
}

func TestAvailableBytesErrorsWhenNoAncestorExists(t *testing.T) {
	// A drive letter that's extremely unlikely to exist.
	_, err := AvailableBytes(`Z:\definitely\does\not\exist`)
	if err == nil {
		t.Skip("Z: drive exists on this machine; cannot exercise the not-found path")
	}
}
