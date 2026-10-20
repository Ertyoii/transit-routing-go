package tripbased_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"github.com/ertyoii/transit-routing-go/tripbased"
)

func retainedCappedConfig(d model.Dataset, q model.Query) tripbased.DestinationRetainedCappedConfig {
	c := fixedPatternConfig(d, q)
	c.ProfileMaxWalkingSeconds = 6000
	c.MaxProfileRecords = 10000
	return tripbased.DestinationRetainedCappedConfig{Scope: c, MaxPendingSegments: 100000, MaxSourceSeeds: 100000}
}

func normalizeRetainedFrontier(v []int64) []int64 {
	out := slices.Clone(v)
	for i := range out {
		if out[i] == paper.Infinity {
			out[i] = inf
		}
	}
	return out
}

// This boundary checks independent raw prefix enumeration, not just fresh vs
// retained agreement. Every output itinerary is replayed by the whole-word spec.
func compareRetainedCapped(t testing.TB, d model.Dataset, q model.Query, allInteger bool) map[string]any {
	t.Helper()
	if d.Window == nil {
		d.Window = &model.TimeWindow{End: q.LatestArrival}
	}
	q.LatestArrival = d.Window.End
	q.MaxWalkSeconds, q.MaxLegs, q.TransferBuffer, q.AccessibleOnly = 6000, 1024, 0, false
	c := retainedCappedConfig(d, q)
	x, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, c)
	if e != nil || !x.Complete() {
		t.Fatal("retained build", e, d, q)
	}
	s := x.Snapshot()
	if s.Method != "fixed_cap_destination_retained_exact_events_v1" || s.Retained == nil || s.Stats.NativeProfileQueries != 0 || s.Stats.SourcesCompleted != len(d.Stops)-1 || s.Retained.Stats.ProfileRecordsOmitted != 0 || len(s.Retained.Profiles) != s.Stats.OfferedReadiness {
		t.Fatal("retained accounting", s.Stats, s.Retained)
	}
	if s.Patterns.Backend.Stats.RetainedReadiness != 0 || s.Patterns.Backend.Stats.ProfileRuns != 0 {
		t.Fatal("unbounded profile policy executed", s.Patterns.Backend.Stats)
	}
	if s.Retained.Stats.EventVisits > c.Scope.Patterns.Profile.MaxBoardings*len(d.Connections)*s.Stats.SourcesStarted || s.Retained.Stats.QueuedSegments != s.Retained.Stats.ProcessedSegments {
		t.Fatal("newly exposed event/queue invariant", s.Retained.Stats)
	}
	if s.Retained.Stats.TransferVisits > (c.Scope.Patterns.Profile.MaxBoardings-1)*s.Patterns.Backend.Stats.CanonicalTransfers*s.Stats.SourcesStarted {
		t.Fatal("once-per-event transfer bound", s.Retained.Stats, s.Patterns.Backend.Stats)
	}
	profiles := []map[string]any{}
	points := map[string]map[int64]bool{}
	visits := 0
	for _, r := range s.Retained.Profiles {
		v := q
		v.Origin, v.Departure, v.MaxBoardings = r.Source, r.Departure, c.Scope.Patterns.Profile.MaxBoardings
		ms, n := prefixSpec(t, d, v)
		visits += n
		want := objectives(ms, v.MaxBoardings)
		if got := normalizeRetainedFrontier(r.Frontier); !reflect.DeepEqual(got, want) {
			t.Fatal("retained profile mismatch", r, want, d, v)
		}
		if points[r.Source] == nil {
			points[r.Source] = map[int64]bool{}
		}
		points[r.Source][r.Departure] = true
		profiles = append(profiles, map[string]any{"source": r.Source, "departure": r.Departure, "oracle_frontier": want, "retained_frontier": normalizeRetainedFrontier(r.Frontier)})
	}
	// Fresh construction is useful corroboration, but never the oracle above.
	fresh, e := tripbased.BuildDestinationCappedPatterns(context.Background(), d, c.Scope)
	if e != nil {
		t.Fatal("fresh reference", e)
	}
	if fresh.Snapshot().Stats.OfferedReadiness != s.Stats.OfferedReadiness {
		t.Fatal("exact breakpoint universe changed", fresh.Snapshot().Stats, s.Stats)
	}
	queries := 0
	for _, source := range d.Stops {
		at := map[int64]bool{d.Window.Start: true, d.Window.End: true}
		if allInteger {
			if d.Window.End-d.Window.Start > 100 {
				t.Fatal("unbounded tiny integer window")
			}
			for departure := d.Window.Start; departure <= d.Window.End; departure++ {
				at[departure] = true
			}
		} else {
			ordered := []int64{d.Window.Start, d.Window.End}
			for departure := range points[source.ID] {
				ordered = append(ordered, departure)
				for _, delta := range []int64{-1, 0, 1} {
					if v := departure + delta; v >= d.Window.Start && v <= d.Window.End {
						at[v] = true
					}
				}
			}
			sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
			for i := 1; i < len(ordered); i++ {
				at[ordered[i-1]+(ordered[i]-ordered[i-1])/2] = true
			}
		}
		departures := []int64{}
		for departure := range at {
			departures = append(departures, departure)
		}
		sort.Slice(departures, func(i, j int) bool { return departures[i] < departures[j] })
		for _, departure := range departures {
			v := q
			v.Origin, v.Departure = source.ID, departure
			ms, n := prefixSpec(t, d, v)
			visits += n
			want := objectives(ms, v.MaxBoardings)
			for name, index := range map[string]*tripbased.DestinationCappedPatternIndex{"retained": x, "fresh": fresh} {
				r, e := index.Search(context.Background(), v)
				if e != nil || !r.Complete {
					t.Fatal(name, e, v)
				}
				if got := objectives(reconstructed(t, expandedRuleSpec(d), v, r.Journeys), v.MaxBoardings); !reflect.DeepEqual(got, want) {
					t.Fatal("query mismatch", name, got, want, d, v, r)
				}
			}
			queries++
		}
	}
	for _, w := range s.Patterns.Witnesses {
		v := q
		v.Origin, v.Departure, v.MaxBoardings = w.Source, w.Departure, c.Scope.Patterns.Profile.MaxBoardings
		reconstructed(t, expandedRuleSpec(d), v, []model.Journey{w.Journey})
	}
	return map[string]any{"dataset": d, "scope": c, "stats": s.Stats, "retained_stats": s.Retained.Stats, "profiles": profiles, "queries_compared": queries, "independent_raw_prefix_visits": visits, "witnesses": s.Patterns.Witnesses, "query_integer_exhaustive": allInteger}
}

