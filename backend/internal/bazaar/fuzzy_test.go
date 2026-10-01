package bazaar

import "testing"

func TestFuzzyMatchEmptyQueryAlwaysMatches(t *testing.T) {
	score, ok := FuzzyMatch("anything at all", "")
	if !ok || score != 0 {
		t.Errorf("FuzzyMatch(_, \"\") = (%d, %v), want (0, true)", score, ok)
	}
}

func TestFuzzyMatchIsCaseInsensitiveSubsequence(t *testing.T) {
	for _, tc := range []struct{ text, query string }{
		{"GoPlausible", "gplausible"}, // missing letter, wrong case
		{"tendrilregister.007575.xyz", "tendril"},
		{"CANIX402 DeFi execution quotes", "canix quotes"}, // spans two words
	} {
		if _, ok := FuzzyMatch(tc.text, tc.query); !ok {
			t.Errorf("FuzzyMatch(%q, %q) = not a match, want a match", tc.text, tc.query)
		}
	}
}

func TestFuzzyMatchRejectsOutOfOrderOrMissingRunes(t *testing.T) {
	for _, tc := range []struct{ text, query string }{
		{"Prism", "mspri"},        // right letters, wrong order
		{"Tendril", "tendrilxyz"}, // query has runes text doesn't
		{"", "anything"},          // nothing to match against
	} {
		if _, ok := FuzzyMatch(tc.text, tc.query); ok {
			t.Errorf("FuzzyMatch(%q, %q) = a match, want none", tc.text, tc.query)
		}
	}
}

func TestFuzzyMatchRanksAContiguousMatchAboveAScatteredOne(t *testing.T) {
	// Both are valid subsequence matches for "prism"; only the first has it
	// as one contiguous word. The scattered fixture is deliberately still
	// within maxFuzzySpanSlack/Const (a few stray characters between each
	// letter, not "somewhere in an entire sentence") -- this test is about
	// the score gap between the two shapes, not about the span bound, which
	// TestFuzzyMatchRejectsAScatteredMatchAcrossTooWideASpan covers.
	contiguous, ok := FuzzyMatch("Prism code review", "prism")
	if !ok {
		t.Fatal("contiguous case: want a match")
	}
	scattered, ok := FuzzyMatch("p.r.i.s.m, spread out", "prism")
	if !ok {
		t.Fatal("scattered case: want a match")
	}
	if contiguous <= scattered {
		t.Errorf("contiguous score %d, scattered score %d -- want contiguous strictly higher", contiguous, scattered)
	}
}

// TestFuzzyMatchRejectsAScatteredMatchAcrossTooWideASpan is the regression
// test for a bug a security-adjacent review pass caught: unbounded
// subsequence matching let "tendril" (7 runes) fuzzy-"match" a completely
// unrelated ~120-character Prism endpoint description, because some
// occurrence of t, then e, then n, then d, then r, then i, then l existed
// somewhere in that much text, in that order, purely by chance -- a real
// false positive in a live handler test (TestBazaarResourcesSearchFilters),
// not a hypothetical. maxFuzzySpanSlack/Const is what closes it.
func TestFuzzyMatchRejectsAScatteredMatchAcrossTooWideASpan(t *testing.T) {
	text := "A careful review of one file, the kind a senior engineer would give it, with a proper security pass. Slower, and catches more."
	if _, ok := FuzzyMatch(text, "tendril"); ok {
		t.Error("\"tendril\" matched an unrelated ~120-character description -- the span bound did not fire")
	}
}

func TestFuzzyMatchRanksAnEarlyMatchAboveALateOne(t *testing.T) {
	early, ok := FuzzyMatch("prism review tool for code", "prism")
	if !ok {
		t.Fatal("early case: want a match")
	}
	late, ok := FuzzyMatch("a code review tool people sometimes call prism", "prism")
	if !ok {
		t.Fatal("late case: want a match")
	}
	if early <= late {
		t.Errorf("early-match score %d, late-match score %d -- want the early match strictly higher", early, late)
	}
}
