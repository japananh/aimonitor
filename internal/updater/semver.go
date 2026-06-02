package updater

import (
	"strconv"
	"strings"
)

// compareSemver compares version strings a and b, returning -1 if a < b,
// 0 if equal, +1 if a > b. It understands a "vX.Y.Z" core with an optional
// "-beta.N"-style pre-release suffix, following SemVer 2.0 precedence: a
// pre-release ranks below its release, and numeric pre-release identifiers
// compare numerically (so beta.9 < beta.10, which a plain string compare
// gets wrong). A leading "v" and build metadata ("+sha") are ignored.
//
// A version whose core can't be parsed (notably the "dev" default of a
// `go build` without ldflags) sorts below any parseable version, so a dev
// build always sees a published release as newer.
func compareSemver(a, b string) int {
	ca, pa, oka := parseSemver(a)
	cb, pb, okb := parseSemver(b)
	switch {
	case !oka && !okb:
		return 0
	case !oka:
		return -1
	case !okb:
		return 1
	}
	for i := 0; i < 3; i++ {
		if ca[i] != cb[i] {
			if ca[i] < cb[i] {
				return -1
			}
			return 1
		}
	}
	return comparePrerelease(pa, pb)
}

// parseSemver splits "v1.2.3-beta.4" into core [1,2,3] and pre-release
// "beta.4". ok is false when the core has no parseable numeric component.
func parseSemver(s string) (core [3]int, pre string, ok bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return core, "", false
	}
	if i := strings.IndexByte(s, '+'); i >= 0 { // drop build metadata
		s = s[:i]
	}
	corePart := s
	if i := strings.IndexByte(s, '-'); i >= 0 {
		corePart = s[:i]
		pre = s[i+1:]
	}
	fields := strings.Split(corePart, ".")
	for i := 0; i < 3 && i < len(fields); i++ {
		n, err := strconv.Atoi(fields[i])
		if err != nil {
			return [3]int{}, "", false
		}
		core[i] = n
		ok = true
	}
	return core, pre, ok
}

// comparePrerelease applies SemVer pre-release precedence. An empty
// pre-release (a final release) ranks ABOVE any pre-release.
func comparePrerelease(a, b string) int {
	if a == b {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if as[i] == bs[i] {
			continue
		}
		an, aerr := strconv.Atoi(as[i])
		bn, berr := strconv.Atoi(bs[i])
		switch {
		case aerr == nil && berr == nil:
			if an < bn {
				return -1
			}
			return 1
		case aerr == nil: // numeric identifiers rank below alphanumeric
			return -1
		case berr == nil:
			return 1
		default:
			if as[i] < bs[i] {
				return -1
			}
			return 1
		}
	}
	switch {
	case len(as) < len(bs):
		return -1
	case len(as) > len(bs):
		return 1
	}
	return 0
}
