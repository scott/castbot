package update

import (
	"strconv"
	"strings"
)

// ver is a numeric major.minor.patch. Build metadata after + is ignored.
type ver struct {
	major int
	minor int
	patch int
}

// Newer reports whether candidate is strictly newer than running.
// dev, a blank version, and a version that is not major.minor.patch are never newer.
// 0.0.0+<sha> compares as 0.0.0, so a dated release is newer.
func Newer(running, candidate string) bool {
	have, ok := parseVersion(running)
	if !ok {
		return false
	}
	want, ok := parseVersion(candidate)
	if !ok {
		return false
	}
	if want.major != have.major {
		return want.major > have.major
	}
	if want.minor != have.minor {
		return want.minor > have.minor
	}
	return want.patch > have.patch
}

func parseVersion(s string) (ver, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "dev" {
		return ver{}, false
	}
	s = strings.TrimPrefix(s, "v")
	s, _, _ = strings.Cut(s, "+")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return ver{}, false
	}
	nums := [3]int{}
	for i, part := range parts {
		if part == "" || len(part) > 9 || part[0] == '+' || part[0] == '-' {
			return ver{}, false
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 {
			return ver{}, false
		}
		nums[i] = n
	}
	return ver{major: nums[0], minor: nums[1], patch: nums[2]}, true
}
