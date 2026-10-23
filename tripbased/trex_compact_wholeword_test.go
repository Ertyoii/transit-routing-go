package tripbased_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
)

// The independently authored assess function generates every raw word of this
// finite acyclic four-edge model. Compare the actual ranked fixed-cap router,
// not just its verifier, with the resulting complete literal itinerary set.
func TestTREXCompactRankedIndependentWholeWords(t *testing.T) {
	raw, e := os.ReadFile("testdata/trex_fixed_transfer_cap/rank-loss.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Dataset   model.Dataset  `json:"dataset"`
		Query     model.Query    `json:"query"`
		Partition map[string]int `json:"partition"`
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	var spec []model.Journey
	var generate func([]int)
	generate = func(word []int) {
		if j, _, ok := assess(f.Dataset, f.Query, word); ok {
			spec = append(spec, j)
		}
		if len(word) == 4 {
			return
		}
		for token := 0; token < len(f.Dataset.Connections)+len(f.Dataset.Footpaths); token++ {
			generate(append(append([]int{}, word...), token))
		}
	}
	generate(nil)
	if len(spec) != 1 {
		t.Fatal("literal model changed", spec)
	}
	for _, exact := range []bool{false, true} {
		cfg := tripbased.TREXConfig{ExactStorage: exact, Levels: 1, MaxBoardings: 4, Partition: f.Partition, Limits: tripbased.TREXLimits{MaxStops: 100, MaxTrips: 100, MaxInputEdges: 1000, MaxClosurePaths: 1000, MaxWalkSettled: 1000, MaxTransfers: 1000, MaxRuns: 1000, MaxWork: 100000, MaxWitnesses: 1000, MaxOverlayEntries: 10000, MaxQueuedSegments: 10000, MaxQueryWork: 100000}}
		x, e := tripbased.BuildTREXFixedTransferCapRankedCandidate(context.Background(), f.Dataset, cfg, f.Query.Walking.Transfer)
		if e != nil {
			t.Fatal(e)
		}
		gate, _, e := tripbased.PrepareTREXFixedTransferCapCandidate(context.Background(), x, tripbased.TREXStaticCapVerifyLimits{MaxStates: 10000, MaxJourneys: 1000, MaxScans: 100000}, tripbased.TREXRawVerifierPreparationLimits{MaxRows: 1000, MaxWork: 2000, MaxPayloadBytes: 100000})
		if e != nil {
			t.Fatal(e)
		}
		got, e := gate.Search(context.Background(), f.Query)
		if e != nil || !got.Complete || !got.Certificate.Accepted || !reflect.DeepEqual(got.Journeys, spec) {
			t.Fatal("compact ranked whole-word mismatch", exact, got, e, spec)
		}
	}
}