func TestRetainedCappedFrozenIndependentCases(t *testing.T) {
	f, e := os.Open("testdata/fixed_caps_cases.jsonl")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	s.Buffer(make([]byte, 4096), 1000000)
	evidence := []map[string]any{}
	for s.Scan() {
		var c struct {
			ID      string        `json:"id"`
			Dataset model.Dataset `json:"dataset"`
			Query   model.Query   `json:"query"`
		}
		if e = json.Unmarshal(s.Bytes(), &c); e != nil {
			t.Fatal(e)
		}
		evidence = append(evidence, compareRetainedCapped(t, c.Dataset, c.Query, false))
	}
	if e = s.Err(); e != nil {
		t.Fatal(e)
	}
	if len(evidence) != 56 {
		t.Fatal(len(evidence))
	}
	if path := os.Getenv("RETAINED_CAP_EVIDENCE"); path != "" {
		b, e := json.MarshalIndent(map[string]any{"scope": "56 frozen independent synthetic models only; no full-model qualification", "cases": evidence}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, append(b, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}

func TestRetainedCappedEveryTinyDeparture(t *testing.T) {
	for seed := uint64(0); seed < 32; seed++ {
		d, q := generated(seed)
		q.TransferBuffer, q.AccessibleOnly = 0, false
		compareRetainedCapped(t, d, q, true)
	}
	for _, caps := range []model.WalkingLimits{{}, {Transfer: 5}, {Transfer: 6}, {Access: 1, Egress: 1, Transfer: 6, Direct: 1}, {Access: 6, Egress: 6, Transfer: 6, Direct: 6}} {
		d, q := roleDetourData()
		d.Window = &model.TimeWindow{End: 20}
		q.Walking = &caps
		compareRetainedCapped(t, d, q, true)
	}
}

func TestRetainedCappedLiteralParentsAndPermissions(t *testing.T) {
	for _, minimum := range []int64{0, 4, 9} {
		d, q := data(), query()
		d.Window = &model.TimeWindow{End: 24}
		q.LatestArrival = 24
		q.Walking = &model.WalkingLimits{Access: 4, Transfer: 5, Direct: 3, Egress: 0}
		trip(&d, "first", []string{"A", "B"}, 2, 3)
		trip(&d, "later", []string{"A", "C"}, 4, 6)
		trip(&d, "early-receiver", []string{"X", "D"}, 9, 10)
		trip(&d, "late-receiver", []string{"X", "D"}, 15, 16)
		d.Footpaths = []model.Footpath{{ID: "ab", From: "A", To: "B", Duration: 4, Accessible: model.Yes}, {ID: "bx", From: "B", To: "X", Duration: 4, Accessible: model.Yes}, {ID: "cy", From: "C", To: "Y", Duration: 1, Accessible: model.Yes}, {ID: "yx", From: "Y", To: "X", Duration: 1, Accessible: model.Yes}}
		d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "B", ToStop: "X", Type: 2, MinSeconds: minimum}}
		compareRetainedCapped(t, d, q, true)
	}
	for _, dropoff := range []bool{false, true} {
		d, q := data(), query()
		d.Window = &model.TimeWindow{Start: 90000, End: 90020}
		q.Departure, q.LatestArrival = 90000, 90020
		q.Walking = &model.WalkingLimits{Access: 2, Transfer: 4}
		trip(&d, "loop", []string{"A", "B", "D", "C", "D"}, 90002, 90003, 90004, 90005, 90006, 90007, 90008, 90009)
		d.Connections[1].Dropoff = dropoff
		d.Connections[2].Pickup = false
		d.Connections[3].Pickup = false
		d.Footpaths = []model.Footpath{{ID: "xa", From: "X", To: "A", Duration: 2, Accessible: model.Yes}}
		d.Stops = append(d.Stops, model.Stop{ID: "PB", LocationType: 1, Accessible: model.Yes})
		d.Stops[1].ParentStation = "PB"
		d.Transfers = []model.TransferRule{{ID: "parent-no-reboard", FromStop: "PB", ToStop: "PB", Type: 3}}
		compareRetainedCapped(t, d, q, true)
	}
	// Overtaking routes must remain separate exact trips/lines; equal alternatives
	// and a same-duration two-edge walk exercise deterministic geometry and ties.
	d, q := data(), query()
	d.Window = &model.TimeWindow{End: 30}
	q.LatestArrival = 30
	q.Walking = &model.WalkingLimits{Access: 4, Transfer: 5, Egress: 0, Direct: 0}
	trip(&d, "slow", []string{"A", "B", "D"}, 2, 12, 13, 22)
	trip(&d, "fast", []string{"A", "B", "D"}, 3, 7, 8, 15)
	trip(&d, "tie", []string{"A", "B", "D"}, 3, 7, 8, 15)
	d.Footpaths = []model.Footpath{{ID: "ax", From: "A", To: "X", Duration: 2, Accessible: model.Yes}, {ID: "xy", From: "X", To: "Y", Duration: 2, Accessible: model.Yes}, {ID: "ay", From: "A", To: "Y", Duration: 4, Accessible: model.Yes}}
	compareRetainedCapped(t, d, q, true)
}

func TestRetainedCappedLaterTripParentRepair(t *testing.T) {
	d, q := data(), query()
	d.Window = &model.TimeWindow{End: 20}
	q.LatestArrival = 20
	q.Walking = &model.WalkingLimits{Access: 2, Transfer: 1}
	trip(&d, "early-feed", []string{"A", "B"}, 4, 5)
	trip(&d, "late-feed", []string{"A", "C"}, 8, 9)
	trip(&d, "early-receiver", []string{"X", "D"}, 7, 8)
	trip(&d, "late-receiver", []string{"X", "D"}, 12, 13)
	d.Footpaths = []model.Footpath{{ID: "bx", From: "B", To: "X", Duration: 1, Accessible: model.Yes}, {ID: "cx", From: "C", To: "X", Duration: 1, Accessible: model.Yes}, {ID: "ya", From: "Y", To: "A", Duration: 2, Accessible: model.Yes}}
	compareRetainedCapped(t, d, q, true)
	q.Origin = "Y"
	q.Departure = 0
	q.MaxWalkSeconds = 6000
	x, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, retainedCappedConfig(d, q))
	if e != nil {
		t.Fatal(e)
	}
	r, e := x.Search(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	ms := reconstructed(t, d, q, r.Journeys)
	if !reflect.DeepEqual(objectives(ms, q.MaxBoardings), []int64{inf, inf, 8, 8}) {
		t.Fatal("early receiving trip lost", r, ms)
	}
	if len(r.Journeys) != 1 || len(r.Journeys[0].Legs) != 4 || r.Journeys[0].Legs[0].Departure != 0 || r.Journeys[0].Legs[1].Ref != "early-feed/0" || r.Journeys[0].Legs[3].Ref != "early-receiver/0" {
		t.Fatal("wrong retained parent or access clock", r)
	}
}

func TestRetainedCappedThreeBoardings(t *testing.T) {
	d, q := roleDetourData()
	d.Window = &model.TimeWindow{End: 20}
	for i := range d.Connections {
		if d.Connections[i].TripID == "out-early" || d.Connections[i].TripID == "out-late" {
			d.Connections[i].To = "X"
		}
	}
	d.Footpaths = append(d.Footpaths, model.Footpath{ID: "xy", From: "X", To: "Y", Duration: 3, Accessible: model.Yes})
	trip(&d, "third", []string{"Y", "D"}, 13, 14)
	d.Transfers = append(d.Transfers, model.TransferRule{ID: "no-skip", FromStop: "B", ToStop: "Y", Type: 3})
	q.MaxBoardings, q.MaxWalkSeconds = 3, 6000
	compareRetainedCapped(t, d, q, true)
}

func TestRetainedCappedExposesEachEventAndTransferOnce(t *testing.T) {
	d, q := data(), query()
	d.Window = &model.TimeWindow{End: 60}
	stops := []string{}
	times := []int64{}
	for i := 0; i <= 16; i++ {
		id := fmt.Sprintf("chain-%02d", i)
		stops = append(stops, id)
		d.Stops = append(d.Stops, model.Stop{ID: id, Accessible: model.Yes})
		if i < 16 {
			d.Footpaths = append(d.Footpaths, model.Footpath{ID: "access-" + id, From: "X", To: id, Duration: 1, Accessible: model.Yes})
			times = append(times, int64(10+2*i), int64(11+2*i))
		}
	}
	trip(&d, "chain", stops, times...)
	trip(&d, "exit", []string{stops[16], "D"}, 50, 51)
	q.Origin, q.LatestArrival, q.MaxBoardings, q.MaxLegs, q.MaxWalkSeconds = "X", 60, 4, 1024, 6000
	q.Walking = &model.WalkingLimits{Access: 1}
	c := retainedCappedConfig(d, q)
	c.Scope.MaxProfileSources = 1
	c.SourceOrder = []string{"X"}
	for _, stop := range d.Stops {
		if stop.ID != "X" && stop.ID != "D" {
			c.SourceOrder = append(c.SourceOrder, stop.ID)
		}
	}
	x, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, c)
	if e == nil || x.Complete() {
		t.Fatal("one-source prefix must stay incomplete", e)
	}
	s := x.Snapshot()
	if s.Stats.SourcesCompleted != 1 || s.Stats.OfferedReadiness != 17 || s.Retained.Stats.EventVisits != 17 || s.Retained.Stats.TransferVisits != 1 || s.Retained.Stats.SeedOffers != 16 {
		t.Fatal("disjoint exact-event work changed", s.Stats, s.Retained.Stats)
	}
	if r, e := x.Search(context.Background(), q); e == nil || r.Complete || len(r.Journeys) != 0 {
		t.Fatal("work prefix answered", r, e)
	}
	native, e := tripbased.New(context.Background(), d, config(q))
	if e != nil {
		t.Fatal(e)
	}
	connections, transfers := 0, 0
	for _, profile := range s.Retained.Profiles {
		v := q
		v.Departure = profile.Departure
		ms, _ := prefixSpec(t, d, v)
		if want := objectives(ms, v.MaxBoardings); !reflect.DeepEqual(want, []int64{inf, inf, 51, 51, 51}) {
			t.Fatal("independent work fixture changed", want)
		}
		r, e := native.SearchCappedWalking(context.Background(), v)
		if e != nil {
			t.Fatal(e)
		}
		if got := objectives(reconstructed(t, d, v, r.Journeys), v.MaxBoardings); !reflect.DeepEqual(got, normalizeRetainedFrontier(profile.Frontier)) {
			t.Fatal("work fixture frontier differs", got, profile)
		}
		connections += r.Stats.Connections
		transfers += r.Stats.Transfers
	}
	if connections != 169 || transfers != 17 {
		t.Fatal("fresh work-count control changed", connections, transfers)
	}
	for _, w := range s.Patterns.Witnesses {
		v := q
		v.Departure = w.Departure
		reconstructed(t, d, v, []model.Journey{w.Journey})
	}
	if path := os.Getenv("RETAINED_WORK_EVIDENCE"); path != "" {
		b, e := json.MarshalIndent(map[string]any{"scope": "one-source tiny synthetic structural work comparison; incomplete global index; no wall-clock performance claim", "dataset": d, "config": c, "stats": s.Stats, "retained_stats": s.Retained.Stats, "fresh_native_reconstruction_visits": connections, "fresh_native_transfer_visits": transfers, "external_queries_run": 0}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(path, append(b, '\n'), 0600); e != nil {
			t.Fatal(e)
		}
	}
}

func TestRetainedCappedInputSourceOrderOwnership(t *testing.T) {
	d, q := roleDetourData()
	d.Window = &model.TimeWindow{End: 20}
	q.MaxWalkSeconds = 6000
	c := retainedCappedConfig(d, q)
	x, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, c)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := json.Marshal(x.Snapshot())
	reversed := d.Clone()
	slices.Reverse(reversed.Stops)
	slices.Reverse(reversed.Trips)
	slices.Reverse(reversed.Connections)
	slices.Reverse(reversed.Footpaths)
	slices.Reverse(reversed.Transfers)
	for _, stop := range reversed.Stops {
		if stop.ID != q.Destination {
			c.SourceOrder = append(c.SourceOrder, stop.ID)
		}
	}
	y, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), reversed, c)
	if e != nil {
		t.Fatal(e)
	}
	for _, source := range d.Stops {
		for at := int64(0); at <= 20; at++ {
			v := q
			v.Origin, v.Departure = source.ID, at
			var baseline tripbased.PatternResult
			for i, index := range []*tripbased.DestinationCappedPatternIndex{x, y} {
				r, e := index.Search(context.Background(), v)
				if e != nil {
					t.Fatal(e)
				}
				ms, _ := prefixSpec(t, d, v)
				if !reflect.DeepEqual(objectives(reconstructed(t, d, v, r.Journeys), v.MaxBoardings), objectives(ms, v.MaxBoardings)) {
					t.Fatal(source, at, r)
				}
				if i == 0 {
					baseline = r
				} else if !reflect.DeepEqual(baseline.Journeys, r.Journeys) || !reflect.DeepEqual(baseline.Graph, r.Graph) {
					t.Fatal("input/source order changed deterministic query representative", v, baseline, r)
				}
			}
		}
	}
	s := y.Snapshot()
	s.Retained.Config.SourceOrder[0] = "mutated"
	s.Retained.Profiles[0].Frontier[0] = -1
	if y.Snapshot().Retained.Config.SourceOrder[0] == "mutated" || y.Snapshot().Retained.Profiles[0].Frontier[0] == -1 {
		t.Fatal("retained snapshot aliases")
	}
	// Fixed input order has deterministic full representatives, not all tied IDs.
	z, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, retainedCappedConfig(d, q))
	if e != nil {
		t.Fatal(e)
	}
	if !reflect.DeepEqual(x.Snapshot().Patterns.Witnesses, z.Snapshot().Patterns.Witnesses) {
		t.Fatal("ties nondeterministic")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r, e := x.Search(context.Background(), q); e != nil || !r.Complete {
				t.Error(e, r)
			}
		}()
	}
	wg.Wait()
	after, _ := json.Marshal(x.Snapshot())
	if string(before) != string(after) {
		t.Fatal("query mutated retained index")
	}
	bad := c
	bad.SourceOrder = bad.SourceOrder[:len(bad.SourceOrder)-1]
	if p, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, bad); e == nil || p.Complete() {
		t.Fatal("incomplete source domain accepted")
	}
}

