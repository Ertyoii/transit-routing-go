package tripbased_test

import (
	"context"
	"encoding/json"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"reflect"
	"sync"
	"testing"
)

func fixedPatternConfig(d model.Dataset, q model.Query) tripbased.DestinationCappedPatternConfig {
	return tripbased.DestinationCappedPatternConfig{Patterns: rulePatternConfig(), Destination: q.Destination, Walking: *q.Walking, ProfileMaxLegs: 1024, ProfileMaxWalkingSeconds: 1000, MaxProfileSources: 100, MaxProfileQueries: 10000, MaxProfileSegments: 1000000}
}
func checkFixedGenerated(t testing.TB, seed uint64) {
	t.Helper()
	d, q := generated(seed)
	q.TransferBuffer, q.AccessibleOnly, q.MaxWalkSeconds, q.MaxLegs = 0, false, 1000, 128
	ms, _ := fullWords(expandedRuleSpec(d), q, 7)
	want := objectives(ms, q.MaxBoardings)
	x, e := tripbased.BuildDestinationCappedPatterns(context.Background(), d, fixedPatternConfig(d, q))
	if e != nil {
		t.Fatal(seed, e)
	}
	r, e := x.Search(context.Background(), q)
	if e != nil || !r.Complete {
		t.Fatal(seed, e, r)
	}
	got := objectives(reconstructed(t, expandedRuleSpec(d), q, r.Journeys), q.MaxBoardings)
	if !reflect.DeepEqual(got, want) {
		t.Fatal(seed, got, want, d, q)
	}
	for _, w := range x.Snapshot().Patterns.Witnesses {
		wq := q
		wq.Origin, wq.Destination, wq.Departure = w.Source, w.Target, w.Departure
		wq.MaxBoardings = 4
		wq.MaxLegs = 1024
		wq.LatestArrival = model.MaxServiceSecond
		reconstructed(t, expandedRuleSpec(d), wq, []model.Journey{w.Journey})
	}
}
func TestFixedCapPatternsGenerated(t *testing.T) {
	for seed := uint64(0); seed < 320; seed++ {
		checkFixedGenerated(t, seed)
	}
}
func FuzzFixedCapPatterns(f *testing.F) {
	for _, seed := range []uint64{0, 1, 71, 319} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { checkFixedGenerated(t, seed) })
}

func TestFixedCapPatternsDetourLateWindowAndOwnership(t *testing.T) {
	d, q := roleDetourData()
	for i := range d.Connections {
		d.Connections[i].Departure += 90000
		d.Connections[i].Arrival += 90000
	}
	d.Window = &model.TimeWindow{Start: 89990, End: 90020}
	q.Departure, q.LatestArrival, q.MaxWalkSeconds = 90000, 90020, 1000
	c := fixedPatternConfig(d, q)
	beforeInput, _ := json.Marshal(d)
	x, e := tripbased.BuildDestinationCappedPatterns(context.Background(), d, c)
	if e != nil {
		t.Fatal(e)
	}
	afterInput, _ := json.Marshal(d)
	if string(beforeInput) != string(afterInput) {
		t.Fatal("conditioning mutated input")
	}
	for _, a := range d.Stops {
		for _, at := range []int64{89990, 90000, 90001, 90004, 90008, 90020} {
			v := q
			v.Origin, v.Departure = a.ID, at
			ms, _ := prefixSpec(t, d, v)
			want := objectives(ms, v.MaxBoardings)
			r, e := x.Search(context.Background(), v)
			if e != nil || !r.Complete {
				t.Fatal(e, v)
			}
			if got := objectives(reconstructed(t, d, v, r.Journeys), v.MaxBoardings); !reflect.DeepEqual(got, want) {
				t.Fatal(v, got, want)
			}
		}
	}
	before, _ := json.Marshal(x.Snapshot())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := x.Search(context.Background(), q)
			if e != nil || !r.Complete {
				t.Error(e, r)
			}
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(x.Snapshot())
	if string(before) != string(after) {
		t.Fatal("query mutated prepared index")
	}
	s := x.Snapshot()
	s.Config.Walking.Transfer = 0
	if len(s.Patterns.DAGs) > 0 && len(s.Patterns.DAGs[0].Nodes) > 0 {
		s.Patterns.DAGs[0].Nodes[0].Stop = "mutated"
	}
	owned, _ := json.Marshal(x.Snapshot())
	if string(before) != string(owned) {
		t.Fatal("snapshot aliases index")
	}
}

func TestFixedCapPatternsParentRulesAndLoopPermissions(t *testing.T) {
	for _, kind := range []int{2, 3} {
		d, q := roleDetourData()
		q.MaxWalkSeconds = 1000
		d.Stops = append(d.Stops, model.Stop{ID: "PB", LocationType: 1, Accessible: model.Yes}, model.Stop{ID: "PC", LocationType: 1, Accessible: model.Yes})
		for i := range d.Stops {
			if d.Stops[i].ID == "B" {
				d.Stops[i].ParentStation = "PB"
			}
			if d.Stops[i].ID == "C" {
				d.Stops[i].ParentStation = "PC"
			}
		}
		d.Transfers = []model.TransferRule{{ID: "parent", FromStop: "PB", ToStop: "PC", Type: kind}}
		if kind == 2 {
			d.Transfers[0].MinSeconds = 3
		}
		x, e := tripbased.BuildDestinationCappedPatterns(context.Background(), d, fixedPatternConfig(d, q))
		if e != nil {
			t.Fatal(e)
		}
		ms, _ := prefixSpec(t, d, q)
		r, e := x.Search(context.Background(), q)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(objectives(reconstructed(t, expandedRuleSpec(d), q, r.Journeys), q.MaxBoardings), objectives(ms, q.MaxBoardings)) {
			t.Fatal(kind, r, ms)
		}
	}
	for _, firstDropoff := range []bool{false, true} {
		d := data()
		trip(&d, "loop", []string{"A", "B", "D", "C", "D"}, 90000, 90001, 90002, 90003, 90004, 90005, 90006, 90007)
		d.Connections[1].Pickup = false
		d.Connections[2].Pickup = false
		d.Connections[3].Pickup = false
		d.Connections[1].Dropoff = firstDropoff
		d.Transfers = []model.TransferRule{{ID: "no-reboard", FromStop: "B", ToStop: "B", Type: 3}}
		d.Window = &model.TimeWindow{Start: 89990, End: 90020}
		q := query()
		q.Departure, q.LatestArrival, q.MaxBoardings, q.MaxWalkSeconds, q.MaxLegs = 89990, 90020, 1, 6000, 128
		q.Walking = &model.WalkingLimits{Transfer: 900}
		cfg := fixedPatternConfig(d, q)
		cfg.ProfileMaxWalkingSeconds = 6000
		x, e := tripbased.BuildDestinationCappedPatterns(context.Background(), d, cfg)
		if e != nil {
			t.Fatal(e)
		}
		ms, _ := prefixSpec(t, d, q)
		r, e := x.Search(context.Background(), q)
		if e != nil {
			t.Fatal(e)
		}
		if !reflect.DeepEqual(objectives(reconstructed(t, d, q, r.Journeys), q.MaxBoardings), objectives(ms, q.MaxBoardings)) {
			t.Fatal(firstDropoff, r, ms)
		}
	}
}
