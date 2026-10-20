package capverify

import (
	"math"
	"math/big"
	"testing"
)

func legacyBigExpected(value *big.Int) (int64, bool) {
	if value.Cmp(big.NewInt(math.MaxInt64)) > 0 {
		return math.MaxInt64, true
	}
	return value.Int64(), false
}

func TestPreparedRawLegacyRuleCostBigReference(t *testing.T) {
	for _, test := range []struct {
		name                string
		rules, stops, trips int64
	}{
		{"zero_factor_huge_stops", 0, math.MaxInt64, 7},
		{"zero_factor_exact_max", 0, math.MaxInt64, math.MaxInt64},
		{"exact_max", 1, 0, math.MaxInt64 - 1},
		{"one_over", 1, 0, math.MaxInt64},
		{"factor_overflow", 1, math.MaxInt64, 0},
		{"factor_boundary", 1, (math.MaxInt64 - 1) / 4, 0},
		{"product_overflow", math.MaxInt64, 1, 0},
		{"trip_add_overflow", 2, 0, math.MaxInt64 - 1},
		{"ordinary", 17, 23, 31},
	} {
		t.Run(test.name, func(t *testing.T) {
			factor := new(big.Int).Add(new(big.Int).Mul(big.NewInt(test.stops), big.NewInt(4)), big.NewInt(1))
			value := new(big.Int).Add(new(big.Int).Mul(big.NewInt(test.rules), factor), big.NewInt(test.trips))
			want, saturated := legacyBigExpected(value)
			got, gotSaturated := legacyRuleCost(test.rules, test.stops, test.trips)
			if got != want || gotSaturated != saturated {
				t.Fatal("mathematical rule cost differs", value, got, gotSaturated, want, saturated)
			}
			// The same owning accumulation path must retain the product status.
			stats := ProbeStats{LegacyComplete: true}
			stats.addLegacy(got, gotSaturated)
			if stats.LegacyScans != want || stats.LegacySaturated != saturated || stats.LegacyComplete == saturated {
				t.Fatal("product status lost during accumulation", stats)
			}
		})
	}
}

func TestPreparedRawLegacyAccumulationBigReference(t *testing.T) {
	for _, test := range []struct {
		name  string
		parts []int64
	}{
		{"zero", []int64{0, 0}},
		{"exact_max", []int64{math.MaxInt64 - 2, 1, 1, 0}},
		{"one_over", []int64{math.MaxInt64 - 2, 1, 1, 1}},
		{"sticky", []int64{math.MaxInt64, 1, 0, 7}},
		{"two_large", []int64{math.MaxInt64, math.MaxInt64}},
	} {
		t.Run(test.name, func(t *testing.T) {
			stats := ProbeStats{LegacyComplete: true}
			total := new(big.Int)
			for _, part := range test.parts {
				total.Add(total, big.NewInt(part))
				stats.addLegacy(part, false)
				want, saturated := legacyBigExpected(total)
				if stats.LegacyScans != want || stats.LegacySaturated != saturated || stats.LegacyComplete == saturated {
					t.Fatal("cumulative mathematical count differs", total, stats)
				}
			}
		})
	}
	stats := ProbeStats{LegacyScans: math.MaxInt64, LegacySaturated: true}
	stats.AddProjectionScans(0)
	if !stats.LegacySaturated || stats.LegacyScans != math.MaxInt64 || stats.LegacyComplete {
		t.Fatal("incoming saturation lost", stats)
	}
}
