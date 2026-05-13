package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// DefaultThresholds is the default tripwire list applied when no user config exists.
var DefaultThresholds = []int{40, 60, 100}

// ErrInvalidThresholds is returned when a threshold list fails validation rules.
var ErrInvalidThresholds = errors.New("invalid thresholds")

// ParseThresholds parses a comma-separated list (e.g. "40,60,100") and validates it.
// Whitespace around values is ignored. An empty string is invalid.
func ParseThresholds(s string) ([]int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, fmt.Errorf("%w: must provide at least one value", ErrInvalidThresholds)
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for i, p := range parts {
		p = strings.TrimSpace(p)
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("%w: value %d (%q) is not an integer", ErrInvalidThresholds, i+1, p)
		}
		out = append(out, n)
	}
	if err := ValidateThresholds(out); err != nil {
		return nil, err
	}
	return out, nil
}

// ValidateThresholds enforces:
//   - at least one value
//   - all values in the range (0, 100]
//   - strictly ascending (no duplicates, no decreases)
func ValidateThresholds(ts []int) error {
	if len(ts) == 0 {
		return fmt.Errorf("%w: must provide at least one value", ErrInvalidThresholds)
	}
	prev := 0
	for i, v := range ts {
		if v <= 0 {
			return fmt.Errorf("%w: value %d (%d) must be > 0", ErrInvalidThresholds, i+1, v)
		}
		if v > 100 {
			return fmt.Errorf("%w: value %d (%d) must be <= 100", ErrInvalidThresholds, i+1, v)
		}
		if i > 0 && v <= prev {
			return fmt.Errorf("%w: values must be strictly ascending; got %d after %d at position %d", ErrInvalidThresholds, v, prev, i+1)
		}
		prev = v
	}
	return nil
}

// FormatThresholds renders a threshold list back to the canonical comma-separated form.
func FormatThresholds(ts []int) string {
	parts := make([]string, len(ts))
	for i, v := range ts {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ",")
}
