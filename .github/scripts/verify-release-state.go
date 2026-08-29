package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

type registryDocument struct {
	Plugins []struct {
		Version string `json:"version"`
		Install struct {
			Artifacts []struct {
				GOOS   string `json:"goos"`
				GOArch string `json:"goarch"`
				URL    string `json:"url"`
				SHA256 string `json:"sha256"`
				Size   int64  `json:"size"`
			} `json:"artifacts"`
		} `json:"install"`
	} `json:"plugins"`
}

type semver struct {
	major      int
	minor      int
	patch      int
	prerelease []string
}

var semverPattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

func main() {
	root := flag.String("root", ".", "repository root")
	tag := flag.String("tag", "", "optional release tag to validate")
	requireMatch := flag.Bool("require-registry-match", false, "require registry version to equal source version")
	flag.Parse()

	if err := verify(filepath.Clean(*root), strings.TrimSpace(*tag), *requireMatch); err != nil {
		fmt.Fprintln(os.Stderr, "release-state:", err)
		os.Exit(1)
	}
	fmt.Println("release-state: source and published metadata are consistent")
}

func verify(root, tag string, requireRegistryMatch bool) error {
	sourceText, err := readTrimmed(filepath.Join(root, "VERSION"))
	if err != nil {
		return fmt.Errorf("read VERSION: %w", err)
	}
	source, err := parseSemver(sourceText)
	if err != nil {
		return fmt.Errorf("VERSION %q is invalid: %w", sourceText, err)
	}

	makefile, err := readFile(filepath.Join(root, "Makefile"))
	if err != nil {
		return fmt.Errorf("read Makefile: %w", err)
	}
	if !strings.Contains(makefile, "VERSION ?= $(strip $(file <VERSION))") {
		return errors.New("Makefile must derive its default VERSION from VERSION")
	}

	plugin, err := readFile(filepath.Join(root, "plugin", "codex-agent-identity", "plugin.go"))
	if err != nil {
		return fmt.Errorf("read plugin.go: %w", err)
	}
	pluginVersion := regexp.MustCompile(`pluginVersion\s*=\s*"([^"]+)"`).FindStringSubmatch(plugin)
	if len(pluginVersion) != 2 || pluginVersion[1] != sourceText {
		got := "missing"
		if len(pluginVersion) == 2 {
			got = pluginVersion[1]
		}
		return fmt.Errorf("plugin.go version %q does not match VERSION %q", got, sourceText)
	}
	minimumSidecar := regexp.MustCompile(`minimumSidecarVersion\s*=\s*"([^"]+)"`).FindStringSubmatch(plugin)
	if len(minimumSidecar) != 2 {
		return errors.New("plugin.go does not declare minimumSidecarVersion")
	}
	minimumSidecarVersion, err := parseSemver(minimumSidecar[1])
	if err != nil {
		return fmt.Errorf("minimumSidecarVersion %q is invalid: %w", minimumSidecar[1], err)
	}
	if compareSemver(minimumSidecarVersion, source) > 0 {
		return fmt.Errorf("minimumSidecarVersion %s is newer than source version %s", minimumSidecar[1], sourceText)
	}

	var registry registryDocument
	rawRegistry, err := readFile(filepath.Join(root, "registry.json"))
	if err != nil {
		return fmt.Errorf("read registry.json: %w", err)
	}
	if err := json.Unmarshal([]byte(rawRegistry), &registry); err != nil {
		return fmt.Errorf("parse registry.json: %w", err)
	}
	if len(registry.Plugins) != 1 {
		return fmt.Errorf("registry must contain exactly one plugin, got %d", len(registry.Plugins))
	}
	publishedText := strings.TrimSpace(registry.Plugins[0].Version)
	published, err := parseSemver(publishedText)
	if err != nil {
		return fmt.Errorf("registry version %q is invalid: %w", publishedText, err)
	}
	if compareSemver(published, source) > 0 {
		return fmt.Errorf("registry version %s is ahead of source version %s", publishedText, sourceText)
	}
	if requireRegistryMatch && publishedText != sourceText {
		return fmt.Errorf("registry version %s must match source version %s", publishedText, sourceText)
	}
	if err := verifyRegistryArtifacts(registry.Plugins[0].Install.Artifacts, publishedText); err != nil {
		return err
	}

	envExample, err := readFile(filepath.Join(root, ".env.example"))
	if err != nil {
		return fmt.Errorf("read .env.example: %w", err)
	}
	image := regexp.MustCompile(`(?m)^SIDECAR_IMAGE=[^\r\n:]+(?:/[^\r\n:]+)*:v([^\s]+)\s*$`).FindStringSubmatch(envExample)
	if len(image) != 2 || image[1] != publishedText {
		got := "missing"
		if len(image) == 2 {
			got = image[1]
		}
		return fmt.Errorf(".env.example SIDECAR_IMAGE tag %q does not match published registry version %q", got, publishedText)
	}

	changelog, err := readFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		return fmt.Errorf("read CHANGELOG.md: %w", err)
	}
	if !strings.Contains(changelog, "## [Unreleased] - "+sourceText) {
		return fmt.Errorf("CHANGELOG.md has no Unreleased section for %s", sourceText)
	}

	if tag != "" && tag != "v"+sourceText {
		return fmt.Errorf("tag %q does not match source version v%s", tag, sourceText)
	}
	return nil
}

