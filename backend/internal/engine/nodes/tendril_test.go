package nodes

import "testing"

// Renting is a flat 1¢ gate fee; hours are bought by holding credit. So "2
// hours on a $6/hr box" costs the user $12.00 of their own Tendril credit,
// plus the 1¢ gate fee for the rent call itself.
func TestRequiredCreditAtomic(t *testing.T) {
	cases := []struct {
		name  string
		rate  int64
		hours float64
		want  int64
	}{
		{"two hours at six dollars", 6_000_000, 2, 12_010_000},
		{"one hour at six dollars", 6_000_000, 1, 6_010_000},
		{"half hour at one fifty", 1_500_000, 0.5, 760_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := RequiredCreditAtomic(tc.rate, tc.hours); got != tc.want {
				t.Errorf("RequiredCreditAtomic(%d, %v) = %d, want %d", tc.rate, tc.hours, got, tc.want)
			}
		})
	}
}

// Hours come off a canvas text field, so every rejection here is a rejection
// of real money being spent on a nonsense duration.
func TestParseHours(t *testing.T) {
	ok := map[string]float64{"1": 1, "2": 2, "0.5": 0.5, " 3 ": 3, "": 1}
	for in, want := range ok {
		got, err := parseHours(in)
		if err != nil {
			t.Errorf("parseHours(%q) errored: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseHours(%q) = %v, want %v", in, got, want)
		}
	}
	for _, bad := range []string{"0", "-1", "abc", "1e9", "25"} {
		if _, err := parseHours(bad); err == nil {
			t.Errorf("parseHours(%q) should have errored", bad)
		}
	}
}
