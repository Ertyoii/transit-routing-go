package tripbased_test

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
)

func walkingScopeConfig() tripbased.TREXConfig {
	return tripbased.TREXConfig{Levels: 0, MaxBoardings: 3, Limits: tripbased.TREXLimits{
		MaxStops: 100, MaxTrips: 100, MaxInputEdges: 1000, MaxClosurePaths: 1000,
		MaxWalkSettled: 10000, MaxTransfers: 1000, MaxRuns: 1000, MaxWork: 1000000,
		MaxWitnesses: 1000, MaxOverlayEntries: 10000, MaxQueuedSegments: 10000, MaxQueryWork: 1000000,
	}}
}

func walkingScopeFixture() (model.Dataset, model.Query) {
	d := data()
	for _, name := range []string{"O", "T", "Z", "W", "tail"} {
		d.Stops = append(d.Stops, model.Stop{ID: name, Accessible: model.Yes})
	}
	d.Window = &model.TimeWindow{Start: 0, End: 100}
	trip(&d, "first", []string{"A", "B"}, 10, 12)
	trip(&d, "second", []string{"C", "D"}, 16, 18)
	for _, e := range []struct {
		id, from, to string
		seconds      int64
	}{
		{"ox", "O", "X", 3}, {"xa", "X", "A", 3},
		{"by", "B", "Y", 2}, {"yc", "Y", "C", 2},
		{"dz", "D", "Z", 3}, {"zt", "Z", "T", 3},
		{"ow", "O", "W", 20}, {"wt", "W", "T", 20},
		{"tail", "A", "tail", 1000},
	} {
		d.Footpaths = append(d.Footpaths, model.Footpath{ID: e.id, From: e.from, To: e.to, Duration: e.seconds, Accessible: model.Yes})
	}
	q := query()
	q.Origin, q.Destination, q.MaxBoardings, q.MaxWalkSeconds, q.MaxLegs = "O", "T", 2, 1000, 128
	q.Walking = &model.WalkingLimits{Access: 6, Transfer: 4, Egress: 6, Direct: 40}
	return d, q
}

