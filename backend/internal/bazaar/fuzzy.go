package bazaar

import "strings"

// maxFuzzySpanSlack bounds how far a match may spread across text relative
// to the query's own length: reject unless (last match index - first match
// index) <= len(query)*maxFuzzySpanSlack + maxFuzzySpanConst. Pure
// unbounded subsequence matching (no span limit at all) is what a caught-in-
// review bug against a real fixture exposed: "tendril" — 7 runes — fuzzy-
// "matched" a completely unrelated ~120-character Prism endpoint
// description, because SOME occurrence of t, then e, then n, then d, then r,
// then i, then l existed somewhere in that much text, in that order, purely
// by chance. A real typo-tolerant match (like "gplausible" for
// "GoPlausible") stays close to the query's own length; only a spurious one
// needs to wander across an entire sentence to complete.
const (
	maxFuzzySpanSlack = 3
	maxFuzzySpanConst = 4
)

// FuzzyMatch reports whether every rune of query appears in text, in order
// -- a SUBSEQUENCE match, not a substring match, so "gplausible" still
// matches "GoPlausible" despite the missing 'o' and the case difference, the
// way a typed-ahead fuzzy finder (fzf, VSCode's "quick open") behaves. Bounded
// by maxFuzzySpanSlack/Const so this stays a typo-tolerant near-match rather
// than "these letters occur somewhere, in order, in this entire paragraph" --
// see that constant's doc comment for the false positive this closes.
//
// When ok is true, score ranks how GOOD the match is; higher is better.
// Two things are rewarded: runs of consecutive matched runes (a query typed
// as one word should rank a text containing it as one contiguous word above
// a text where the same letters merely happen to be scattered through
// unrelated words), and an early match (a provider's own name, at the start
// of the haystack, matters more than an unrelated word buried in a long
// description). Ranking, not filtering, is the whole reason this exists over
// strings.Contains: a search that finds ten matches with no way to tell
// which one the user actually meant is not meaningfully more useful than no
// search at all.
//
// Tries every occurrence of the query's own first rune as a starting point,
// not just the first one in text, and keeps whichever attempt scores best.
// A single leftmost-greedy pass over "a tool people use for prism analysis"
// searching for "prism" would grab the 'p' out of "people" -- an unrelated
// word earlier in the string -- and never recover, dragging the match's span
// across everything in between and failing the bound above for a query that
// is RIGHT THERE, spelled out whole, later in the same text.
func FuzzyMatch(text, query string) (score int, ok bool) {
	if query == "" {
		return 0, true
	}
	t := []rune(strings.ToLower(text))
	q := []rune(strings.ToLower(query))

	best := 0
	found := false
	for start := 0; start < len(t); start++ {
		if t[start] != q[0] {
			continue
		}
		if s, matched := fuzzyMatchFrom(t, q, start); matched {
			if !found || s > best {
				best = s
				found = true
			}
		}
	}
	return best, found
}

// fuzzyMatchFrom runs one greedy subsequence match of q against t, forcing
// the first rune of q to match at exactly index start (the caller has
// already checked t[start] == q[0]). See FuzzyMatch's doc comment for why
// it is tried at every candidate start rather than only the leftmost.
func fuzzyMatchFrom(t, q []rune, start int) (score int, ok bool) {
	ti := start + 1
	lastMatch := start
	for _, qc := range q[1:] {
		found := false
		for ; ti < len(t); ti++ {
			if t[ti] == qc {
				found = true
				break
			}
		}
		if !found {
			return 0, false
		}
		if ti == lastMatch+1 {
			score += 3
		}
		lastMatch = ti
		ti++
	}
	if start < 20 {
		score += 1
	}
	if lastMatch-start+1 > len(q)*maxFuzzySpanSlack+maxFuzzySpanConst {
		return 0, false
	}
	return score, true
}
