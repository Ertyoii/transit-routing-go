package tripbased_test

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/tripbased"
)

// Independent tiny raw directed-access traversal; no prepared walking/profile
// helper is used to determine the required complete readiness denominator.
func domainReadiness(d model.Dataset, source string, q model.Query) []int64 {
	dist, settled := map[string]int64{source: 0}, map[string]bool{}
	for {
		name, at := "", inf
		for id, v := range dist {
			if !settled[id] && (v < at || v == at && id < name) {
				name, at = id, v
			}
		}
		if name == "" {
			break
		}
		settled[name] = true
		if name == q.Destination {
			continue
		}
		for _, w := range d.Footpaths {
			if w.From != name || at+w.Duration > q.Walking.Access {
				continue
			}
			if old, ok := dist[w.To]; !ok || at+w.Duration < old {
				dist[w.To] = at + w.Duration
			}
		}
	}
	points := map[int64]bool{d.Window.Start: true}
	for _, c := range d.Connections {
		walk, ok := dist[c.From]
		if !ok || c.From == q.Destination || !c.Pickup || c.Arrival > d.Window.End {
			continue
		}
		at := c.Departure - walk
		if at >= d.Window.Start && at <= d.Window.End {
			points[at] = true
		}
	}
	answer := []int64{}
	for at := range points {
		answer = append(answer, at)
	}
	sort.Slice(answer, func(i, j int) bool { return answer[i] > answer[j] })
	return answer
}

func domainConfig(d model.Dataset, q model.Query, sources ...string) tripbased.DestinationSourceDomainConfig {
	c := retainedCappedConfig(d, q)
	c.Scope.MaxProfileRecords = 0 // telemetry may be omitted, never readiness.
	c.Scope.MaxProfileSources = len(sources)
	return tripbased.DestinationSourceDomainConfig{Retained: c, Sources: sources}
}

func compareSourceDomain(t testing.TB, d model.Dataset, q model.Query, sources []string, everyInteger bool) {
	t.Helper()
	if d.Window == nil {
		d.Window = &model.TimeWindow{End: q.LatestArrival}
	}
	q.MaxWalkSeconds, q.MaxLegs, q.TransferBuffer, q.AccessibleOnly = 6000, 1024, 0, false
	q.LatestArrival = d.Window.End
	c := domainConfig(d, q, sources...)
	before, _ := json.Marshal(d)
	x, err := tripbased.BuildDestinationSourceDomainPatterns(context.Background(), d, c)
	if err != nil || !x.DomainComplete() || x.GlobalComplete() {
		t.Fatal("domain build", err, x.Snapshot())
	}
	after, _ := json.Marshal(d)
	if string(before) != string(after) {
		t.Fatal("domain preparation mutated full network")
	}
	s := x.Snapshot()
	if s.InnerGlobalComplete || !s.FinalizationComplete || len(s.Sources) != len(sources) || s.Stats.SourcesCompleted != len(sources) || s.Stats.NativeProfileQueries != 0 || s.RetainedStats.ProfileRecordsOmitted != s.Stats.OfferedReadiness {
		t.Fatal("domain completeness/accounting", s)
	}
	total := 0
	for _, row := range s.Sources {
		v := q
		v.Origin = row.Source
		points := domainReadiness(d, row.Source, v)
		if !row.Complete || !row.LowerSentinel || row.ReadinessCompleted != len(points) {
			t.Fatal("missing source/readiness", row, points)
		}
		total += len(points)
		departures := map[int64]bool{d.Window.Start: true, d.Window.End: true}
		if everyInteger {
			if d.Window.End-d.Window.Start > 100 {
				t.Fatal("unbounded tiny fixture")
			}
			for at := d.Window.Start; at <= d.Window.End; at++ {
				departures[at] = true
			}
		} else {
			for _, at := range points {
				for _, delta := range []int64{-1, 0, 1} {
					if p := at + delta; p >= d.Window.Start && p <= d.Window.End {
						departures[p] = true
					}
				}
			}
			for i := 1; i < len(points); i++ {
				departures[points[i]+(points[i-1]-points[i])/2] = true
			}
		}
		for at := range departures {
			v.Departure = at
			ms, _ := prefixSpec(t, d, v)
			r, e := x.Search(context.Background(), v)
			if e != nil || !r.Complete {
				t.Fatal("ordinary domain query", e, v, r)
			}
			if got, want := objectives(reconstructed(t, expandedRuleSpec(d), v, r.Journeys), v.MaxBoardings), objectives(ms, v.MaxBoardings); !reflect.DeepEqual(got, want) {
				t.Fatal("independent source-domain frontier", v, got, want, r)
			}
		}
	}
	if s.Stats.OfferedReadiness != total {
		t.Fatal("offered denominator", s.Stats, total)
	}
	for _, stop := range d.Stops {
		allowed := false
		for _, source := range sources {
			allowed = allowed || stop.ID == source
		}
		if allowed {
			continue
		}
		v := q
		v.Origin = stop.ID
		r, e := x.Search(context.Background(), v)
		if e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("undeclared source answered", v, r, e)
		}
	}
}

