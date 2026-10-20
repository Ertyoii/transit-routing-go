package tripbased_test

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func patternConfig() tripbased.PatternConfig {
	return tripbased.PatternConfig{Profile: flashConfig(1), MaxPatterns: 10000, MaxNodes: 10000, MaxEdges: 10000, MaxDirectEntries: 10000, MaxBuildWork: 1000000, MaxQueryWork: 100000, MaxQueryLabels: 10000}
}
func patternCompare(t testing.TB, d model.Dataset, x *tripbased.PatternIndex, q model.Query, want []int64) {
	t.Helper()
	r, e := x.Search(context.Background(), q)
	if e != nil || !r.Complete {
		t.Fatalf("TP %v data=%+v q=%+v", e, d, q)
	}
	got := objectives(reconstructed(t, d, q, r.Journeys), q.MaxBoardings)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("TP want%v got%v data=%+v q=%+v result=%+v", want, got, d, q, r)
	}
}
func TestPatternsIndependentWholeWords(t *testing.T) {
	words := 0
	patterns := 0
	for seed := uint64(0); seed < 320; seed++ {
		d, q := generated(seed)
		d.Transfers = nil
		for i := range d.Connections {
			d.Connections[i].Pickup = true
			d.Connections[i].Dropoff = true
		}
		q = flashQuery(q)
		ms, n := fullWords(d, q, 7)
		words += n
		x, e := tripbased.BuildPatterns(context.Background(), d, patternConfig())
		if e != nil {
			t.Fatalf("seed%d: %v data=%+v", seed, e, d)
		}
		patternCompare(t, d, x, q, objectives(ms, q.MaxBoardings))
		snap := x.Snapshot()
		patterns += snap.Stats.Patterns
		for _, w := range snap.Witnesses {
			wq := flashQuery(query())
			wq.Origin = w.Source
			wq.Destination = w.Target
			wq.Departure = w.Departure
			wq.MaxBoardings = 4
			reconstructed(t, d, wq, []model.Journey{w.Journey})
		}
	}
	t.Logf("320 whole-word universes; %d unpruned words; %d complete-pattern witnesses", words, patterns)
}
func TestPatternsLongIntermediateAndDisconnected(t *testing.T) {
	for _, intermediate := range []bool{false, true} {
		d := data()
		if intermediate {
			trip(&d, "in", []string{"A", "B"}, 0, 1)
			trip(&d, "out", []string{"C", "D"}, 752, 753)
		} else {
			trip(&d, "direct", []string{"A", "D"}, 0, 1)
		}
		d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 750, Accessible: model.Yes}}
		q := query()
		q.LatestArrival = 800
		q.MaxWalkSeconds = 10000
		q.MaxLegs = 128
		q.Walking = &model.WalkingLimits{Access: 600, Egress: 600, Direct: 600, Transfer: 750}
		ms, _ := fullWords(d, q, 4)
		x, e := tripbased.BuildPatterns(context.Background(), d, patternConfig())
		if e != nil {
			t.Fatal(e)
		}
		patternCompare(t, d, x, q, objectives(ms, q.MaxBoardings))
	}
}
func FuzzPatternsIndependent(f *testing.F) {
	for _, s := range []uint64{0, 1, 71, 319} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		d, q := generated(seed)
		d.Transfers = nil
		for i := range d.Connections {
			d.Connections[i].Pickup = true
			d.Connections[i].Dropoff = true
		}
		q = flashQuery(q)
		ms, _ := fullWords(d, q, 7)
		x, e := tripbased.BuildPatterns(context.Background(), d, patternConfig())
		if e != nil {
			t.Fatal(e)
		}
		patternCompare(t, d, x, q, objectives(ms, q.MaxBoardings))
	})
}

