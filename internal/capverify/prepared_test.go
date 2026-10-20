package capverify

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

func rawLiteral() (model.Dataset, model.Query) {
	d := model.Dataset{SchemaVersion: "0.2", ID: "raw-verifier-literal", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-16", Window: &model.TimeWindow{Start: 0, End: 200}}
	for _, id := range []string{"S", "A", "B", "C", "T", "P", "Q", "U"} {
		d.Stops = append(d.Stops, model.Stop{ID: id, Accessible: model.Yes})
	}
	d.Stops[1].ParentStation = "P"
	d.Stops[2].ParentStation = "P"
	d.Stops[3].ParentStation = "Q"
	d.Stops[5].LocationType = 1
	d.Stops[6].LocationType = 1
	d.Trips = []model.Trip{{ID: "t0", SourceID: "source0", RouteID: "r0", Accessible: model.Yes}, {ID: "t1", SourceID: "source1", RouteID: "r1", Accessible: model.Yes}, {ID: "t2", SourceID: "source2", RouteID: "r2", Accessible: model.Yes}}
	d.Connections = []model.Connection{
		{ID: "sa", TripID: "t0", Sequence: 0, From: "S", To: "A", Departure: 10, Arrival: 20, Pickup: true, Dropoff: true},
		{ID: "ab", TripID: "t0", Sequence: 1, From: "A", To: "B", Departure: 25, Arrival: 30, Pickup: true, Dropoff: true},
		{ID: "bc", TripID: "t1", Sequence: 0, From: "B", To: "C", Departure: 40, Arrival: 50, Pickup: true, Dropoff: true},
		{ID: "ct", TripID: "t2", Sequence: 0, From: "C", To: "T", Departure: 60, Arrival: 70, Pickup: true, Dropoff: true},
		{ID: "unrelated", TripID: "t2", Sequence: 1, From: "T", To: "U", Departure: 80, Arrival: 90, Pickup: true, Dropoff: true},
	}
	d.Footpaths = []model.Footpath{
		{ID: "access", From: "S", To: "A", Duration: 1, Accessible: model.Yes},
		{ID: "walk-ab", From: "A", To: "B", Duration: 1, Accessible: model.Yes},
		{ID: "walk-bc", From: "B", To: "C", Duration: 1, Accessible: model.Yes},
		{ID: "egress", From: "C", To: "T", Duration: 1, Accessible: model.Yes},
		{ID: "cycle", From: "B", To: "A", Duration: 1, Accessible: model.Yes},
		{ID: "direct", From: "S", To: "T", Duration: 8, Accessible: model.Yes},
	}
	q := model.Query{ID: "raw-q", Origin: "S", Destination: "T", LatestArrival: 100, MaxWalkSeconds: 8, MaxLegs: 8, MaxBoardings: 4, Walking: &model.WalkingLimits{Access: 2, Transfer: 3, Egress: 3, Direct: 8}}
	return d, q
}
func rawPrepLimits() PreparationLimits {
	return PreparationLimits{MaxRows: 1000, MaxWork: 2000, MaxPayloadBytes: 100000}
}
func rawLimits() Limits { return Limits{MaxStates: 10000, MaxJourneys: 10000, MaxScans: 10000000} }
func compareRaw(t *testing.T, d model.Dataset, q model.Query) {
	t.Helper()
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	p, _, err := PrepareRaw(context.Background(), d, rawPrepLimits())
	if err != nil {
		t.Fatal(err)
	}
	a, ea := p.Enumerate(context.Background(), q, rawLimits())
	b, eb := Enumerate(context.Background(), d, q, rawLimits())
	if (ea == nil) != (eb == nil) || ea != nil && ea.Error() != eb.Error() || a.Complete != b.Complete || a.States != b.States || !reflect.DeepEqual(a.Journeys, b.Journeys) {
		t.Fatalf("ordered physical words differ\nprepared=%+v %v\nscan=%+v %v", a, ea, b, eb)
	}
	if a.Complete && a.ProbeStats.LegacyScans != int64(b.Scans) {
		t.Fatal("shadow logical meter differs", a.ProbeStats, b.Scans)
	}
	if a.Scans != a.ProbeStats.ConnectionRows+a.ProbeStats.FootpathRows+a.ProbeStats.RuleRows+a.ProbeStats.MetadataProbes {
		t.Fatal("unclassified probes", a.ProbeStats)
	}
	if ea == nil {
		checker, e := validate.New(d)
		if e != nil {
			t.Fatal(e)
		}
		for _, j := range a.Journeys {
			if _, e = checker.Check(q, j); e != nil {
				t.Fatal("whole-witness replay", j, e)
			}
		}
	}
}
func TestPreparedRawOrderedWords(t *testing.T) {
	for _, kind := range []string{"base", "reverse_input", "nil_roles", "zero_roles", "leg_bound", "board_bound", "no_pickup", "no_dropoff", "no_dropoff_goal", "inaccessible_stop", "inaccessible_trip", "inaccessible_walk", "minimum", "prohibited", "station", "source_alias", "route_priority", "ambiguous", "empty_goal"} {
		t.Run(kind, func(t *testing.T) {
			d, q := rawLiteral()
			switch kind {
			case "reverse_input":
				for i, j := 0, len(d.Connections)-1; i < j; i, j = i+1, j-1 {
					d.Connections[i], d.Connections[j] = d.Connections[j], d.Connections[i]
				}
				for i, j := 0, len(d.Footpaths)-1; i < j; i, j = i+1, j-1 {
					d.Footpaths[i], d.Footpaths[j] = d.Footpaths[j], d.Footpaths[i]
				}
			case "nil_roles":
				q.Walking = nil
			case "zero_roles":
				q.Walking = &model.WalkingLimits{}
			case "leg_bound":
				q.MaxLegs = 2
				q.MaxBoardings = 2
			case "board_bound":
				q.MaxBoardings = 1
			case "no_pickup":
				d.Connections[1].Pickup = false
			case "no_dropoff":
				d.Connections[0].Dropoff = false
			case "no_dropoff_goal":
				q.Destination = "A"
				d.Connections[0].Dropoff = false
			case "inaccessible_stop":
				q.AccessibleOnly = true
				d.Stops[2].Accessible = model.Unknown
			case "inaccessible_trip":
				q.AccessibleOnly = true
				d.Trips[1].Accessible = model.No
			case "inaccessible_walk":
				q.AccessibleOnly = true
				d.Footpaths[2].Accessible = model.Unknown
			case "minimum":
				d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "B", ToStop: "B", Type: 2, MinSeconds: 11}}
			case "prohibited":
				d.Transfers = []model.TransferRule{{ID: "forbid", FromStop: "B", ToStop: "B", Type: 3}}
			case "station":
				d.Transfers = []model.TransferRule{{ID: "parent", FromStop: "P", ToStop: "P", Type: 3}}
			case "source_alias":
				d.Transfers = []model.TransferRule{{ID: "alias", FromStop: "B", ToStop: "B", FromTrip: "source0", ToTrip: "source1", Type: 3}}
			case "route_priority":
				d.Transfers = []model.TransferRule{{ID: "lower", FromStop: "B", ToStop: "B", Type: 3}, {ID: "higher", FromStop: "B", ToStop: "B", FromRoute: "r0", ToRoute: "r1", Type: 0}}
			case "ambiguous":
				d.Transfers = []model.TransferRule{{ID: "one", FromStop: "B", ToStop: "B", Type: 2}, {ID: "two", FromStop: "P", ToStop: "B", Type: 2}}
			case "empty_goal":
				q.Destination = q.Origin
				q.MaxLegs = 0
				q.MaxBoardings = 0
			}
			compareRaw(t, d, q)
		})
	}
	for _, role := range []string{"access", "egress", "transfer", "direct"} {
		for cap := int64(0); cap <= 4; cap++ {
			t.Run(role+string(rune('0'+cap)), func(t *testing.T) {
				d, q := rawLiteral()
				switch role {
				case "access":
					q.Walking.Access = cap
				case "egress":
					q.Walking.Egress = cap
				case "transfer":
					q.Walking.Transfer = cap
				case "direct":
					q.Walking.Direct = cap
				}
				compareRaw(t, d, q)
			})
		}
	}
}

