package capverify

import "math"

// Nonnegative diagnostic totals are represented exactly up to MaxInt64.
// Saturated is sticky and means the mathematical total is strictly greater
// than MaxInt64; equality alone must not be marked saturated.
func legacyAdd(a int64, aSaturated bool, b int64, bSaturated bool) (int64, bool) {
	if aSaturated || bSaturated || b > math.MaxInt64-a {
		return math.MaxInt64, true
	}
	return a + b, false
}

// Evaluate the whole expression without overflowing an intermediate. The zero
// rule factor is special: an arbitrarily large stop term is then irrelevant.
func legacyRuleCost(rules, stops, trips int64) (int64, bool) {
	if rules == 0 {
		return trips, false
	}
	if stops > (math.MaxInt64-1)/4 {
		return math.MaxInt64, true
	}
	factor := 4*stops + 1
	if rules > (math.MaxInt64-trips)/factor {
		return math.MaxInt64, true
	}
	return rules*factor + trips, false
}

func (s *ProbeStats) addLegacy(amount int64, saturated bool) {
	s.LegacyScans, s.LegacySaturated = legacyAdd(s.LegacyScans, s.LegacySaturated, amount, saturated)
	if s.LegacySaturated {
		s.LegacyComplete = false
	}
}

// AddProjectionScans carries enumeration's legacy diagnostic into the outer
// certificate's actual, separately charged Frontier work. It affects no budget,
// transition, state, word, cancellation check or acceptance decision.
func (s *ProbeStats) AddProjectionScans(work int) {
	s.addLegacy(int64(work), false)
}
