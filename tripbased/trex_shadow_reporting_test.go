package tripbased

import (
	"encoding/json"
	"math"
	"math/big"
	"testing"
)

func TestTREXFixedTransferCapShadowProjectionReporting(t *testing.T) {
	for _, test := range []struct {
		name       string
		initial    int64
		saturated  bool
		projection []int
	}{
		{"exact_max", math.MaxInt64 - 2, false, []int{1, 1}},
		{"projection_overflow", math.MaxInt64 - 1, false, []int{1, 1}},
		{"incoming_saturation", math.MaxInt64, true, []int{0, 1}},
		{"ordinary", 30, false, []int{12, 12}},
	} {
		t.Run(test.name, func(t *testing.T) {
			out := TREXFixedTransferCapCandidateResult{VerificationComplete: true, ShadowLegacyScans: test.initial, ShadowLegacySaturated: test.saturated}
			limit := int(^uint(0) >> 1)
			total := big.NewInt(test.initial)
			if test.saturated {
				total.Add(total, big.NewInt(1))
			}
			for i, work := range test.projection {
				total.Add(total, big.NewInt(int64(work)))
				out.addProjectionShadow(work, limit, i == len(test.projection)-1)
				saturated := total.Cmp(big.NewInt(math.MaxInt64)) > 0
				want := int64(math.MaxInt64)
				if !saturated {
					want = total.Int64()
				}
				if out.ShadowLegacyScans != want || out.ShadowLegacySaturated != saturated || out.ShadowLegacyWouldDecline != (saturated || want > int64(limit)) || out.ShadowLegacyComplete != (i == len(test.projection)-1 && !saturated) {
					t.Fatal("outer projection mathematical count/status differs", total, out)
				}
			}
			encoded, e := json.Marshal(out)
			if e != nil {
				t.Fatal(e)
			}
			var metadata map[string]json.RawMessage
			if e = json.Unmarshal(encoded, &metadata); e != nil {
				t.Fatal(e)
			}
			if string(metadata["shadow_legacy_saturated"]) != map[bool]string{false: "false", true: "true"}[out.ShadowLegacySaturated] {
				t.Fatal("saturation status missing from public metadata", string(encoded))
			}
		})
	}
	// A completed projection cannot claim a completed enumeration scope.
	out := TREXFixedTransferCapCandidateResult{ShadowLegacyScans: 7}
	out.addProjectionShadow(3, 9, true)
	if out.ShadowLegacyComplete || !out.ShadowLegacyWouldDecline || out.ShadowLegacySaturated {
		t.Fatal("scope/ordinary budget status", out)
	}
}
