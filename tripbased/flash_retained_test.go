package tripbased_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"github.com/ertyoii/transit-routing-go/validate"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"
)

func retainedConfig() tripbased.PatternConfig {
	c := rulePatternConfig()
	c.Profile.ProfilePolicy = "retained_exact_event_v1"
	c.Profile.Limits.MaxRetainedReadiness = 10000
	c.Profile.Limits.MaxRetainedRuns = 10000
	c.Profile.Limits.MaxRetainedSources = 100
	c.Profile.Limits.MaxRetainedQueued = 10000
	return c
}

func compareRetained(t testing.TB, d model.Dataset, qs []model.Query) {
	t.Helper()
	fresh, e := tripbased.BuildPatterns(context.Background(), d, rulePatternConfig())
	if e != nil {
		t.Fatal(e)
	}
	c := retainedConfig()
	// This old guard keeps its fresh-only meaning; retained policy declares
	// every readiness in the separate explicit guard and counter.
	c.Profile.Limits.MaxRuns = 1
	retained, e := tripbased.BuildPatterns(context.Background(), d, c)
	if e != nil {
		t.Fatal(e, d)
	}
	s := retained.Snapshot()
	if s.Profile != "basic_tp_static_stop_pair_retained_exact_event_nonbinding_v1" {
		t.Fatal(s.Profile)
	}
	if s.Backend.Stats.ProfileRuns != 0 || s.Backend.Stats.RetainedReadiness != fresh.Snapshot().Backend.Stats.ProfileRuns {
		t.Fatal("exact readiness accounting", s.Backend.Stats, fresh.Snapshot().Backend.Stats)
	}
	// Disjoint newly exposed intervals imply K*C bound per started source.
	if s.Backend.Stats.RetainedEvents > c.Profile.MaxBoardings*len(d.Connections)*s.Backend.Stats.RetainedSourcesStarted {
		t.Fatal("event bound violated", s.Backend.Stats)
	}
	checker, e := validate.New(d)
	if e != nil {
		t.Fatal(e)
	}
	for _, q := range qs {
		ms, _ := prefixSpec(t, d, q)
		want := objectives(ms, q.MaxBoardings)
		for name, x := range map[string]*tripbased.PatternIndex{"fresh": fresh, "retained": retained} {
			r, e := x.Search(context.Background(), q)
			if e != nil || !r.Complete {
				t.Fatal(name, e, q)
			}
			got := []model.Metrics{}
			for _, j := range r.Journeys {
				v, e := checker.Check(q, j)
				if e != nil {
					t.Fatal(name, e, j, q)
				}
				got = append(got, v.Metrics)
			}
			if !reflect.DeepEqual(objectives(got, q.MaxBoardings), want) {
				t.Fatalf("%s want%v got%v data%+v q%+v", name, want, objectives(got, q.MaxBoardings), d, q)
			}
		}
	}
	for _, w := range s.Witnesses {
		q := flashQuery(query())
		q.Origin, q.Destination, q.Departure, q.MaxBoardings = w.Source, w.Target, w.Departure, 4
		if d.Window != nil {
			q.LatestArrival = d.Window.End
		} else {
			q.LatestArrival = model.MaxServiceSecond
		}
		if _, e := checker.Check(q, w.Journey); e != nil {
			t.Fatal("invalid retained profile witness", e, w)
		}
	}
}

func TestRetainedProfilesGenerated(t *testing.T) {
	for seed := uint64(0); seed < 320; seed++ {
		d, q := generated(seed)
		q = flashQuery(q)
		compareRetained(t, d, []model.Query{q})
	}
}

func TestRetainedProfilesContextAndAllOD(t *testing.T) {
	d := data()
	trip(&d, "good", []string{"A", "B"}, 0, 2)
	trip(&d, "bad", []string{"A", "X"}, 1, 2)
	trip(&d, "early", []string{"C", "D"}, 8, 9)
	trip(&d, "late", []string{"C", "D"}, 110, 111)
	d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 2, Accessible: model.Yes}, {ID: "xc", From: "X", To: "C", Duration: 1, Accessible: model.Yes}}
	d.Transfers = []model.TransferRule{{ID: "good-min", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: 5}, {ID: "bad-min", FromStop: "X", ToStop: "C", Type: 2, MinSeconds: 100}}
	d.Window = &model.TimeWindow{End: 120}
	qs := []model.Query{}
	for _, a := range d.Stops {
		for _, b := range d.Stops {
			for _, at := range []int64{0, 1, 2, 7, 110, 112, 120} {
				q := flashQuery(query())
				q.Origin, q.Destination, q.Departure, q.LatestArrival = a.ID, b.ID, at, 120
				qs = append(qs, q)
			}
		}
	}
	compareRetained(t, d, qs)
}

