package tripbased_test

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/internal/capverify"
	"github.com/ertyoii/transit-routing-go/model"
)

// Whole-word validity predates both kernels. Exhaust all words of length <=4
// in this acyclic literal four-edge model; it cannot contain a longer path.
func TestTREXFixedTransferCapIndependentWholeWords(t *testing.T) {
	raw, e := os.ReadFile("testdata/trex_fixed_transfer_cap/rank-loss.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Dataset  model.Dataset `json:"dataset"`
		Query    model.Query   `json:"query"`
		Expected model.Journey `json:"expected"`
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	prepared, _, e := capverify.PrepareRaw(context.Background(), f.Dataset, capverify.PreparationLimits{MaxRows: 100, MaxWork: 100, MaxPayloadBytes: 10000})
	if e != nil {
		t.Fatal(e)
	}
	got, e := prepared.Enumerate(context.Background(), f.Query, capverify.Limits{MaxStates: 100, MaxJourneys: 100, MaxScans: 10000})
	if e != nil || !got.Complete {
		t.Fatal(got, e)
	}
	var spec []model.Journey
	var generate func([]int)
	generate = func(word []int) {
		if journey, _, ok := assess(f.Dataset, f.Query, word); ok {
			spec = append(spec, journey)
		}
		if len(word) == 4 {
			return
		}
		for token := 0; token < len(f.Dataset.Connections)+len(f.Dataset.Footpaths); token++ {
			generate(append(append([]int{}, word...), token))
		}
	}
	generate(nil)
	if !reflect.DeepEqual(spec, []model.Journey{f.Expected}) || !reflect.DeepEqual(got.Journeys, spec) {
		t.Fatal("independent whole-word specification", spec, got)
	}
}
