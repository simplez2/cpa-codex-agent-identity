package releaseversion

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// Version is a SemVer 2.0.0 version represented without fixed-width integers.
// Numeric components remain strings so valid, very large identifiers cannot
// overflow during release ordering comparisons.
type Version struct {
	major      string
	minor      string
	patch      string
	prerelease []string
}

var pattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

// Parse parses a SemVer 2.0.0 version.
func Parse(value string) (Version, error) {
	matches := pattern.FindStringSubmatch(value)
	if len(matches) != 5 {
		return Version{}, errors.New("expected MAJOR.MINOR.PATCH with an optional prerelease")
	}
	result := Version{major: matches[1], minor: matches[2], patch: matches[3]}
	if matches[4] == "" {
		return result, nil
	}
	for _, identifier := range strings.Split(matches[4], ".") {
		if isNumeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
			return Version{}, fmt.Errorf("numeric prerelease identifier %q has a leading zero", identifier)
		}
		result.prerelease = append(result.prerelease, identifier)
	}
	return result, nil
}

// Compare applies SemVer precedence and returns -1, 0, or 1.
func Compare(left, right Version) int {
	for _, pair := range [][2]string{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if result := compareNumeric(pair[0], pair[1]); result != 0 {
			return result
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
			if result := compareNumeric(leftID, rightID); result != 0 {
				return result
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

func compareNumeric(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func isNumeric(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}