func TestSourceDomainIndependentTinyQueries(t *testing.T) {
	for seed := uint64(0); seed < 16; seed++ {
		d, q := generated(seed)
		sources := []string{}
		for _, stop := range d.Stops {
			if stop.ID != q.Destination {
				sources = append(sources, stop.ID)
			}
		}
		sort.Strings(sources)
		if len(sources) < 2 {
			t.Fatal("tiny fixture lacks two non-goal origins")
		}
		compareSourceDomain(t, d, q, sources[:2], true)
	}
	for _, caps := range []model.WalkingLimits{{}, {Transfer: 5}, {Transfer: 6}, {Access: 1, Egress: 1, Transfer: 6, Direct: 1}, {Access: 6, Egress: 6, Transfer: 6, Direct: 6}} {
		d, q := roleDetourData()
		d.Window = &model.TimeWindow{End: 20}
		q.Walking = &caps
		compareSourceDomain(t, d, q, []string{"X", "A"}, true)
	}
}

func TestSourceDomainLiteralReadinessAndEmpty(t *testing.T) {
	d, q := data(), query()
	d.Window = &model.TimeWindow{End: 20}
	q.Walking = &model.WalkingLimits{}
	trip(&d, "early", []string{"A", "D"}, 4, 8)
	trip(&d, "late", []string{"A", "D"}, 12, 13)
	compareSourceDomain(t, d, q, []string{"A", "Y"}, true)
	c := domainConfig(d, q, "A", "Y")
	x, e := tripbased.BuildDestinationSourceDomainPatterns(context.Background(), d, c)
	if e != nil {
		t.Fatal(e)
	}
	s := x.Snapshot()
	if len(s.Sources) != 2 || s.Sources[0].ReadinessCompleted != 3 || s.Sources[1].ReadinessCompleted != 1 || s.Stats.OfferedReadiness != 4 {
		t.Fatal("literal exact departures 12,4,0 and unreachable 0", s)
	}
	v := q
	v.Origin = "Y"
	v.LatestArrival = 20
	v.MaxWalkSeconds = 6000
	v.MaxLegs = 1024
	r, e := x.Search(context.Background(), v)
	if e != nil || !r.Complete || len(r.Journeys) != 0 {
		t.Fatal("qualified unreachable is complete empty", r, e)
	}
}