type rawCancelBoundary struct{ calls, target int }

func (c *rawCancelBoundary) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *rawCancelBoundary) Done() <-chan struct{}       { return nil }
func (c *rawCancelBoundary) Value(any) any               { return nil }
func (c *rawCancelBoundary) Err() error {
	c.calls++
	if c.target > 0 && c.calls >= c.target {
		return context.Canceled
	}
	return nil
}

func TestPreparedRawGuardsOwnershipAndCancellation(t *testing.T) {
	d, q := rawLiteral()
	p, stats, e := PrepareRaw(context.Background(), d, rawPrepLimits())
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"rows", "work", "payload"} {
		t.Run("preparation_"+kind, func(t *testing.T) {
			limits := rawPrepLimits()
			switch kind {
			case "rows":
				limits.MaxRows = stats.Rows - 1
			case "work":
				limits.MaxWork = stats.Work - 1
			case "payload":
				limits.MaxPayloadBytes = stats.PayloadBytes - 1
			}
			bad, _, err := PrepareRaw(context.Background(), d, limits)
			if err == nil || bad != nil {
				t.Fatal("partial preparation usable", bad, err)
			}
		})
	}
	complete, err := p.Enumerate(context.Background(), q, rawLimits())
	if err != nil || len(complete.Journeys) < 2 {
		t.Fatal(complete, err)
	}
	for _, kind := range []string{"probes", "states", "journeys"} {
		t.Run(kind, func(t *testing.T) {
			limits := rawLimits()
			switch kind {
			case "probes":
				limits.MaxScans = complete.Scans - 1
			case "states":
				limits.MaxStates = complete.States - 1
			case "journeys":
				limits.MaxJourneys = len(complete.Journeys) - 1
			}
			a, e := p.Enumerate(context.Background(), q, limits)
			if e == nil || a.Complete {
				t.Fatal("guard falsely complete", a, e)
			}
		})
	}
	prepContext := &rawCancelBoundary{}
	if _, _, e := PrepareRaw(prepContext, d, rawPrepLimits()); e != nil {
		t.Fatal(e)
	}
	for target := 1; target <= prepContext.calls; target++ {
		bad, _, err := PrepareRaw(&rawCancelBoundary{target: target}, d, rawPrepLimits())
		if !errors.Is(err, context.Canceled) || bad != nil {
			t.Fatal("preparation cancellation", target, err)
		}
	}
	queryContext := &rawCancelBoundary{}
	if _, e := p.Enumerate(queryContext, q, rawLimits()); e != nil {
		t.Fatal(e)
	}
	for target := 1; target <= queryContext.calls; target++ {
		a, e := p.Enumerate(&rawCancelBoundary{target: target}, q, rawLimits())
		if !errors.Is(e, context.Canceled) || a.Complete {
			t.Fatal("query cancellation", target, a, e)
		}
	}
	d.Connections[0].Arrival = 199
	d.Stops[0].Accessible = model.No
	d.Window.End = 1
	complete.Journeys[0].Legs[0].Ref = "caller-mutated"
	want, e := p.Enumerate(context.Background(), q, rawLimits())
	if e != nil {
		t.Fatal(e)
	}
	if reflect.DeepEqual(complete.Journeys, want.Journeys) {
		t.Fatal("output mutation control ineffective")
	}
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			got, e := p.Enumerate(context.Background(), q, rawLimits())
			if e != nil || !reflect.DeepEqual(got, want) {
				t.Error("immutable reader differed", got, e)
			}
		}()
	}
	group.Wait()
}