func TestRetainedCappedGuardsAndScope(t *testing.T) {
	d, q := roleDetourData()
	d.Window = &model.TimeWindow{End: 20}
	q.MaxWalkSeconds = 6000
	base := retainedCappedConfig(d, q)
	for name, mutate := range map[string]func(*tripbased.DestinationRetainedCappedConfig){
		"sources":   func(c *tripbased.DestinationRetainedCappedConfig) { c.Scope.MaxProfileSources = 1 },
		"readiness": func(c *tripbased.DestinationRetainedCappedConfig) { c.Scope.MaxProfileQueries = 1 },
		"segments":  func(c *tripbased.DestinationRetainedCappedConfig) { c.Scope.MaxProfileSegments = 1 },
		"pending":   func(c *tripbased.DestinationRetainedCappedConfig) { c.MaxPendingSegments = 1 },
		"seed":      func(c *tripbased.DestinationRetainedCappedConfig) { c.MaxSourceSeeds = 1 },
		"patterns":  func(c *tripbased.DestinationRetainedCappedConfig) { c.Scope.Patterns.MaxPatterns = 1 },
		"work":      func(c *tripbased.DestinationRetainedCappedConfig) { c.Scope.Patterns.MaxBuildWork = 1 },
		"legs":      func(c *tripbased.DestinationRetainedCappedConfig) { c.Scope.ProfileMaxLegs = 4 },
		"aggregate": func(c *tripbased.DestinationRetainedCappedConfig) { c.Scope.ProfileMaxWalkingSeconds = 1 },
	} {
		t.Run(name, func(t *testing.T) {
			c := base
			mutate(&c)
			x, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, c)
			if e == nil || x.Complete() {
				t.Fatal("guard accepted", name, e)
			}
			if r, e := x.Search(context.Background(), q); e == nil || r.Complete || len(r.Journeys) != 0 {
				t.Fatal("partial index answered", name, r, e)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if x, e := tripbased.BuildDestinationRetainedCappedPatterns(ctx, d, base); e == nil || x.Complete() {
		t.Fatal("cancelled index accepted", e)
	}
	x, e := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, base)
	if e != nil {
		t.Fatal(e)
	}
	for _, change := range []func(*model.Query){func(q *model.Query) { q.Destination = "C" }, func(q *model.Query) { w := *q.Walking; w.Transfer++; q.Walking = &w }, func(q *model.Query) { q.MaxLegs = 1 }, func(q *model.Query) { q.MaxWalkSeconds = 1 }} {
		v := q
		change(&v)
		if r, e := x.Search(context.Background(), v); e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("unsupported query accepted", fmt.Sprint(v), e)
		}
	}
}

func TestRetainedCappedExactWorkBoundariesAndCancellation(t *testing.T) {
	d, q := roleDetourData()
	d.Window = &model.TimeWindow{End: 20}
	q.MaxWalkSeconds = 6000
	c := retainedCappedConfig(d, q)
	count := cancelAt(1 << 60)
	x, e := tripbased.BuildDestinationRetainedCappedPatterns(count, d, c)
	count.cancel()
	if e != nil {
		t.Fatal(e)
	}
	s := x.Snapshot()
	for _, which := range []string{"readiness", "segments"} {
		limit := s.Stats.OfferedReadiness
		if which == "segments" {
			limit = s.Retained.Stats.QueuedSegments
		}
		for _, delta := range []int{0, -1} {
			cfg := c
			if which == "readiness" {
				cfg.Scope.MaxProfileQueries = limit + delta
			} else {
				cfg.Scope.MaxProfileSegments = limit + delta
			}
			index, err := tripbased.BuildDestinationRetainedCappedPatterns(context.Background(), d, cfg)
			if delta == 0 && (err != nil || !index.Complete()) {
				t.Fatal("exact work boundary declined", which, err)
			}
			if delta < 0 && (err == nil || index.Complete()) {
				t.Fatal("exhausted exact work boundary accepted", which, err)
			}
		}
	}
	partialProfile := false
	for part := int64(1); part <= 16; part++ {
		ctx := cancelAt(max(1, count.checks.Load()*part/16))
		index, err := tripbased.BuildDestinationRetainedCappedPatterns(ctx, d, c)
		ctx.cancel()
		if !errors.Is(err, context.Canceled) || index.Complete() {
			t.Fatal("checkpoint cancellation accepted", part, err)
		}
		v := index.Snapshot()
		partialProfile = partialProfile || (v.Stats.SourcesStarted > v.Stats.SourcesCompleted && v.Stats.OfferedReadiness > 0)
		if r, err := index.Search(context.Background(), q); err == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("cancelled prefix answered", part, r, err)
		}
	}
	if !partialProfile {
		t.Fatal("no retained profile cancellation exercised")
	}
}
