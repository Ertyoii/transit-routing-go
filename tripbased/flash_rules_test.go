package tripbased_test

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
	"github.com/ertyoii/transit-routing-go/validate"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func ruleConfig(cells int) tripbased.FlashConfig {
	c := flashConfig(cells)
	c.Semantics = "static_stop_pair_v1"
	c.Limits.MaxSuffixBytes = 1000000
	c.Limits.MaxSnapshotTransfers = 10000
	return c
}
func rulePatternConfig() tripbased.PatternConfig {
	c := patternConfig()
	c.Profile = ruleConfig(1)
	return c
}

// Independent test normalization: expand parent selectors into child pairs by
// scanning raw stop records. Neither search nor the production resolver is used.
func expandedRuleSpec(d model.Dataset) model.Dataset {
	out := d.Clone()
	out.Transfers = nil
	match := func(selector string, s model.Stop) bool { return selector == s.ID || s.ParentStation == selector }
	for _, r := range d.Transfers {
		for _, a := range d.Stops {
			if !match(r.FromStop, a) {
				continue
			}
			for _, b := range d.Stops {
				if !match(r.ToStop, b) {
					continue
				}
				v := r
				v.FromStop = a.ID
				v.ToStop = b.ID
				out.Transfers = append(out.Transfers, v)
			}
		}
	}
	return out
}
func checkRuleModel(t testing.TB, d model.Dataset, q model.Query, cells int) {
	t.Helper()
	ms, _ := fullWords(expandedRuleSpec(d), q, 7)
	want := objectives(ms, q.MaxBoardings)
	fx, e := tripbased.BuildFlash(context.Background(), d, ruleConfig(cells))
	if e != nil {
		t.Fatal(e, d)
	}
	flashCompare(t, d, fx, q, want)
	pc := rulePatternConfig()
	px, e := tripbased.BuildPatterns(context.Background(), d, pc)
	if e != nil {
		t.Fatal(e, d)
	}
	pr, e := px.Search(context.Background(), q)
	if e != nil || !pr.Complete {
		t.Fatal(e, pr)
	}
	got := []model.Metrics{}
	checker, e := validate.New(d)
	if e != nil {
		t.Fatal(e)
	}
	for _, j := range pr.Journeys {
		v, e := checker.Check(q, j)
		if e != nil {
			t.Fatalf("TP invalid witness %v %+v", e, j)
		}
		got = append(got, v.Metrics)
	}
	if !reflect.DeepEqual(objectives(got, q.MaxBoardings), want) {
		t.Fatalf("TP want%v got%v data%+v q%+v", want, objectives(got, q.MaxBoardings), d, q)
	}
	for _, w := range px.Snapshot().Witnesses {
		wq := flashQuery(query())
		wq.Origin = w.Source
		wq.Destination = w.Target
		wq.Departure = w.Departure
		wq.MaxBoardings = 4
		if d.Window != nil {
			wq.LatestArrival = d.Window.End
		}
		if _, e := checker.Check(wq, w.Journey); e != nil {
			t.Fatalf("invalid profile witness: %v %+v", e, w)
		}
	}
}
func TestRuleSemanticsIndependentWords(t *testing.T) {
	for seed := uint64(0); seed < 320; seed++ {
		d, q := generated(seed)
		q = flashQuery(q)
		checkRuleModel(t, d, q, 1+int(seed%3))
	}
	t.Log("320 source-rule/pickup/dropoff networks; full unpruned word specification, FLASH3controls and TP/native witness checks")
}
func TestRuleSemanticsHistoryAndMinimum(t *testing.T) {
	for _, kind := range []string{"prohibited", "minimum", "parent"} {
		d := data()
		q := flashQuery(query())
		q.LatestArrival = 100
		trip(&d, "in", []string{"A", "B"}, 0, 2)
		trip(&d, "out-early", []string{"C", "D"}, 4, 5)
		trip(&d, "out-late", []string{"C", "D"}, 8, 9)
		d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 1, Accessible: model.Yes}}
		d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: 5}}
		if kind == "parent" {
			d.Stops = append(d.Stops, model.Stop{ID: "P", LocationType: 1, Accessible: model.Yes}, model.Stop{ID: "Q", LocationType: 1, Accessible: model.Yes})
			for i := range d.Stops {
				if d.Stops[i].ID == "B" {
					d.Stops[i].ParentStation = "P"
				}
				if d.Stops[i].ID == "C" {
					d.Stops[i].ParentStation = "Q"
				}
			}
			d.Transfers[0].FromStop = "P"
			d.Transfers[0].ToStop = "Q"
		} else {
			// Earlier arrival at C via X cannot dominate B's usable transfer context.
			trip(&d, "bad-context", []string{"A", "X"}, 0, 1)
			d.Footpaths = append(d.Footpaths, model.Footpath{ID: "xc", From: "X", To: "C", Duration: 1, Accessible: model.Yes})
			r := model.TransferRule{ID: "bad", FromStop: "X", ToStop: "C", Type: 3}
			if kind == "minimum" {
				r.Type = 2
				r.MinSeconds = 90
			}
			d.Transfers = append(d.Transfers, r)
		}
		// This literal reference uses at most two rides and one walk: depth3 is complete.
		ms, _ := fullWords(expandedRuleSpec(d), q, 3)
		want := objectives(ms, q.MaxBoardings)
		if want[2] != 9 {
			t.Fatal("independent reference should choose 9", kind, want)
		}
		fx, e := tripbased.BuildFlash(context.Background(), d, ruleConfig(3))
		if e != nil {
			t.Fatal(e)
		}
		// The retained raw assess helper lacks parent expansion; compare with expanded
		// input for its feasibility specification, while native validation uses raw d.
		flashCompare(t, expandedRuleSpec(d), fx, q, want)
		px, e := tripbased.BuildPatterns(context.Background(), d, rulePatternConfig())
		if e != nil {
			t.Fatal(e)
		}
		r, e := px.Search(context.Background(), q)
		if e != nil {
			t.Fatal(e)
		}
		checker, _ := validate.New(d)
		metrics := []model.Metrics{}
		for _, j := range r.Journeys {
			v, e := checker.Check(q, j)
			if e != nil {
				t.Fatal(kind, e, j)
			}
			metrics = append(metrics, v.Metrics)
		}
		if !reflect.DeepEqual(objectives(metrics, q.MaxBoardings), want) {
			t.Fatalf("%s TP want%v got%v", kind, want, objectives(metrics, q.MaxBoardings))
		}
	}
}
func TestRuleSemanticsStayAboardAndRestrictions(t *testing.T) {
	d := data()
	trip(&d, "through", []string{"A", "B", "D"}, 0, 1, 2, 3)
	trip(&d, "later", []string{"B", "D"}, 8, 9)
	d.Connections[0].Dropoff = false
	d.Connections[1].Pickup = false
	d.Transfers = []model.TransferRule{{ID: "self", FromStop: "B", ToStop: "B", Type: 3}}
	for _, origin := range []string{"A", "B"} {
		q := flashQuery(query())
		q.Origin = origin
		checkRuleModel(t, d, q, 3)
	}
}
func TestRuleSemanticsRealFlagsAndFailure(t *testing.T) {
	d := data()
	trip(&d, "in", []string{"A", "B"}, 0, 2)
	trip(&d, "out", []string{"B", "D"}, 3, 5)
	trip(&d, "branch", []string{"B", "X"}, 3, 6)
	c := ruleConfig(len(d.Stops))
	x, e := tripbased.BuildFlash(context.Background(), d, c)
	if e != nil {
		t.Fatal(e)
	}
	q := flashQuery(query())
	r, e := x.Search(context.Background(), q)
	if e != nil || r.Stats.FlagRejected == 0 {
		t.Fatal("target cells must actually filter transfers", e, r)
	}
	s := x.Snapshot()
	if s.Stats.ProfileRuns != 0 || s.Stats.TransferRuns != 0 || s.Stats.SetFlags == 0 || !strings.Contains(s.FlagConstruction, "conservative") {
		t.Fatal(s)
	}
	for _, kind := range []string{"suffix", "profile-runs", "selector", "cancelled"} {
		cfg := rulePatternConfig()
		input := d.Clone()
		ctx, cancel := context.WithCancel(context.Background())
		switch kind {
		case "suffix":
			cfg.Profile.Limits.MaxSuffixBytes = 1
		case "profile-runs":
			cfg.Profile.Limits.MaxRuns = 1
		case "selector":
			input.Transfers = []model.TransferRule{{ID: "selected", FromStop: "B", ToStop: "B", FromTrip: "in", Type: 0}}
		case "cancelled":
			cancel()
		}
		px, e := tripbased.BuildPatterns(ctx, input, cfg)
		cancel()
		if px.Snapshot().Complete {
			t.Fatal("partial snapshot accepted", kind)
		}
		if e == nil || px.Complete() {
			t.Fatal("incomplete policy accepted", kind, e)
		}
		result, e := px.Search(context.Background(), q)
		if e == nil || result.Complete || len(result.Journeys) != 0 {
			t.Fatal("partial index answered", kind, result, e)
		}
	}
	// The original public mode must retain its rejection and identifier.
	d.Transfers = []model.TransferRule{{ID: "old-guard", FromStop: "B", ToStop: "B", Type: 0}}
	if x, e := tripbased.BuildFlash(context.Background(), d, flashConfig(3)); e == nil || x.Complete() {
		t.Fatal("default guard changed")
	}
}
func TestRuleSemanticsThreeBoardingsAndLoop(t *testing.T) {
	for seed := 0; seed < 32; seed++ {
		d := data()
		trip(&d, "first", []string{"A", "B"}, 0, 2)
		trip(&d, "second", []string{"C", "X"}, int64(4+seed%4), int64(6+seed%4))
		trip(&d, "third", []string{"Y", "D"}, int64(10+seed%7), int64(12+seed%7))
		d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 1, Accessible: model.Yes}, {ID: "xy", From: "X", To: "Y", Duration: 2, Accessible: model.Yes}}
		d.Transfers = []model.TransferRule{{ID: "bc-min", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: int64(seed % 5)}, {ID: "xy-min", FromStop: "X", ToStop: "Y", Type: 2, MinSeconds: int64(seed % 7)}}
		if seed%4 == 0 {
			d.Connections[1].Pickup = false
		}
		if seed%5 == 0 {
			d.Connections[0].Dropoff = false
		}
		if seed%6 == 0 {
			d.Transfers[0].Type = 3
			d.Transfers[0].MinSeconds = 0
		}
		q := flashQuery(query())
		q.MaxBoardings = 3
		checkRuleModel(t, d, q, 3)
	}
	d := data()
	trip(&d, "loop", []string{"A", "B", "C", "B", "D"}, 0, 1, 1, 2, 2, 3, 3, 4)
	trip(&d, "late", []string{"B", "D"}, 8, 9)
	d.Connections[1].Pickup = false
	d.Connections[3].Pickup = false
	d.Transfers = []model.TransferRule{{ID: "no-change-at-B", FromStop: "B", ToStop: "B", Type: 3}}
	for _, origin := range []string{"A", "B", "C"} {
		q := flashQuery(query())
		q.Origin = origin
		checkRuleModel(t, d, q, 3)
	}
}
func TestRuleSemanticsBoundedDiagnosticsAndQueryCaps(t *testing.T) {
	d := data()
	trip(&d, "in", []string{"A", "B"}, 0, 2)
	trip(&d, "out", []string{"B", "D"}, 3, 5)
	trip(&d, "branch", []string{"B", "X"}, 3, 6)
	cfg := ruleConfig(len(d.Stops))
	cfg.Limits.MaxSnapshotTransfers = 1
	x, e := tripbased.BuildFlash(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	snap := x.Snapshot()
	if !snap.Complete || snap.OmittedTransferRecords <= 1 || len(snap.Transfers) != 0 {
		t.Fatal("diagnostic omission must be explicit", snap)
	}
	q := flashQuery(query())
	flashCompare(t, d, x, q, []int64{inf, inf, 5, 5})
	q.TransferBuffer = 1
	if r, e := x.Search(context.Background(), q); e == nil || r.Complete {
		t.Fatal("unprepared query buffer accepted")
	}
	d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 4, Accessible: model.Yes}}
	cfg = ruleConfig(3)
	x, e = tripbased.BuildFlash(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	q = flashQuery(query())
	q.Walking.Transfer = 0
	if r, e := x.Search(context.Background(), q); e == nil || r.Complete {
		t.Fatal("binding transfer cap accepted")
	}
}
func TestRuleSemanticsDeclaredWindow(t *testing.T) {
	d := data()
	d.Window = &model.TimeWindow{Start: 5, End: 12}
	trip(&d, "in", []string{"A", "B"}, 6, 7)
	trip(&d, "out", []string{"B", "D"}, 9, 10)
	trip(&d, "too-late", []string{"B", "D"}, 13, 14)
	d.Footpaths = []model.Footpath{{ID: "ca", From: "C", To: "A", Duration: 5, Accessible: model.Yes}, {ID: "bx", From: "B", To: "X", Duration: 6, Accessible: model.Yes}}
	for _, origin := range []string{"A", "C"} {
		q := flashQuery(query())
		q.Origin = origin
		q.Departure = 5
		q.LatestArrival = 12
		checkRuleModel(t, d, q, 3)
	}
}
func TestRuleSemanticsPreparedOrderAndConcurrency(t *testing.T) {
	d := data()
	trip(&d, "in", []string{"A", "B"}, 0, 2)
	trip(&d, "out", []string{"C", "D"}, 8, 9)
	d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 1, Accessible: model.Yes}}
	d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: 5}}
	fx, e := tripbased.BuildFlash(context.Background(), d, ruleConfig(3))
	if e != nil {
		t.Fatal(e)
	}
	px, e := tripbased.BuildPatterns(context.Background(), d, rulePatternConfig())
	if e != nil {
		t.Fatal(e)
	}
	check := func(departure int64) {
		q := flashQuery(query())
		q.Departure = departure
		ms, _ := fullWords(d, q, 3)
		want := objectives(ms, q.MaxBoardings)
		flashCompare(t, d, fx, q, want)
		patternCompare(t, d, px, q, want)
		snap := fx.Snapshot()
		if len(snap.Transfers) > 0 {
			snap.Transfers[0].RawEdges = nil
			snap.Transfers[0].Cells = nil
		}
		snap.Partition.Cells["D"] = -99
	}
	for _, dep := range []int64{4, 1, 0, 1, 4, 0} {
		check(dep)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) { defer wg.Done(); check(int64(i % 3)) }(i)
	}
	wg.Wait()
	check(0)
}
func TestRuleSemanticsRecombinedContextCounterexample(t *testing.T) {
	d := data()
	d.Window = &model.TimeWindow{Start: 0, End: 120}
	trip(&d, "good-in", []string{"A", "B"}, 0, 2)
	trip(&d, "bad-in", []string{"A", "X"}, 1, 2)
	trip(&d, "early-out", []string{"C", "D"}, 8, 9)
	trip(&d, "late-out", []string{"C", "D"}, 110, 111)
	d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 2, Accessible: model.Yes}, {ID: "xc", From: "X", To: "C", Duration: 1, Accessible: model.Yes}}
	d.Transfers = []model.TransferRule{{ID: "good-min", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: 5}, {ID: "bad-min", FromStop: "X", ToStop: "C", Type: 2, MinSeconds: 100}}
	q := flashQuery(query())
	q.LatestArrival = 120
	ms, _ := fullWords(d, q, 3)
	want := objectives(ms, q.MaxBoardings)
	if want[2] != 9 {
		t.Fatal("bad independent literal", want)
	}
	px, e := tripbased.BuildPatterns(context.Background(), d, rulePatternConfig())
	if e != nil {
		t.Fatal(e)
	}
	result, e := px.Search(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	needed := map[tripbased.PatternEdge]bool{{From: "A", To: "X", Kind: "ride"}: false, {From: "X", To: "C", Kind: "walk"}: false, {From: "B", To: "C", Kind: "walk"}: false}
	for _, edge := range result.Graph {
		if _, ok := needed[edge]; ok {
			needed[edge] = true
		}
	}
	for edge, present := range needed {
		if !present {
			t.Fatal("counterexample graph did not contain competing context", edge, result.Graph)
		}
	}
	got := objectives(reconstructed(t, d, q, result.Journeys), q.MaxBoardings)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("history collapse: want%v got%v", want, got)
	}
	// At readiness1 only the bad incoming trip remains, so its later valid pattern
	// must also be retained; the graph contains both contexts for readiness0.
	q.Departure = 1
	reference, _ := fullWords(d, q, 3)
	patternCompare(t, d, px, q, objectives(reference, q.MaxBoardings))
}
func TestRuleSemanticsAllODReadinessCells(t *testing.T) {
	d := data()
	d.Window = &model.TimeWindow{Start: 0, End: 120}
	trip(&d, "good-in", []string{"A", "B"}, 0, 2)
	trip(&d, "bad-in", []string{"A", "X"}, 1, 2)
	trip(&d, "early-out", []string{"C", "D"}, 8, 9)
	trip(&d, "late-out", []string{"C", "D"}, 110, 111)
	d.Footpaths = []model.Footpath{{ID: "bc", From: "B", To: "C", Duration: 2, Accessible: model.Yes}, {ID: "xc", From: "X", To: "C", Duration: 1, Accessible: model.Yes}}
	d.Transfers = []model.TransferRule{{ID: "good-min", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: 5}, {ID: "bad-min", FromStop: "X", ToStop: "C", Type: 2, MinSeconds: 100}}
	fx, e := tripbased.BuildFlash(context.Background(), d, ruleConfig(len(d.Stops)))
	if e != nil {
		t.Fatal(e)
	}
	px, e := tripbased.BuildPatterns(context.Background(), d, rulePatternConfig())
	if e != nil {
		t.Fatal(e)
	}
	cases := 0
	for _, from := range d.Stops {
		for _, to := range d.Stops {
			for _, ready := range []int64{0, 1, 2, 3, 8, 110, 120} {
				q := flashQuery(query())
				q.Origin = from.ID
				q.Destination = to.ID
				q.Departure = ready
				q.LatestArrival = 120
				// The raw graph is acyclic and any feasible word has at most3legs.
				ms, _ := fullWords(d, q, 3)
				want := objectives(ms, q.MaxBoardings)
				flashCompare(t, d, fx, q, want)
				patternCompare(t, d, px, q, want)
				cases++
			}
		}
	}
	t.Logf("%d complete small-network OD/readiness cases, each stop a separate flag cell", cases)
}
func FuzzRuleSemanticsIndependent(f *testing.F) {
	for _, s := range []uint64{0, 1, 71, 319} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		d, q := generated(seed)
		q = flashQuery(q)
		checkRuleModel(t, d, q, 3)
	})
}
