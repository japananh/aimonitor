package config

import (
	"errors"
	"reflect"
	"testing"
)

func TestParseThresholds(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []int
		wantErr bool
	}{
		{"default", "40,60,100", []int{40, 60, 100}, false},
		{"single", "50", []int{50}, false},
		{"single max", "100", []int{100}, false},
		{"whitespace tolerated", " 10 , 20 , 30 ", []int{10, 20, 30}, false},
		{"two values", "40,60", []int{40, 60}, false},

		// invalid
		{"empty", "", nil, true},
		{"whitespace only", "   ", nil, true},
		{"zero", "0", nil, true},
		{"negative", "-1,50", nil, true},
		{"too big", "101", nil, true},
		{"way too big", "200,300", nil, true},
		{"not ascending", "60,40", nil, true},
		{"duplicate", "40,40", nil, true},
		{"non-int", "40,abc,60", nil, true},
		{"trailing comma", "40,60,", nil, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseThresholds(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				if !errors.Is(err, ErrInvalidThresholds) {
					t.Fatalf("error must wrap ErrInvalidThresholds; got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFormatThresholds(t *testing.T) {
	got := FormatThresholds([]int{40, 60, 100})
	if got != "40,60,100" {
		t.Fatalf("got %q, want %q", got, "40,60,100")
	}
}

func TestDefaultThresholdsAreValid(t *testing.T) {
	if err := ValidateThresholds(DefaultThresholds); err != nil {
		t.Fatalf("DefaultThresholds must be valid; got %v", err)
	}
}
