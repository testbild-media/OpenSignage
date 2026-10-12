// Package version holds the single product version shared by every component.
package version

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Version is injected at build time via
// -ldflags "-X opensignage/internal/common/version.Version=1.2.3".
var Version = "0.0.0-dev"

// Commit is the source revision, injected at build time.
var Commit = "unknown"

// Protocol is the device link protocol version. It only changes on breaking changes.
const Protocol = 1

// SemVer is a parsed semantic version.
type SemVer struct {
	Major, Minor, Patch int
	Pre                 string
}

// MaxLen is the longest accepted version string.
const MaxLen = 64

// releaseRe is a plain SemVer without build metadata: the form of release versions (tags
// v<version>, package names, directories releases/<version>).
var releaseRe = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$`)

// ValidRelease reports whether s is a release version: MAJOR.MINOR.PATCH[-pre], no build
// metadata, at most MaxLen characters.
func ValidRelease(s string) bool { return len(s) <= MaxLen && releaseRe.MatchString(s) }

// Parse parses MAJOR.MINOR.PATCH[-pre][+build] strictly. Versions name directories
// (releases/<v>) and files, so nothing else is accepted: no "v" prefix, no whitespace,
// pre-release and build metadata only [0-9A-Za-z-] identifiers separated by single dots
// (never "/", ".." or other characters), at most MaxLen characters.
func Parse(s string) (SemVer, error) {
	var v SemVer
	if len(s) > MaxLen {
		return v, fmt.Errorf("invalid version %q: longer than %d characters", s, MaxLen)
	}
	orig := s
	if i := strings.IndexByte(s, '+'); i >= 0 {
		if !validIdents(s[i+1:]) {
			return v, fmt.Errorf("invalid version %q: bad build metadata", orig)
		}
		s = s[:i]
	}
	core := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		core, v.Pre = s[:i], s[i+1:]
		if v.Pre == "" {
			return v, fmt.Errorf("invalid version %q: empty pre-release", orig)
		}
		if !validIdents(v.Pre) {
			return v, fmt.Errorf("invalid version %q: bad pre-release", orig)
		}
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return v, fmt.Errorf("invalid version %q", orig)
	}
	nums := [3]*int{&v.Major, &v.Minor, &v.Patch}
	for i, p := range parts {
		if p == "" || strings.IndexFunc(p, func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
			return v, fmt.Errorf("invalid version %q", orig)
		}
		n, err := strconv.Atoi(p)
		if err != nil || (len(p) > 1 && p[0] == '0') {
			return v, fmt.Errorf("invalid version %q", orig)
		}
		*nums[i] = n
	}
	return v, nil
}

// Valid reports whether Parse accepts s.
func Valid(s string) bool {
	_, err := Parse(s)
	return err == nil
}

// validIdents checks dot separated, non-empty [0-9A-Za-z-] identifiers.
func validIdents(s string) bool {
	for _, id := range strings.Split(s, ".") {
		if id == "" || strings.IndexFunc(id, func(r rune) bool {
			return !(r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '-')
		}) >= 0 {
			return false
		}
	}
	return true
}

func (v SemVer) String() string {
	s := fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
	if v.Pre != "" {
		s += "-" + v.Pre
	}
	return s
}

// Compare returns -1, 0 or 1. Pre-releases sort before the release.
func Compare(a, b SemVer) int {
	for _, d := range [][2]int{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if d[0] != d[1] {
			if d[0] < d[1] {
				return -1
			}
			return 1
		}
	}
	switch {
	case a.Pre == b.Pre:
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	}
	return comparePre(a.Pre, b.Pre)
}

// CompareStrings compares two version strings; unparsable versions sort first.
func CompareStrings(a, b string) int {
	va, errA := Parse(a)
	vb, errB := Parse(b)
	switch {
	case errA != nil && errB != nil:
		return strings.Compare(a, b)
	case errA != nil:
		return -1
	case errB != nil:
		return 1
	}
	return Compare(va, vb)
}

func comparePre(a, b string) int {
	ap, bp := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(ap) && i < len(bp); i++ {
		an, aErr := strconv.Atoi(ap[i])
		bn, bErr := strconv.Atoi(bp[i])
		switch {
		case aErr == nil && bErr == nil:
			if an != bn {
				if an < bn {
					return -1
				}
				return 1
			}
		case aErr == nil:
			return -1
		case bErr == nil:
			return 1
		default:
			if c := strings.Compare(ap[i], bp[i]); c != 0 {
				return c
			}
		}
	}
	switch {
	case len(ap) < len(bp):
		return -1
	case len(ap) > len(bp):
		return 1
	}
	return 0
}
