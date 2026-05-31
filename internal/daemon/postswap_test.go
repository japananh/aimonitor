package daemon

import (
	"testing"
	"time"
)

func TestParseEtime(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"00:45", 45 * time.Second},
		{"02:30", 2*time.Minute + 30*time.Second},
		{"01:02:03", 1*time.Hour + 2*time.Minute + 3*time.Second},
		{"2-03:04:05", 2*24*time.Hour + 3*time.Hour + 4*time.Minute + 5*time.Second},
	}
	for _, c := range cases {
		got, err := parseEtime(c.in)
		if err != nil {
			t.Errorf("parseEtime(%q) error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseEtime(%q) = %v want %v", c.in, got, c.want)
		}
	}
}

func TestParseEtime_Errors(t *testing.T) {
	bad := []string{"", "not-a-time", "x:y", "1-x:y:z"}
	for _, in := range bad {
		if _, err := parseEtime(in); err == nil {
			t.Errorf("parseEtime(%q) should have errored", in)
		}
	}
}
