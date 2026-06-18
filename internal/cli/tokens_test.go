package cli

import "testing"

func TestCacheHitPct(t *testing.T) {
	cases := []struct {
		name                         string
		input, cacheRead, cacheWrite int64
		want                         float64
	}{
		{"no input-side tokens", 0, 0, 0, 0},
		{"all fresh input", 100, 0, 0, 0},
		{"all cache read", 0, 100, 0, 100},
		{"half cached", 50, 50, 0, 50},
		{"with cache write in denom", 10, 60, 30, 60}, // 60 / (10+60+30) = 60%
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cacheHitPct(c.input, c.cacheRead, c.cacheWrite); got != c.want {
				t.Errorf("cacheHitPct(%d,%d,%d) = %v, want %v", c.input, c.cacheRead, c.cacheWrite, got, c.want)
			}
		})
	}
}

func TestComma(t *testing.T) {
	cases := map[int64]string{0: "0", 42: "42", 999: "999", 1000: "1,000", 1234567: "1,234,567", -1234: "-1,234"}
	for n, want := range cases {
		if got := comma(n); got != want {
			t.Errorf("comma(%d) = %q, want %q", n, got, want)
		}
	}
}
