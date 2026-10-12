package version

import (
	"strings"
	"testing"
)

func TestCompare(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"1.2.0", "1.10.0", -1},
		{"1.0.0-dev.2", "1.0.0", -1},
		{"1.0.0-dev.2", "1.0.0-dev.10", -1},
		{"2.0.0", "1.9.9", 1},
		{"v2.0.0", "1.9.9", -1}, // not a version: sorts first
		{"1.0.0+build.7", "1.0.0", 0},
		{"1.0.0-alpha", "1.0.0-beta", -1},
		{"1.0.0-rc-1.x-2", "1.0.0-rc-1.x-3", -1},
	}
	for _, c := range cases {
		if got := CompareStrings(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s,%s)=%d want %d", c.a, c.b, got, c.want)
		}
	}
	for _, bad := range []string{"1.0", "01.0.0", "1.0.0-", "a.b.c", "", "1.0.0-../../etc", "1.0.0-a/b", "1.0.0-a..b",
		"1.0.0-.a", "1.0.0-a.", "1.0.0-a b", "1.0.0-ä", "1.0.0-a\b",
		// versions name directories: no prefix, no whitespace, strict build metadata
		"v1.0.0", " 1.0.0", "1.0.0 ", "1.0.0\n", "1.0.0+", "1.0.0+a/b", "1.0.0+../x", "1.0.0+a..b", "1.0.0+.a",
		"1.0.0+a b", "1.0.0+a+b", "1.0.0-a+", "+1.0.0", "1.+0.0", "1.0.0" + strings.Repeat("0", 64)} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
		if Valid(bad) {
			t.Errorf("Valid(%q) should be false", bad)
		}
	}
	for _, good := range []string{"0.0.0-dev", "1.2.3", "1.2.3-rc.1", "1.2.3+build.7", "1.2.3-rc-1+20261011.x-y"} {
		if !Valid(good) {
			t.Errorf("Valid(%q) should be true", good)
		}
	}
}

func TestValidRelease(t *testing.T) {
	for s, want := range map[string]bool{"1.2.3": true, "1.2.3-rc.1": true, "0.1.0-dev.202610111200": true,
		"1.2.3+build": false, "v1.2.3": false, "01.2.3": false, "1.2.3-": false, "1.2.3-a..b": false,
		"1.2.3-" + strings.Repeat("a", 60): false} {
		if ValidRelease(s) != want {
			t.Errorf("ValidRelease(%q) = %v", s, !want)
		}
	}
}
