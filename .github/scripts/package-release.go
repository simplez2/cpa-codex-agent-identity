package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func main() {
	libraryPath := flag.String("library", "", "path to the compiled plugin library")
	archivePath := flag.String("archive", "", "path to the output zip archive")
	checksumPath := flag.String("checksum", "", "path to the output checksum file")
	flag.Parse()

	if *libraryPath == "" || *archivePath == "" || *checksumPath == "" {
		fatalf("library, archive, and checksum are required")
	}
	archiveData, err := packageLibrary(*libraryPath, *archivePath)
	if err != nil {
		fatalf("%v", err)
	}
	if err = writeChecksum(*checksumPath, *archivePath, archiveData); err != nil {
		fatalf("%v", err)
	}
}

func packageLibrary(libraryPath, archivePath string) ([]byte, error) {
	info, err := os.Lstat(libraryPath)
	if err != nil {
		return nil, fmt.Errorf("stat library: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("plugin library must be a regular file")
	}
	if info.Size() == 0 {
		return nil, errors.New("plugin library is empty")
	}
	library, err := os.Open(libraryPath)
	if err != nil {
		return nil, fmt.Errorf("open library: %w", err)
	}
	defer library.Close()

	if err = os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
		return nil, fmt.Errorf("create archive directory: %w", err)
	}
	archive, err := os.Create(archivePath)
	if err != nil {
		return nil, fmt.Errorf("create archive: %w", err)
	}
	writer := zip.NewWriter(archive)
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		_ = archive.Close()
		return nil, fmt.Errorf("create zip header: %w", err)
	}
	header.Name = filepath.Base(libraryPath)
	header.Method = zip.Deflate
	header.SetMode(0o755)
	entry, err := writer.CreateHeader(header)
	if err == nil {
		_, err = io.Copy(entry, library)
	}
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if closeErr := archive.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, fmt.Errorf("write archive: %w", err)
	}
	data, err := os.ReadFile(archivePath)
	if err != nil {
		return nil, fmt.Errorf("read archive: %w", err)
	}
	return data, nil
}

func writeChecksum(checksumPath, archivePath string, archiveData []byte) error {
	if len(archiveData) == 0 {
		return errors.New("archive is empty")
	}
	if err := os.MkdirAll(filepath.Dir(checksumPath), 0o755); err != nil {
		return fmt.Errorf("create checksum directory: %w", err)
	}
	digest := sha256.Sum256(archiveData)
	line := fmt.Sprintf("%s  %s\n", hex.EncodeToString(digest[:]), filepath.Base(archivePath))
	if err := os.WriteFile(checksumPath, []byte(line), 0o644); err != nil {
		return fmt.Errorf("write checksum: %w", err)
	}
	return nil
}

func fatalf(format string, values ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", values...)
	os.Exit(1)
}
