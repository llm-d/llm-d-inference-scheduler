package multi

import (
	"testing"
)

func FuzzParseContextRange(f *testing.F) {
	// Seed corpus: valid formats
	f.Add("0-2048")
	f.Add("2048-8192")
	f.Add("0-0")
	f.Add("100-100")

	// Seed corpus: edge cases and invalid inputs
	f.Add("")
	f.Add("-")
	f.Add("abc")
	f.Add("1-2-3")
	f.Add("-1-100")
	f.Add("100--1")
	f.Add("999999999999999999999-1")
	f.Add("  0  -  2048  ")
	f.Add("0-")
	f.Add("-0")
	f.Add("0-100-200")
	f.Add("100-50")
	f.Add("abc-100")

	f.Fuzz(func(t *testing.T, rangeStr string) {
		r, err := parseContextRange(rangeStr)
		if err != nil {
			return
		}

		// Invariant: min must not exceed max
		if r.min > r.max {
			t.Errorf("parseContextRange(%q) returned min (%d) > max (%d)", rangeStr, r.min, r.max)
		}

		// Context lengths should be non-negative
		if r.min < 0 {
			t.Errorf("parseContextRange(%q) accepted negative min value: %d", rangeStr, r.min)
		}
		if r.max < 0 {
			t.Errorf("parseContextRange(%q) accepted negative max value: %d", rangeStr, r.max)
		}
	})
}