func patternsFixture() (model.Dataset, model.Query) {
	d := data()
	trip(&d, "early", []string{"A", "B"}, 0, 1)
	trip(&d, "late", []string{"A", "B"}, 3, 4)
	trip(&d, "out", []string{"B", "D"}, 5, 6)
	trip(&d, "direct", []string{"A", "D"}, 0, 20)
	return d, flashQuery(query())
}
func TestPatternsTwoBoardings(t *testing.T) {
	d, q := patternsFixture()
	x, e := tripbased.BuildPatterns(context.Background(), d, patternConfig())
	if e != nil {
		t.Fatal(e)
	}
	patternCompare(t, d, x, q, []int64{inf, 20, 6, 6})
}
func TestPatternsBudgetsAndCancellation(t *testing.T) {
	d, q := patternsFixture()
	for _, kind := range []string{"patterns", "nodes", "edges", "direct", "build", "profile"} {
		c := patternConfig()
		switch kind {
		case "patterns":
			c.MaxPatterns = 1
		case "nodes":
			c.MaxNodes = 1
		case "edges":
			c.MaxEdges = 1
		case "direct":
			c.MaxDirectEntries = 1
		case "build":
			c.MaxBuildWork = 1
		case "profile":
			c.Profile.Limits.MaxRuns = 1
		}
		x, e := tripbased.BuildPatterns(context.Background(), d, c)
		if e == nil || x.Complete() {
			t.Fatal("accepted partial preparation", kind)
		}
		r, e := x.Search(context.Background(), q)
		if e == nil || r.Complete || len(r.Journeys) > 0 {
			t.Fatal("partial index queried", kind)
		}
	}
	for _, kind := range []string{"query-work", "query-labels"} {
		c := patternConfig()
		if kind == "query-work" {
			c.MaxQueryWork = 1
		} else {
			c.MaxQueryLabels = 1
		}
		x, e := tripbased.BuildPatterns(context.Background(), d, c)
		if e != nil {
			t.Fatal(e)
		}
		r, e := x.Search(context.Background(), q)
		if e == nil || r.Complete || len(r.Journeys) > 0 {
			t.Fatal("query exhaustion accepted", kind)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	x, e := tripbased.BuildPatterns(ctx, d, patternConfig())
	if e == nil || x.Complete() {
		t.Fatal("cancelled preparation")
	}
	x, e = tripbased.BuildPatterns(context.Background(), d, patternConfig())
	if e != nil {
		t.Fatal(e)
	}
	for _, self := range []bool{false, true} {
		qq := q
		if self {
			qq.Destination = qq.Origin
		}
		r, e := x.Search(ctx, qq)
		if e == nil || r.Complete || len(r.Journeys) > 0 {
			t.Fatal("cancelled query")
		}
	}
	for _, kind := range []string{"buffer", "accessibility", "boards", "total", "legs"} {
		qq := q
		switch kind {
		case "buffer":
			qq.TransferBuffer = 300
		case "accessibility":
			qq.AccessibleOnly = true
		case "boards":
			qq.MaxBoardings = 5
		case "total":
			qq.MaxWalkSeconds = 0
		case "legs":
			qq.MaxLegs = 1
		}
		r, e := x.Search(context.Background(), qq)
		if e == nil || r.Complete {
			t.Fatal("unsupported query", kind)
		}
	}
}
func TestPatternsOrderTranslationOwnershipAndRebuild(t *testing.T) {
	d, q := patternsFixture()
	x, e := tripbased.BuildPatterns(context.Background(), d, patternConfig())
	if e != nil {
		t.Fatal(e)
	}
	original := x.Snapshot()
	queries := []model.Query{q, q, q, q}
	queries[1].Departure = 2
	queries[2].MaxBoardings = 1
	queries[3].LatestArrival = 5
	wants := make([][]int64, len(queries))
	raw := make([][]model.Journey, len(queries))
	for i, qq := range queries {
		ms, _ := fullWords(d, qq, 5)
		wants[i] = objectives(ms, qq.MaxBoardings)
		r, e := x.Search(context.Background(), qq)
		if e != nil {
			t.Fatal(e)
		}
		raw[i] = r.Journeys
		patternCompare(t, d, x, qq, wants[i])
	}
	for _, size := range []int{1, 2, 4} {
		for start := 0; start < len(queries); start += size {
			for i := min(len(queries), start+size) - 1; i >= start; i-- {
				r, e := x.Search(context.Background(), queries[i])
				if e != nil || !reflect.DeepEqual(r.Journeys, raw[i]) {
					t.Fatal("independent call grouping changed result", size, i, e)
				}
			}
		}
	}
	var wg sync.WaitGroup
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i, qq := range queries {
				r, e := x.Search(context.Background(), qq)
				if e != nil || !reflect.DeepEqual(r.Journeys, raw[i]) {
					t.Error("concurrent query", i, e)
				}
			}
		}()
	}
	wg.Wait()
	mutated := x.Snapshot()
	mutated.DAGs[0].Nodes[0].Stop = "changed"
	for target := range mutated.DAGs[0].Targets {
		delete(mutated.DAGs[0].Targets, target)
	}
	mutated.Witnesses[0].Journey.Legs[0].Ref = "changed"
	mutated.Witnesses[0].Edges[0].From = "changed"
	mutated.Backend.Lines[0][0] = "changed"
	if !reflect.DeepEqual(x.Snapshot(), original) {
		t.Fatal("snapshot alias")
	}
	slices.Reverse(d.Stops)
	slices.Reverse(d.Trips)
	slices.Reverse(d.Connections)
	y, e := tripbased.BuildPatterns(context.Background(), d, patternConfig())
	if e != nil {
		t.Fatal(e)
	}
	a, b := x.Snapshot(), y.Snapshot()
	a.PhasesNS = nil
	b.PhasesNS = nil
	a.Backend.PhasesNS = nil
	b.Backend.PhasesNS = nil
	if !reflect.DeepEqual(a, b) {
		t.Fatal("input permutation changed logical index")
	}
	for i := range d.Connections {
		d.Connections[i].Departure += 90000
		d.Connections[i].Arrival += 90000
	}
	z, e := tripbased.BuildPatterns(context.Background(), d, patternConfig())
	if e != nil {
		t.Fatal(e)
	}
	for i, qq := range queries {
		qq.Departure += 90000
		qq.LatestArrival += 90000
		want := append([]int64{}, wants[i]...)
		for j := range want {
			if want[j] < inf {
				want[j] += 90000
			}
		}
		patternCompare(t, d, z, qq, want)
	}
	// The prior index owns the old timetable; changed data requires a rebuild.
	old, oldq := patternsFixture()
	patternCompare(t, old, x, oldq, wants[0])
	for i := range old.Connections {
		if old.Connections[i].TripID == "direct" {
			old.Connections[i].Arrival = 2
		}
	}
	fresh, e := tripbased.BuildPatterns(context.Background(), old, patternConfig())
	if e != nil {
		t.Fatal(e)
	}
	patternCompare(t, old, fresh, oldq, []int64{inf, 2, 2, 2})
}
func TestPatternsWalkingOnlyAndTypedParallel(t *testing.T) {
	d := data()
	d.Footpaths = []model.Footpath{{ID: "ab", From: "A", To: "B", Duration: 2, Accessible: model.Yes}, {ID: "bd", From: "B", To: "D", Duration: 2, Accessible: model.Yes}}
	q := flashQuery(query())
	q.Departure = 90000
	q.LatestArrival = 90010
	q.MaxBoardings = 0
	x, e := tripbased.BuildPatterns(context.Background(), d, patternConfig())
	if e != nil {
		t.Fatal(e)
	}
	patternCompare(t, d, x, q, []int64{90004})
	trip(&d, "faster", []string{"A", "D"}, 0, 1)
	q.Departure = 0
	q.LatestArrival = 10
	q.MaxBoardings = 3
	x, e = tripbased.BuildPatterns(context.Background(), d, patternConfig())
	if e != nil {
		t.Fatal(e)
	}
	patternCompare(t, d, x, q, []int64{4, 1, 1, 1})
	r, e := x.Search(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	walk, ride := false, false
	for _, edge := range r.Graph {
		if edge.From == "A" && edge.To == "D" {
			walk = walk || edge.Kind == "walk"
			ride = ride || edge.Kind == "ride"
		}
	}
	if !walk || !ride {
		t.Fatal("parallel modes conflated", r.Graph)
	}
}