// Prefix enumeration is physically contiguous raw-edge enumeration followed by
// the independently authored whole-word assessment. No production walking,
// transfer graph or router is involved in the expected frontier or replay.
func compareWalkingScope(t *testing.T, d model.Dataset, q model.Query, want []int64, ranked bool) {
	t.Helper()
	ctx := context.Background()
	metrics, _ := prefixSpec(t, d, q)
	if independent := objectives(metrics, q.MaxBoardings); !reflect.DeepEqual(independent, want) {
		t.Fatal("literal/spec disagreement", independent, want)
	}
	bounded, e := tripbased.BuildBoundedWalkingTB(ctx, d, q.Destination, q.Walking.Transfer, walkingScopeConfig())
	if e != nil {
		t.Fatal(e)
	}
	before, _ := json.Marshal(bounded.Snapshot())
	r, e := bounded.Search(ctx, q)
	if e != nil || !r.Complete || r.Stats.EndpointWalkWork == 0 {
		t.Fatal("bounded endpoint query failed/unaccounted", r, e)
	}
	if got := objectives(reconstructed(t, d, q, r.Journeys), q.MaxBoardings); !reflect.DeepEqual(got, want) {
		t.Fatal("bounded frontier", got, want)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, e := bounded.Search(ctx, q); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(bounded.Snapshot())
	if string(before) != string(after) {
		t.Fatal("lazy endpoint walks mutated index")
	}
	for _, retained := range []bool{false, true} {
		pc := fixedPatternConfig(d, q)
		var p *tripbased.DestinationCappedPatternIndex
		if retained {
			p, e = tripbased.BuildDestinationRetainedCappedPatterns(ctx, d, tripbased.DestinationRetainedCappedConfig{Scope: pc, MaxPendingSegments: 10000, MaxSourceSeeds: 10000})
		} else {
			p, e = tripbased.BuildDestinationCappedPatterns(ctx, d, pc)
		}
		if e != nil {
			t.Fatal(retained, e)
		}
		r, e := p.Search(ctx, q)
		if e != nil || !r.Complete {
			t.Fatal(retained, e, r)
		}
		if got := objectives(reconstructed(t, d, q, r.Journeys), q.MaxBoardings); !reflect.DeepEqual(got, want) {
			t.Fatal("TP frontier", retained, got, want)
		}
		base := p.Snapshot().Patterns.Backend
		if base.WalkingClosureLimit == nil || *base.WalkingClosureLimit != max(q.Walking.Access, q.Walking.Transfer, q.Walking.Egress, q.Walking.Direct) || base.ClosureDiameter > *base.WalkingClosureLimit {
			t.Fatal("TP closure radius omitted/misreported", base)
		}
	}
	domain, e := tripbased.BuildDestinationSourceDomainPatterns(ctx, d, tripbased.DestinationSourceDomainConfig{
		Retained: tripbased.DestinationRetainedCappedConfig{Scope: fixedPatternConfig(d, q), MaxPendingSegments: 10000, MaxSourceSeeds: 10000},
		Sources:  []string{q.Origin},
	})
	if e != nil || !domain.DomainComplete() {
		t.Fatal("source-domain construction", e)
	}
	dr, e := domain.Search(ctx, q)
	if e != nil || !dr.Complete || !reflect.DeepEqual(objectives(reconstructed(t, d, q, dr.Journeys), q.MaxBoardings), want) {
		t.Fatal("source-domain frontier", dr, e)
	}
	if ranked {
		x, e := tripbased.BuildTREXFixedTransferCapRankedCandidate(ctx, d, walkingScopeConfig(), q.Walking.Transfer)
		if e != nil {
			t.Fatal(e)
		}
		gate, _, e := tripbased.PrepareTREXFixedTransferCapCandidate(ctx, x,
			tripbased.TREXStaticCapVerifyLimits{MaxStates: 10000, MaxJourneys: 10000, MaxScans: 1000000},
			tripbased.TREXRawVerifierPreparationLimits{MaxRows: 1000, MaxWork: 10000, MaxPayloadBytes: 1000000})
		if e != nil {
			t.Fatal(e)
		}
		r, e := gate.Search(ctx, q)
		if e != nil || !r.Complete || !r.Certificate.Accepted || !r.VerificationComplete {
			t.Fatal("fixed-ranked gate", r, e)
		}
		if got := objectives(reconstructed(t, d, q, r.Journeys), q.MaxBoardings); !reflect.DeepEqual(got, want) {
			t.Fatal("fixed-ranked frontier", got, want)
		}
	}
}

func TestWalkingScopeIndependentRoleSeparation(t *testing.T) {
	d, q := walkingScopeFixture()
	t.Run("larger_access_egress_direct", func(t *testing.T) {
		compareWalkingScope(t, d, q, []int64{40, 40, 24}, true)
	})
	t.Run("cannot_split_transfer_block", func(t *testing.T) {
		q := q
		w := *q.Walking
		w.Transfer = 3 // each raw edge costs 2, but the one block costs 4
		q.Walking = &w
		compareWalkingScope(t, d, q, []int64{40, 40, 40}, true)
	})
	t.Run("directed_asymmetry", func(t *testing.T) {
		q := q
		q.Origin, q.Destination = "T", "O"
		compareWalkingScope(t, d, q, []int64{inf, inf, inf}, true)
	})
	t.Run("zero_radius_self_paths", func(t *testing.T) {
		q := q
		q.Origin, q.Destination = "A", "B"
		q.Walking = &model.WalkingLimits{}
		compareWalkingScope(t, d, q, []int64{inf, 12, 12}, true)
	})
}

func TestWalkingScopeIndependentAbsorbingDetour(t *testing.T) {
	d := data()
	d.Window = &model.TimeWindow{Start: 0, End: 100}
	trip(&d, "first", []string{"A", "B"}, 0, 1)
	trip(&d, "second", []string{"C", "D"}, 5, 6)
	for _, edge := range []model.Footpath{
		{ID: "bd", From: "B", To: "D", Duration: 1, Accessible: model.Yes},
		{ID: "dc", From: "D", To: "C", Duration: 1, Accessible: model.Yes},
		{ID: "bx", From: "B", To: "X", Duration: 2, Accessible: model.Yes},
		{ID: "xc", From: "X", To: "C", Duration: 2, Accessible: model.Yes},
	} {
		d.Footpaths = append(d.Footpaths, edge)
	}
	q := query()
	q.MaxBoardings, q.MaxWalkSeconds, q.MaxLegs = 2, 1000, 128
	q.Walking = &model.WalkingLimits{Transfer: 4}
	// The unrestricted B-D-C shortcut is invalid: D is terminal. The retained
	// path must be the longer B-X-C block, exactly on the transfer radius.
	compareWalkingScope(t, d, q, []int64{inf, inf, 6}, false)
}