func TestPreparedRawLiteralProbeAccounting(t *testing.T) {
	d := model.Dataset{SchemaVersion: "0.2", ID: "one", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-16", Stops: []model.Stop{{ID: "S", Accessible: model.Yes}, {ID: "T", Accessible: model.Yes}}, Trips: []model.Trip{{ID: "t", Accessible: model.Yes}}, Connections: []model.Connection{{ID: "ride", TripID: "t", From: "S", To: "T", Departure: 1, Arrival: 2, Pickup: true, Dropoff: true}}}
	q := model.Query{ID: "q", Origin: "S", Destination: "T", LatestArrival: 3, MaxLegs: 1, MaxBoardings: 1}
	p, stats, e := PrepareRaw(context.Background(), d, rawPrepLimits())
	if e != nil {
		t.Fatal(e)
	}
	// Four raw rows + two stop-normalization visits + two validator visits.
	if stats.Rows != 4 || stats.Work != 8 {
		t.Fatal("literal preparation charge", stats)
	}
	got, e := p.Enumerate(context.Background(), q, Limits{MaxScans: 5, MaxStates: 2, MaxJourneys: 1})
	if e != nil || !got.Complete || got.Scans != 5 || got.ProbeStats.ConnectionRows != 1 || got.ProbeStats.MetadataProbes != 4 || got.ProbeStats.LegacyScans != 1 {
		t.Fatal("literal five-probe exact boundary", got, e)
	}
	got, e = p.Enumerate(context.Background(), q, Limits{MaxScans: 4, MaxStates: 2, MaxJourneys: 1})
	if e == nil || got.Complete || got.Scans != 4 {
		t.Fatal("one-short probe boundary", got, e)
	}
}

func TestPreparedRawEveryRuleSpecificity(t *testing.T) {
	selectors := []model.TransferRule{
		{FromStop: "B", ToStop: "B"},
		{FromStop: "B", ToStop: "B", FromRoute: "r0"},
		{FromStop: "B", ToStop: "B", FromRoute: "r0", ToRoute: "r1"},
		{FromStop: "B", ToStop: "B", FromTrip: "source0"},
		{FromStop: "B", ToStop: "B", FromTrip: "source0", ToRoute: "r1"},
		{FromStop: "B", ToStop: "B", FromTrip: "source0", ToTrip: "source1"},
	}
	for low := 0; low < len(selectors); low++ {
		for high := low + 1; high < len(selectors); high++ {
			t.Run(string(rune('1'+low))+"_under_"+string(rune('1'+high)), func(t *testing.T) {
				d, q := rawLiteral()
				q.Walking = &model.WalkingLimits{}
				q.MaxWalkSeconds = 0
				lower, higher := selectors[low], selectors[high]
				lower.ID = "lower"
				lower.Type = 3
				higher.ID = "higher"
				higher.Type = 0
				d.Transfers = []model.TransferRule{lower, higher}
				compareRaw(t, d, q)
				p, _, e := PrepareRaw(context.Background(), d, rawPrepLimits())
				if e != nil {
					t.Fatal(e)
				}
				got, e := p.Enumerate(context.Background(), q, rawLimits())
				refs := [][]string{}
				for _, j := range got.Journeys {
					word := []string{}
					for _, leg := range j.Legs {
						word = append(word, leg.Ref)
					}
					refs = append(refs, word)
				}
				if e != nil || !reflect.DeepEqual(refs, [][]string{{"sa", "ab", "bc", "ct"}}) {
					t.Fatal("higher specificity literal allowance lost", refs, e)
				}
			})
		}
	}
	t.Run("ambiguous_lower_is_superseded", func(t *testing.T) {
		d, q := rawLiteral()
		q.Walking = &model.WalkingLimits{}
		q.MaxWalkSeconds = 0
		d.Transfers = []model.TransferRule{{ID: "low-exact", FromStop: "B", ToStop: "B", Type: 3}, {ID: "low-station", FromStop: "P", ToStop: "B", Type: 3}, {ID: "high", FromStop: "B", ToStop: "B", FromRoute: "r0", Type: 0}}
		compareRaw(t, d, q)
	})
}
