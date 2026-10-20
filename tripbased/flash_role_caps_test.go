package tripbased_test

import (
	"context"
	"encoding/json"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"math/rand/v2"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func roleConfig(cells int) tripbased.FlashConfig {
	c := ruleConfig(cells)
	c.WalkingPolicy = "query_role_caps_v1"
	return c
}

// The unrestricted B-D-C metric is 2; avoiding goal D takes B-X-C = 6.
// That detour exceeds the entire unrestricted metric's diameter (3), and
// requires retargeting the prepared C-D departure at 4 to departure 8.
func roleDetourData() (model.Dataset, model.Query) {
	d := data()
	trip(&d, "in", []string{"A", "B"}, 0, 1)
	trip(&d, "out-early", []string{"C", "D"}, 4, 5)
	trip(&d, "out-late", []string{"C", "D"}, 8, 9)
	trip(&d, "side", []string{"C", "Y"}, 4, 6)
	d.Footpaths = []model.Footpath{
		{ID: "bd", From: "B", To: "D", Duration: 1, Accessible: model.Yes},
		{ID: "dc", From: "D", To: "C", Duration: 1, Accessible: model.Yes},
		{ID: "bx", From: "B", To: "X", Duration: 3, Accessible: model.Yes},
		{ID: "xc", From: "X", To: "C", Duration: 3, Accessible: model.Yes},
	}
	d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: 3}}
	q := query()
	q.LatestArrival, q.MaxBoardings, q.MaxLegs, q.MaxWalkSeconds = 20, 2, 128, 6
	q.Walking = &model.WalkingLimits{Transfer: 6}
	return d, q
}

// Enumerate only physically contiguous, time-feasible raw prefixes; never use
// production dominance, transfer preparation, walking closure or search code.
// Complete itinerary semantics are assessed by the independent whole-word spec.
// Fixtures have acyclic positive walking graphs and positive fixed ride events;
// maxDepth = C + (C+1)*(S-1) exhausts them. Budget exhaustion fails this test.
func prefixSpec(t testing.TB, d model.Dataset, q model.Query) ([]model.Metrics, int) {
	t.Helper()
	d = expandedRuleSpec(d)
	maxDepth := len(d.Connections) + (len(d.Connections)+1)*(len(d.Stops)-1)
	visits := 0
	ms := []model.Metrics{}
	var walk func(string, int64, []int, bool)
	walk = func(stop string, at int64, word []int, alight bool) {
		visits++
		if visits > 10000 {
			t.Fatal("independent prefix enumeration budget exhausted")
		}
		if stop == q.Destination && alight {
			_, m, ok := assess(d, q, word)
			if ok {
				ms = append(ms, m)
			}
			return
		}
		if len(word) >= maxDepth {
			return
		}
		for i, c := range d.Connections {
			if c.From == stop && c.Departure >= at && c.Arrival <= q.LatestArrival {
				walk(c.To, c.Arrival, append(slices.Clone(word), i), c.Dropoff)
			}
		}
		for i, f := range d.Footpaths {
			if f.From == stop && at+f.Duration <= q.LatestArrival {
				walk(f.To, at+f.Duration, append(slices.Clone(word), len(d.Connections)+i), true)
			}
		}
	}
	walk(q.Origin, q.Departure, nil, true)
	return ms, visits
}

