package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPackageLibraryPlacesExecutableAtZipRoot(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	libraryPath := filepath.Join(dir, "codex-agent-identity.so")
	want := []byte("test shared library")
	if err := os.WriteFile(libraryPath, want, 0o755); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(dir, "release", "plugin.zip")
	archiveData, err := packageLibrary(libraryPath, archivePath)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) != 1 || reader.File[0].Name != "codex-agent-identity.so" || reader.File[0].Mode().Perm() != 0o755 {
		t.Fatalf("unexpected archive entries: %#v", reader.File)
	}
	handle, err := reader.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	got, err := io.ReadAll(handle)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("library data = %q, want %q", got, want)
	}
}

func TestWriteChecksumUsesSha256sumFormat(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := []byte("archive")
	path := filepath.Join(dir, "checksums", "plugin.zip.sha256")
	if err := writeChecksum(path, filepath.Join(dir, "plugin.zip"), data); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	want := hex.EncodeToString(digest[:]) + "  plugin.zip"
	if strings.TrimSpace(string(raw)) != want {
		t.Fatalf("checksum = %q, want %q", raw, want)
	}
}
