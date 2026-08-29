package main

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/simplez2/cpa-codex-agent-identity/internal/releaseversion"
)

type registryDocument struct {
	SchemaVersion int              `json:"schema_version"`
	Plugins       []registryPlugin `json:"plugins"`
}

type registryPlugin struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Author      string          `json:"author"`
	Version     string          `json:"version"`
	Repository  string          `json:"repository"`
	Install     registryInstall `json:"install"`
	Logo        string          `json:"logo"`
	Homepage    string          `json:"homepage"`
	License     string          `json:"license"`
	Tags        []string        `json:"tags"`
}

type registryInstall struct {
	Type      string             `json:"type"`
	Artifacts []registryArtifact `json:"artifacts"`
}

type registryArtifact struct {
	GOOS   string `json:"goos"`
	GOArch string `json:"goarch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

func main() {
	root := flag.String("root", ".", "repository root")
	assetsDir := flag.String("assets-dir", "dist/release-assets", "directory containing the released plugin zip archives")
	version := flag.String("version", "", "version to publish; defaults to VERSION")
	flag.Parse()

	if err := publish(filepath.Clean(*root), filepath.Clean(*assetsDir), strings.TrimSpace(*version)); err != nil {
		fmt.Fprintln(os.Stderr, "publish-registry:", err)
		os.Exit(1)
	}
}

func publish(root, assetsDir, requestedVersion string) error {
	if !filepath.IsAbs(assetsDir) {
		assetsDir = filepath.Join(root, assetsDir)
	}
	sourceVersion, err := readTrimmed(filepath.Join(root, "VERSION"))
	if err != nil {
		return fmt.Errorf("read VERSION: %w", err)
	}
	source, err := releaseversion.Parse(sourceVersion)
	if err != nil {
		return fmt.Errorf("VERSION %q is invalid: %w", sourceVersion, err)
	}
	if requestedVersion == "" {
		requestedVersion = sourceVersion
	}
	if requestedVersion != sourceVersion {
		return fmt.Errorf("requested version %s does not match VERSION %s", requestedVersion, sourceVersion)
	}

	registryPath := filepath.Join(root, "registry.json")
	registryData, err := os.ReadFile(registryPath)
	if err != nil {
		return fmt.Errorf("read registry.json: %w", err)
	}
	var registry registryDocument
	if err := json.Unmarshal(registryData, &registry); err != nil {
		return fmt.Errorf("parse registry.json: %w", err)
	}
	if registry.SchemaVersion != 2 || len(registry.Plugins) != 1 {
		return errors.New("registry.json must contain exactly one schema v2 plugin")
	}
	plugin := &registry.Plugins[0]
	if plugin.ID != "codex-agent-identity" || plugin.Install.Type != "direct" {
		return errors.New("registry.json does not describe codex-agent-identity direct artifacts")
	}
	published, err := releaseversion.Parse(plugin.Version)
	if err != nil {
		return fmt.Errorf("current registry version %q is invalid: %w", plugin.Version, err)
	}
	if releaseversion.Compare(published, source) >= 0 {
		return fmt.Errorf("registry version %s is not older than requested publish version %s", plugin.Version, requestedVersion)
	}

	artifacts := make([]registryArtifact, 0, 2)
	for _, arch := range []string{"amd64", "arm64"} {
		name := fmt.Sprintf("codex-agent-identity_%s_linux_%s.zip", requestedVersion, arch)
		path := filepath.Join(assetsDir, name)
		artifact, err := inspectArchive(path, requestedVersion, arch)
		if err != nil {
			return err
		}
		artifacts = append(artifacts, artifact)
	}
	plugin.Version = requestedVersion
	plugin.Install.Artifacts = artifacts

	updatedRegistry, err := json.MarshalIndent(registry, "", "  ")
	if err != nil {
		return fmt.Errorf("encode registry.json: %w", err)
	}
	updatedRegistry = append(updatedRegistry, '\n')

	envPath := filepath.Join(root, ".env.example")
	envData, err := os.ReadFile(envPath)
	if err != nil {
		return fmt.Errorf("read .env.example: %w", err)
	}
	envPattern := regexp.MustCompile(`(?m)^(SIDECAR_IMAGE=[^\r\n:]+(?:/[^\r\n:]+)*:)v[^\r\n\s]+$`)
	if !envPattern.Match(envData) {
		return errors.New(".env.example has no tag-based SIDECAR_IMAGE entry to update")
	}
	updatedEnv := envPattern.ReplaceAll(envData, []byte("${1}v"+requestedVersion))

	if err := writeAtomically(registryPath, updatedRegistry); err != nil {
		return fmt.Errorf("write registry.json: %w", err)
	}
	if err := writeAtomically(envPath, updatedEnv); err != nil {
		return fmt.Errorf("write .env.example: %w", err)
	}
	fmt.Printf("published registry metadata for v%s from verified local archives\n", requestedVersion)
	return nil
}

func inspectArchive(path, version, arch string) (registryArtifact, error) {
	info, err := os.Stat(path)
	if err != nil {
		return registryArtifact{}, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Size() == 0 {
		return registryArtifact{}, fmt.Errorf("release archive %s is empty or not a regular file", path)
	}
	reader, err := zip.OpenReader(path)
	if err != nil {
		return registryArtifact{}, fmt.Errorf("open release archive %s: %w", path, err)
	}
	defer reader.Close()
	foundLibrary := false
	for _, entry := range reader.File {
		if entry.Name == "codex-agent-identity.so" {
			foundLibrary = true
			break
		}
	}
	if !foundLibrary {
		return registryArtifact{}, fmt.Errorf("release archive %s does not contain codex-agent-identity.so at its root", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return registryArtifact{}, fmt.Errorf("read release archive %s: %w", path, err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return registryArtifact{}, fmt.Errorf("hash release archive %s: %w", path, err)
	}
	return registryArtifact{
		GOOS:   "linux",
		GOArch: arch,
		URL:    fmt.Sprintf("https://github.com/simplez2/cpa-codex-agent-identity/releases/download/v%s/codex-agent-identity_%s_linux_%s.zip", version, version, arch),
		SHA256: hex.EncodeToString(hash.Sum(nil)),
		Size:   info.Size(),
	}, nil
}

func writeAtomically(path string, data []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".release-state-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func readTrimmed(path string) (string, error) {
	data, err := os.ReadFile(path)
	return strings.TrimSpace(string(data)), err
}
