package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnnumberedRollbackAllowsCIButNotTagPublication(t *testing.T) {
	root := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("VERSION", "0.3.17\n")
	write("Makefile", "VERSION ?= $(strip $(file <VERSION))\n")
	write("plugin/codex-agent-identity/plugin.go", `var pluginVersion = "0.3.17"`+"\n"+`const minimumSidecarVersion = "0.3.10"`)
	write(".env.example", "SIDECAR_IMAGE=ghcr.io/simplez2/cpa-codex-agent-identity:v0.3.15\n")
	artifacts := []map[string]any{}
	for _, arch := range []string{"amd64", "arm64"} {
		artifacts = append(artifacts, map[string]any{
			"goos": "linux", "goarch": arch, "size": 1, "sha256": strings.Repeat("a", 64),
			"url": "https://github.com/simplez2/cpa-codex-agent-identity/releases/download/v0.3.15/codex-agent-identity_0.3.15_linux_" + arch + ".zip",
		})
	}
	raw, _ := json.Marshal(map[string]any{"plugins": []map[string]any{{"version": "0.3.15", "install": map[string]any{"artifacts": artifacts}}}})
	write("registry.json", string(raw))
	write("CHANGELOG.md", "## [Unreleased]\n\nFixes awaiting acceptance.\n\n## [0.3.17] - 2026-09-08\nWithdrawn.\n")
	if err := verify(root, "", false); err != nil {
		t.Fatal(err)
	}
	if err := verify(root, "v0.3.17", false); err == nil {
		t.Fatal("unassigned fixes must not permit re-publishing the withdrawn tag")
	}
	if err := verify(root, "", true); err == nil {
		t.Fatal("rollback must not pass strict source/registry release matching")
	}
	write("CHANGELOG.md", "## [0.3.17] - 2026-09-08\n")
	if err := verify(root, "", false); err == nil {
		t.Fatal("release hold requires an explicit unnumbered changes section")
	}
}