func TestFlashRoleCapsDetourAndProvenance(t *testing.T) {
	d, q := roleDetourData()
	x, e := tripbased.BuildFlash(context.Background(), d, roleConfig(6))
	if e != nil {
		t.Fatal(e)
	}
	s := x.Snapshot()
	if s.ClosureDiameter != 3 || s.Profile != "flash_tb_static_stop_pair_suffix_flags_role_caps_v1" {
		t.Fatal(s)
	}
	want := []int64{inf, inf, 9}
	ms, _ := prefixSpec(t, d, q)
	if !reflect.DeepEqual(objectives(ms, 2), want) {
		t.Fatal(ms)
	}
	flashCompare(t, d, x, q, want)
	r, e := x.Search(context.Background(), q)
	if e != nil || len(r.Journeys) != 1 || r.Stats.DestinationWalkRepairs == 0 || r.Stats.RepairTripSearches == 0 || r.Stats.FlagRejected == 0 {
		t.Fatal(e, r)
	}
	refs := []string{}
	for _, l := range r.Journeys[0].Legs {
		refs = append(refs, l.Ref)
	}
	if !reflect.DeepEqual(refs, []string{"in/0", "bx", "xc", "out-late/0"}) {
		t.Fatal(refs)
	}
	found := false
	for _, edge := range s.Transfers {
		if edge.FromTrip == "in" && edge.ToTrip == "out-early" && reflect.DeepEqual(edge.RawEdges, []string{"bd", "dc"}) {
			found = slices.Contains(edge.Cells, s.Partition.Cells["D"])
		}
	}
	if !found {
		t.Fatal("original early receiver edge must have target-cell flag before repair", s.Transfers)
	}
	q.Walking.Transfer, q.MaxWalkSeconds = 5, 5
	flashCompare(t, d, x, q, []int64{inf, inf, inf})
}

func TestFlashRoleCapsIndependentAllOD(t *testing.T) {
	d, base := roleDetourData()
	x, e := tripbased.BuildFlash(context.Background(), d, roleConfig(6))
	if e != nil {
		t.Fatal(e)
	}
	caps := []model.WalkingLimits{{}, {Transfer: 5}, {Transfer: 6}, {Access: 1, Egress: 1, Transfer: 6, Direct: 1}, {Access: 6, Egress: 6, Transfer: 6, Direct: 6}, {Access: 3, Egress: 0, Transfer: 2, Direct: 3}}
	cases, visits := 0, 0
	for _, a := range d.Stops {
		for _, b := range d.Stops {
			for _, at := range []int64{0, 1, 3, 7} {
				for _, limits := range caps {
					q := base
					q.Origin, q.Destination, q.Departure, q.Walking, q.MaxWalkSeconds = a.ID, b.ID, at, &limits, 1000
					before, _ := json.Marshal(q)
					ms, n := prefixSpec(t, d, q)
					flashCompare(t, d, x, q, objectives(ms, q.MaxBoardings))
					after, _ := json.Marshal(q)
					if string(before) != string(after) {
						t.Fatal("query mutated")
					}
					cases++
					visits += n
				}
			}
		}
	}
	t.Logf("%d all-OD/readiness/role-cap combinations; %d independent raw prefixes; 3 native controls", cases, visits)
}

func checkGeneratedRoleCaps(t testing.TB, seed uint64) {
	d, q := generated(seed)
	q.TransferBuffer, q.AccessibleOnly, q.MaxWalkSeconds, q.MaxLegs = 0, false, 1000, 128
	ms, _ := fullWords(expandedRuleSpec(d), q, 7)
	x, e := tripbased.BuildFlash(context.Background(), d, roleConfig(1+int(seed%3)))
	if e != nil {
		t.Fatal(e)
	}
	flashCompare(t, d, x, q, objectives(ms, q.MaxBoardings))
}
func TestFlashRoleCapsGenerated(t *testing.T) {
	for seed := uint64(0); seed < 320; seed++ {
		checkGeneratedRoleCaps(t, seed)
	}
}

func TestFlashRoleCapsThreeBoardings(t *testing.T) {
	d, q := roleDetourData()
	// Replace the receiving line's endpoint, then require a second transfer.
	for i := range d.Connections {
		if d.Connections[i].TripID == "out-early" || d.Connections[i].TripID == "out-late" {
			d.Connections[i].To = "X"
		}
	}
	d.Footpaths = append(d.Footpaths, model.Footpath{ID: "xy", From: "X", To: "Y", Duration: 3, Accessible: model.Yes})
	trip(&d, "third", []string{"Y", "D"}, 13, 14)
	d.Transfers = append(d.Transfers, model.TransferRule{ID: "no-skip", FromStop: "B", ToStop: "Y", Type: 3})
	q.MaxBoardings, q.MaxWalkSeconds = 3, 12
	x, e := tripbased.BuildFlash(context.Background(), d, roleConfig(6))
	if e != nil {
		t.Fatal(e)
	}
	ms, _ := prefixSpec(t, d, q)
	want := []int64{inf, inf, inf, 14}
	if !reflect.DeepEqual(objectives(ms, 3), want) {
		t.Fatal(ms)
	}
	flashCompare(t, d, x, q, want)
}