func TestRetainedProfilesLoopParentAndWindow(t *testing.T) {
	d := data()
	trip(&d, "loop", []string{"A", "B", "C", "B", "D"}, 90000, 90001, 90002, 90003, 90004, 90005, 90006, 90007)
	trip(&d, "later", []string{"B", "D"}, 90008, 90009)
	d.Stops = append(d.Stops, model.Stop{ID: "parent", LocationType: 1, Accessible: model.Yes})
	d.Stops[1].ParentStation = "parent"
	d.Transfers = []model.TransferRule{{ID: "no-change", FromStop: "parent", ToStop: "parent", Type: 3}}
	d.Connections[1].Pickup = false
	d.Connections[3].Pickup = false
	d.Connections[0].Dropoff = false
	d.Footpaths = []model.Footpath{{ID: "xa", From: "X", To: "A", Duration: 2, Accessible: model.Yes}, {ID: "xb", From: "X", To: "B", Duration: 4, Accessible: model.Yes}}
	d.Window = &model.TimeWindow{Start: 89990, End: 90020}
	qs := []model.Query{}
	for _, a := range []string{"A", "B", "C", "X"} {
		for _, at := range []int64{89990, 89996, 90000, 90001, 90004, 90006, 90008, 90010} {
			q := flashQuery(query())
			q.Origin, q.Departure, q.LatestArrival = a, at, 90020
			qs = append(qs, q)
		}
	}
	compareRetained(t, d, qs)
}

func TestRetainedProfilesThreeBoardings(t *testing.T) {
	for seed := 0; seed < 32; seed++ {
		d := data()
		trip(&d, "first", []string{"A", "B"}, 0, 2)
		trip(&d, "second", []string{"C", "X"}, int64(4+seed%4), int64(6+seed%4))
		trip(&d, "third", []string{"Y", "D"}, int64(10+seed%7), int64(12+seed%7))
		d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 1, Accessible: model.Yes}, {ID: "xy", From: "X", To: "Y", Duration: 2, Accessible: model.Yes}}
		d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: int64(seed % 5)}}
		if seed%4 == 0 {
			d.Connections[1].Pickup = false
		}
		qs := []model.Query{}
		for _, at := range []int64{0, 1, 3, 10} {
			q := flashQuery(query())
			q.Departure = at
			qs = append(qs, q)
		}
		compareRetained(t, d, qs)
	}
}

func TestRetainedProfilesRandomDAGs(t *testing.T) {
	for seed := uint64(0); seed < 160; seed++ {
		rng := rand.New(rand.NewPCG(seed, seed+813))
		d := data()
		for i := 0; i < 6; i++ {
			a, b := rng.IntN(6), rng.IntN(5)
			if b >= a {
				b++
			}
			at := int64(rng.IntN(23))
			trip(&d, string(rune('a'+i)), []string{d.Stops[a].ID, d.Stops[b].ID}, at, at+1+int64(rng.IntN(5)))
			d.Connections[i].Pickup = rng.IntN(6) != 0
			d.Connections[i].Dropoff = rng.IntN(6) != 0
		}
		for a := 0; a < 6; a++ {
			for b := a + 1; b < 6; b++ {
				if rng.IntN(3) == 0 {
					d.Footpaths = append(d.Footpaths, model.Footpath{ID: d.Stops[a].ID + d.Stops[b].ID, From: d.Stops[a].ID, To: d.Stops[b].ID, Duration: 1 + int64(rng.IntN(5)), Accessible: model.Yes})
				}
			}
		}
		d.Transfers = []model.TransferRule{{ID: "rule", FromStop: d.Stops[rng.IntN(6)].ID, ToStop: d.Stops[rng.IntN(6)].ID, Type: 2, MinSeconds: int64(rng.IntN(10))}}
		qs := []model.Query{}
		for i := 0; i < 8; i++ {
			q := flashQuery(query())
			q.Origin, q.Destination, q.Departure = d.Stops[rng.IntN(6)].ID, d.Stops[rng.IntN(6)].ID, int64(rng.IntN(6))
			q.LatestArrival = 35
			qs = append(qs, q)
		}
		compareRetained(t, d, qs)
	}
}

func TestRetainedProfilesPartialWorkAndCancellation(t *testing.T) {
	d := snapshotFixture(true)
	c := retainedConfig()
	count := cancelAt(1 << 60)
	x, e := tripbased.BuildPatterns(count, d, c)
	checks := count.checks.Load()
	count.cancel()
	if e != nil {
		t.Fatal(e)
	}
	work := x.Snapshot().Backend.Stats.Work
	for budget := 1; budget < work; budget++ {
		limited := c
		limited.Profile.Limits.MaxWork = budget
		p, e := tripbased.BuildPatterns(context.Background(), d, limited)
		if e == nil || p.Complete() || p.Snapshot().Complete {
			t.Fatal(budget, e)
		}
		if r, e := p.Search(context.Background(), flashQuery(query())); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal(budget, r, e)
		}
	}
	for at := int64(1); at <= checks; at++ {
		ctx := cancelAt(at)
		p, e := tripbased.BuildPatterns(ctx, d, c)
		ctx.cancel()
		if !errors.Is(e, context.Canceled) || p.Complete() || p.Snapshot().Complete {
			t.Fatal(at, e)
		}
	}
	t.Logf("%d work-boundary failures and %d cancellation checkpoints fail closed", work-1, checks)
}

