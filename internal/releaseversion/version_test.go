package releaseversion

import "testing"

func TestCompareSemVerPrecedence(t *testing.T) {
	cases := []struct {
		left, right string
		want        int
	}{
		{"0.3.8-rc.1", "0.3.8", -1},
		{"1.0.0-alpha", "1.0.0-alpha.1", -1},
		{"1.0.0-alpha.1", "1.0.0-alpha.beta", -1},
		{"1.0.0-beta.2", "1.0.0-beta.11", -1},
		{"1.0.0-rc.1", "1.0.0", -1},
		{"1.0.0", "1.0.0", 0},
	}
	for _, tc := range cases {
		left, err := Parse(tc.left)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.left, err)
		}
		right, err := Parse(tc.right)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tc.right, err)
		}
		if got := Compare(left, right); got != tc.want {
			t.Errorf("Compare(%q, %q) = %d, want %d", tc.left, tc.right, got, tc.want)
		}
	}
}

func TestCompareDoesNotOverflowLargeNumericIdentifiers(t *testing.T) {
	left, err := Parse("0.3.8-999999999999999999999999999999999999")
	if err != nil {
		t.Fatal(err)
	}
	right, err := Parse("0.3.8-1000000000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if got := Compare(left, right); got != -1 {
		t.Fatalf("large numeric prerelease comparison = %d, want -1", got)
	}
}

func TestParseRejectsNumericPrereleaseLeadingZero(t *testing.T) {
	if _, err := Parse("1.0.0-01"); err == nil {
		t.Fatal("Parse accepted a numeric prerelease identifier with a leading zero")
	}
}