func TestFlashRoleCapsRandomDAGs(t *testing.T) {
	visits := 0
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
		x, e := tripbased.BuildFlash(context.Background(), d, roleConfig(1+int(seed%6)))
		if e != nil {
			t.Fatal(seed, e)
		}
		for i := 0; i < 8; i++ {
			q := query()
			q.Origin, q.Destination, q.Departure = d.Stops[rng.IntN(6)].ID, d.Stops[rng.IntN(6)].ID, int64(rng.IntN(6))
			q.LatestArrival, q.MaxLegs, q.MaxWalkSeconds = 35, 256, 1000
			q.Walking = &model.WalkingLimits{Access: int64(rng.IntN(7)), Egress: int64(rng.IntN(7)), Transfer: int64(rng.IntN(9)), Direct: int64(rng.IntN(7))}
			ms, n := prefixSpec(t, d, q)
			visits += n
			flashCompare(t, d, x, q, objectives(ms, q.MaxBoardings))
		}
	}
	t.Logf("1280 random DAG OD/cap cases, %d independent raw prefixes, 3 controls", visits)
}
func FuzzFlashRoleCaps(f *testing.F) {
	for _, seed := range []uint64{0, 1, 71, 319} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, seed uint64) { checkGeneratedRoleCaps(t, seed) })
}

func TestFlashRoleCapsIsolationAndGuards(t *testing.T) {
	d, q := roleDetourData()
	old, e := tripbased.BuildFlash(context.Background(), d, ruleConfig(6))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = old.Search(context.Background(), q); e == nil {
		t.Fatal("default must retain nonbinding guards")
	}
	c := roleConfig(6)
	c.Semantics = ""
	if _, e = tripbased.BuildFlash(context.Background(), d, c); e == nil {
		t.Fatal("canonical flags cannot opt into role caps")
	}
	p := rulePatternConfig()
	p.Profile = roleConfig(6)
	if _, e = tripbased.BuildPatterns(context.Background(), d, p); e == nil {
		t.Fatal("TP optimal patterns lack cap completeness proof")
	}
	x, e := tripbased.BuildFlash(context.Background(), d, roleConfig(6))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if r, e := x.Search(ctx, q); e == nil || r.Complete || len(r.Journeys) != 0 {
		t.Fatal("cancelled query", r, e)
	}
	before, _ := json.Marshal(x.Snapshot())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); flashCompare(t, d, x, q, []int64{inf, inf, 9}) }()
	}
	wg.Wait()
	after, _ := json.Marshal(x.Snapshot())
	if string(before) != string(after) {
		t.Fatal("query mutated prepared snapshot")
	}
	for _, mutate := range []func(*model.Query){
		func(q *model.Query) { q.MaxWalkSeconds = 5 },
		func(q *model.Query) { q.MaxLegs = 2 },
		func(q *model.Query) { q.TransferBuffer = 1 },
		func(q *model.Query) { q.AccessibleOnly = true },
	} {
		bad := q
		mutate(&bad)
		if r, e := x.Search(context.Background(), bad); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("unsupported query accepted", bad, r, e)
		}
	}
	for _, mutate := range []func(*tripbased.FlashConfig){
		func(c *tripbased.FlashConfig) { c.Limits.MaxWork = 1 },
		func(c *tripbased.FlashConfig) { c.Limits.MaxTransfers = 1 },
		func(c *tripbased.FlashConfig) { c.Limits.MaxSuffixBytes = 1 },
		func(c *tripbased.FlashConfig) { c.Limits.MaxFlagBytes = 1 },
	} {
		c := roleConfig(6)
		mutate(&c)
		partial, e := tripbased.BuildFlash(context.Background(), d, c)
		if e == nil || partial.Complete() || partial.Snapshot().Complete {
			t.Fatal("partial preparation accepted", e)
		}
		if r, e := partial.Search(context.Background(), q); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("partial index answered", r, e)
		}
	}
}