func TestSourceDomainCapabilityOwnershipAndOrder(t *testing.T) {
	d, q := roleDetourData()
	d.Window = &model.TimeWindow{End: 20}
	q.MaxWalkSeconds, q.MaxLegs = 6000, 1024
	q.LatestArrival = 20
	c := domainConfig(d, q, "X", "A")
	x, e := tripbased.BuildDestinationSourceDomainPatterns(context.Background(), d, c)
	if e != nil {
		t.Fatal(e)
	}
	before, _ := json.Marshal(x.Snapshot())
	c.Sources[0] = "bad-caller-mutation"
	c.Retained.Scope.Walking.Access = 0
	s := x.Snapshot()
	s.Config.Sources[0] = "bad-snapshot-mutation"
	s.Sources[0].Source = "bad-snapshot-row"
	after, _ := json.Marshal(x.Snapshot())
	if string(before) != string(after) {
		t.Fatal("domain ownership")
	}
	d2 := d.Clone()
	slices.Reverse(d2.Stops)
	slices.Reverse(d2.Trips)
	slices.Reverse(d2.Connections)
	slices.Reverse(d2.Footpaths)
	y, e := tripbased.BuildDestinationSourceDomainPatterns(context.Background(), d2, domainConfig(d2, q, "A", "X"))
	if e != nil {
		t.Fatal(e)
	}
	a, e := x.Search(context.Background(), q)
	if e != nil {
		t.Fatal(e)
	}
	b, e := y.Search(context.Background(), q)
	if e != nil || !reflect.DeepEqual(a.Journeys, b.Journeys) || !reflect.DeepEqual(a.Graph, b.Graph) {
		t.Fatal("input/domain order changed deterministic query", e, a, b)
	}
	for _, change := range []func(*model.Query){func(v *model.Query) { v.Origin = "D" }, func(v *model.Query) { v.Origin = "unknown" }, func(v *model.Query) { v.Destination = "C" }, func(v *model.Query) { w := *v.Walking; w.Access++; v.Walking = &w }, func(v *model.Query) { v.Departure = -1 }, func(v *model.Query) { v.LatestArrival = 21 }, func(v *model.Query) { v.TransferBuffer = 1 }, func(v *model.Query) { v.AccessibleOnly = true }, func(v *model.Query) { v.MaxBoardings = 5 }, func(v *model.Query) { v.MaxWalkSeconds = 1 }, func(v *model.Query) { v.MaxLegs = 1 }} {
		v := q
		change(&v)
		r, e := x.Search(context.Background(), v)
		if e == nil || r.Complete || len(r.Journeys) != 0 {
			t.Fatal("capability mismatch answered", v, r, e)
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
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
	after, _ = json.Marshal(x.Snapshot())
	if string(before) != string(after) {
		t.Fatal("queries mutated domain certificate")
	}
}

func TestSourceDomainFailureBoundaries(t *testing.T) {
	d, q := roleDetourData()
	d.Window = &model.TimeWindow{End: 20}
	q.MaxWalkSeconds = 6000
	q.LatestArrival = 20
	base := domainConfig(d, q, "A", "X")
	for name, change := range map[string]func(*tripbased.DestinationSourceDomainConfig){"missing-domain": func(c *tripbased.DestinationSourceDomainConfig) { c.Sources = nil }, "unknown": func(c *tripbased.DestinationSourceDomainConfig) { c.Sources = []string{"no-stop"} }, "goal": func(c *tripbased.DestinationSourceDomainConfig) { c.Sources = []string{"D"} }, "duplicate": func(c *tripbased.DestinationSourceDomainConfig) { c.Sources = []string{"A", "A"} }, "global-order": func(c *tripbased.DestinationSourceDomainConfig) { c.Retained.SourceOrder = []string{"A"} }, "source-budget": func(c *tripbased.DestinationSourceDomainConfig) { c.Retained.Scope.MaxProfileSources = 1 }, "readiness": func(c *tripbased.DestinationSourceDomainConfig) { c.Retained.Scope.MaxProfileQueries = 1 }, "work": func(c *tripbased.DestinationSourceDomainConfig) { c.Retained.Scope.Patterns.MaxBuildWork = 1 }, "finalization": func(c *tripbased.DestinationSourceDomainConfig) { c.Retained.Scope.Patterns.MaxDirectEntries = 1 }} {
		t.Run(name, func(t *testing.T) {
			c := base
			change(&c)
			x, e := tripbased.BuildDestinationSourceDomainPatterns(context.Background(), d, c)
			if e == nil || x.DomainComplete() || x.GlobalComplete() {
				t.Fatal("incomplete domain accepted", e, x.Snapshot())
			}
			r, e := x.Search(context.Background(), q)
			if e == nil || r.Complete || len(r.Journeys) != 0 {
				t.Fatal("failed domain answered", r, e)
			}
			if name == "finalization" {
				s := x.Snapshot()
				if s.Stats.SourcesCompleted != 2 || s.FinalizationComplete || !strings.Contains(s.Error, "direct incidence budget") {
					t.Fatal("late finalization cause/accounting lost", s)
				}
			}
		})
	}
	count := cancelAt(1 << 60)
	x, e := tripbased.BuildDestinationSourceDomainPatterns(count, d, base)
	if e != nil {
		t.Fatal(e)
	}
	checks := count.checks.Load()
	count.cancel()
	lateSourceComplete := false
	for _, at := range []int64{1, checks / 2, checks - 5, checks - 3, checks - 1, checks} {
		ctx := cancelAt(max(1, at))
		y, e := tripbased.BuildDestinationSourceDomainPatterns(ctx, d, base)
		ctx.cancel()
		if e == nil || y.DomainComplete() || y.GlobalComplete() {
			t.Fatal("cancellation certified", at, checks, e)
		}
		s := y.Snapshot()
		lateSourceComplete = lateSourceComplete || s.Stats.SourcesCompleted == 2
		if r, e := y.Search(context.Background(), q); e == nil || r.Complete {
			t.Fatal("cancelled domain answered", r, e)
		}
	}
	if !lateSourceComplete {
		t.Fatal("did not exercise cancellation after all sources", checks, x.Snapshot())
	}
}