func verifyRegistryArtifacts(artifacts []struct {
	GOOS   string `json:"goos"`
	GOArch string `json:"goarch"`
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}, published string) error {
	if len(artifacts) != 2 {
		return fmt.Errorf("registry must contain exactly two plugin artifacts, got %d", len(artifacts))
	}
	seen := make(map[string]bool, len(artifacts))
	for _, artifact := range artifacts {
		if artifact.GOOS != "linux" || (artifact.GOArch != "amd64" && artifact.GOArch != "arm64") {
			return fmt.Errorf("registry artifact has unsupported target %s/%s", artifact.GOOS, artifact.GOArch)
		}
		if seen[artifact.GOArch] {
			return fmt.Errorf("registry contains duplicate %s artifact", artifact.GOArch)
		}
		seen[artifact.GOArch] = true
		expectedURL := fmt.Sprintf("https://github.com/simplez2/cpa-codex-agent-identity/releases/download/v%s/codex-agent-identity_%s_linux_%s.zip", published, published, artifact.GOArch)
		if artifact.URL != expectedURL {
			return fmt.Errorf("registry %s artifact URL does not match version %s", artifact.GOArch, published)
		}
		if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(artifact.SHA256) {
			return fmt.Errorf("registry %s artifact SHA-256 is invalid", artifact.GOArch)
		}
		if artifact.Size <= 0 {
			return fmt.Errorf("registry %s artifact size must be positive", artifact.GOArch)
		}
	}
	if !seen["amd64"] || !seen["arm64"] {
		return errors.New("registry must contain both linux/amd64 and linux/arm64 artifacts")
	}
	return nil
}

func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	return string(data), err
}

func readTrimmed(path string) (string, error) {
	value, err := readFile(path)
	return strings.TrimSpace(value), err
}

func parseSemver(value string) (semver, error) {
	matches := semverPattern.FindStringSubmatch(value)
	if len(matches) != 5 {
		return semver{}, errors.New("expected MAJOR.MINOR.PATCH with an optional prerelease")
	}
	result := semver{}
	for index, target := range []*int{&result.major, &result.minor, &result.patch} {
		parsed, err := strconv.Atoi(matches[index+1])
		if err != nil {
			return semver{}, fmt.Errorf("numeric component %q is invalid: %w", matches[index+1], err)
		}
		*target = parsed
	}
	if matches[4] != "" {
		for _, identifier := range strings.Split(matches[4], ".") {
			if identifier == "" {
				return semver{}, errors.New("prerelease contains an empty identifier")
			}
			if isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return semver{}, fmt.Errorf("numeric prerelease identifier %q has a leading zero", identifier)
			}
			result.prerelease = append(result.prerelease, identifier)
		}
	}
	return result, nil
}

func isNumeric(value string) bool {
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return value != ""
}

func compareSemver(left, right semver) int {
	for _, pair := range [][2]int{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if len(left.prerelease) == 0 && len(right.prerelease) == 0 {
		return 0
	}
	if len(left.prerelease) == 0 {
		return 1
	}
	if len(right.prerelease) == 0 {
		return -1
	}
	for index := 0; index < len(left.prerelease) && index < len(right.prerelease); index++ {
		leftID, rightID := left.prerelease[index], right.prerelease[index]
		leftNumeric, rightNumeric := isNumeric(leftID), isNumeric(rightID)
		if leftNumeric && rightNumeric {
			leftValue, _ := strconv.Atoi(leftID)
			rightValue, _ := strconv.Atoi(rightID)
			if leftValue < rightValue {
				return -1
			}
			if leftValue > rightValue {
				return 1
			}
			continue
		}
		if leftNumeric != rightNumeric {
			if leftNumeric {
				return -1
			}
			return 1
		}
		if leftID < rightID {
			return -1
		}
		if leftID > rightID {
			return 1
		}
	}
	if len(left.prerelease) < len(right.prerelease) {
		return -1
	}
	if len(left.prerelease) > len(right.prerelease) {
		return 1
	}
	return 0
}
