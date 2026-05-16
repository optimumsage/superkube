package upgrade

import (
	"fmt"
	"strconv"
	"strings"
)

// compareSemver returns -1, 0, +1 if a < b, a == b, a > b. Inputs may carry
// a leading "v"; pre-release suffixes like "-next" sort BEFORE the same base
// version (so "0.2.0-next" < "0.2.0"), matching goreleaser's snapshot scheme.
//
// We intentionally avoid pulling in golang.org/x/mod/semver to keep the
// dependency tree tight — our needs are modest.
func compareSemver(a, b string) (int, error) {
	an, ap, err := parseSemver(a)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", a, err)
	}
	bn, bp, err := parseSemver(b)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", b, err)
	}
	for i := 0; i < 3; i++ {
		if an[i] != bn[i] {
			if an[i] < bn[i] {
				return -1, nil
			}
			return 1, nil
		}
	}
	// Numeric parts equal — pre-release breaks the tie. A version with a
	// pre-release suffix is considered LESS than the same version without.
	switch {
	case ap == "" && bp == "":
		return 0, nil
	case ap == "" && bp != "":
		return 1, nil
	case ap != "" && bp == "":
		return -1, nil
	default:
		// Both have pre-release; lexicographic is fine for our use.
		return strings.Compare(ap, bp), nil
	}
}

// parseSemver splits "1.2.3" or "1.2.3-rc1" into ([1,2,3], "rc1"). Missing
// components default to 0 so "0.2" parses as 0.2.0.
func parseSemver(s string) ([3]int, string, error) {
	s = stripV(s)
	pre := ""
	if i := strings.IndexByte(s, '-'); i >= 0 {
		pre = s[i+1:]
		s = s[:i]
	}
	parts := strings.Split(s, ".")
	var nums [3]int
	for i := 0; i < len(parts) && i < 3; i++ {
		n, err := strconv.Atoi(parts[i])
		if err != nil {
			return nums, "", fmt.Errorf("non-numeric component %q", parts[i])
		}
		nums[i] = n
	}
	return nums, pre, nil
}
