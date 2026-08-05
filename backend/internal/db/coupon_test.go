package db_test

import (
	"testing"

	"github.com/agentmesh/backend/internal/db"
)

func TestParseCouponCatalog(t *testing.T) {
	cases := []struct {
		name string
		spec string
		want map[string]int64
	}{
		{"empty", "", map[string]int64{}},
		{"whitespace only", "   ", map[string]int64{}},
		{"single whole dollars", "WELCOME5:5", map[string]int64{"WELCOME5": 5_000_000}},
		{"fractional amount", "HALF:12.50", map[string]int64{"HALF": 12_500_000}},
		{
			"multiple with spacing and trailing comma",
			" welcome5:5 , LAUNCH:1.25, ",
			map[string]int64{"WELCOME5": 5_000_000, "LAUNCH": 1_250_000},
		},
		// Floating point puts 0.1*1e6 a hair under 100000; rounding, not
		// truncation, is what keeps this exact.
		{"amount that floats short", "DIME:0.1", map[string]int64{"DIME": 100_000}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := db.ParseCouponCatalog(tc.spec)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("want %v got %v", tc.want, got)
			}
			for code, amount := range tc.want {
				if got[code] != amount {
					t.Fatalf("code %s: want %d got %d (full: %v)", code, amount, got[code], got)
				}
			}
		})
	}
}

func TestParseCouponCatalogRejectsMalformed(t *testing.T) {
	for _, spec := range []string{
		"NOAMOUNT",        // missing ":AMOUNT"
		":5",              // empty code
		"CODE:notanumber", // unparseable amount
		"CODE:0",          // non-positive
		"CODE:-5",         // negative
		"GOOD:5,BAD",      // one bad entry fails the whole spec
	} {
		if _, err := db.ParseCouponCatalog(spec); err == nil {
			t.Fatalf("spec %q: want error, got none", spec)
		}
	}
}