func TestRetainedProfilesExposeEachIntervalOnce(t *testing.T) {
	d := data()
	stops := []string{}
	for i := 0; i <= 16; i++ {
		id := fmt.Sprintf("chain-%02d", i)
		stops = append(stops, id)
		d.Stops = append(d.Stops, model.Stop{ID: id, Accessible: model.Yes})
		if i < 16 {
			d.Footpaths = append(d.Footpaths, model.Footpath{ID: "access-" + id, From: "X", To: id, Duration: 1, Accessible: model.Yes})
		}
	}
	times := []int64{}
	for i := 0; i < 16; i++ {
		times = append(times, int64(10+2*i), int64(11+2*i))
	}
	trip(&d, "chain", stops, times...)
	c := retainedConfig()
	c.Profile.RetainedSourceStart = "X"
	c.Profile.Limits.MaxRetainedSources = 1
	x, e := tripbased.BuildPatterns(context.Background(), d, c)
	if e == nil || x.Complete() {
		t.Fatal("bounded single-source stage must remain incomplete", e)
	}
	s := x.Snapshot()
	if s.Backend.Stats.RetainedSourcesCompleted != 1 || s.Backend.Stats.RetainedReadiness != 16 || s.Backend.Stats.RetainedEvents != 16 {
		t.Fatal("16 source thresholds must expose 16 disjoint events, not 136 suffix visits", s.Backend.Stats)
	}
	checker, e := validate.New(d)
	if e != nil {
		t.Fatal(e)
	}
	for _, w := range s.Witnesses {
		q := flashQuery(query())
		q.Origin, q.Destination, q.Departure, q.LatestArrival, q.MaxBoardings = w.Source, w.Target, w.Departure, 100, 4
		if _, e := checker.Check(q, w.Journey); e != nil {
			t.Fatal("replayed earlier readiness must keep physical access valid", e, w)
		}
	}
}

func TestRetainedProfilesGuardsOwnershipAndConcurrent(t *testing.T) {
	d := data()
	trip(&d, "a", []string{"A", "B"}, 0, 1)
	trip(&d, "b", []string{"B", "D"}, 2, 3)
	trip(&d, "c", []string{"A", "D"}, 4, 5)
	trip(&d, "same-ready-branch", []string{"A", "X"}, 0, 2)
	q := flashQuery(query())
	for _, mutate := range []func(*tripbased.PatternConfig){
		func(c *tripbased.PatternConfig) { c.Profile.Limits.MaxRetainedReadiness = 1 },
		func(c *tripbased.PatternConfig) { c.Profile.Limits.MaxRetainedRuns = 1 },
		func(c *tripbased.PatternConfig) { c.Profile.Limits.MaxRetainedSources = 1 },
		func(c *tripbased.PatternConfig) { c.Profile.Limits.MaxRetainedQueued = 1 },
		func(c *tripbased.PatternConfig) { c.Profile.Limits.MaxWork = 1 },
		func(c *tripbased.PatternConfig) { c.Profile.Limits.MaxRetainedSources = 0 },
		func(c *tripbased.PatternConfig) { c.Profile.ProfilePolicy = "unknown" },
		func(c *tripbased.PatternConfig) { c.Profile.RetainedSourceStart = "missing-stop" },
	} {
		c := retainedConfig()
		mutate(&c)
		x, e := tripbased.BuildPatterns(context.Background(), d, c)
		if e == nil || x.Complete() || x.Snapshot().Complete {
			t.Fatal("guard did not fail closed", e, x.Snapshot())
		}
		if r, e := x.Search(context.Background(), q); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("partial query answered", r, e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if x, e := tripbased.BuildPatterns(ctx, d, retainedConfig()); e == nil || x.Complete() {
		t.Fatal(e)
	}
	x, e := tripbased.BuildPatterns(context.Background(), d, retainedConfig())
	if e != nil {
		t.Fatal(e)
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
		t.Fatal("query mutation")
	}
	rotated := retainedConfig()
	rotated.Profile.RetainedSourceStart = "X"
	rindex, e := tripbased.BuildPatterns(context.Background(), d, rotated)
	if e != nil {
		t.Fatal(e)
	}
	a, e := x.Search(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	b, e := rindex.Search(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(objectives(reconstructed(t, d, q, a.Journeys), q.MaxBoardings), objectives(reconstructed(t, d, q, b.Journeys), q.MaxBoardings)) {
		t.Fatal("rotation changed result")
	}
	if rindex.Snapshot().Backend.Stats.RetainedReadiness != x.Snapshot().Backend.Stats.RetainedReadiness {
		t.Fatal("rotation dropped readiness")
	}
	c := retainedConfig()
	c.Profile.WalkingPolicy = "query_role_caps_v1"
	if x, e := tripbased.BuildPatterns(context.Background(), d, c); e == nil || x.Complete() {
		t.Fatal("retained policy does not certify role caps")
	}
}

func FuzzRetainedProfiles(f *testing.F) {
	for _, seed := range []uint64{0, 1, 71, 319} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		d, q := generated(seed)
		compareRetained(t, d, []model.Query{flashQuery(q)})
	})
}
