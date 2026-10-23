package ultra

import (
	"context"
	"fmt"
	"math/rand/v2"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

func denseLimits() Limits {
	return Limits{MaxRuns: 10000, MaxConnectionVisits: 1000000, MaxWalkSettled: 1000000, MaxWalkEdges: 1000000, MaxShortcuts: 100000, MaxOutputLegs: 1000000}
}

// Tiny directed networks include parallel shortest witnesses, cycles, repeated
// stops, overtaking/equal-time trips, restricted pickup/dropoff, nontransit
// endpoints, accessibility, and station-parent static transfer rules.
func denseDataset(seed uint64) model.Dataset {
	r := rand.New(rand.NewPCG(seed, seed+91))
	d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: fmt.Sprintf("dense-%d", seed), TimeZone: "Etc/UTC", ServiceDate: "2026-10-19"}
	d.Stops = append(d.Stops, model.Stop{ID: "P", LocationType: 1, Accessible: model.Yes})
	for i := 0; i < 6; i++ {
		s := model.Stop{ID: fmt.Sprintf("s%d", i), Accessible: model.Yes}
		if i < 2 {
			s.ParentStation = "P"
		}
		if seed%3 == 0 && i == 3 {
			s.Accessible = model.No
		}
		if seed%5 == 0 && i == 4 {
			s.Accessible = model.Unknown
		}
		d.Stops = append(d.Stops, s)
	}
	add := func(id string, cs []model.Connection, access model.Access) {
		d.Trips = append(d.Trips, model.Trip{ID: id, Accessible: access})
		for j, c := range cs {
			c.ID = fmt.Sprintf("%s:%d", id, j)
			c.TripID = id
			c.Sequence = j
			d.Connections = append(d.Connections, c)
		}
	}
	for i := 0; i < 7; i++ {
		from := r.IntN(5)
		at := int64(r.IntN(10))
		cs := []model.Connection{}
		for j := 0; j < 1+r.IntN(3); j++ {
			to := (from + 1 + r.IntN(4)) % 5
			c := model.Connection{From: fmt.Sprintf("s%d", from), To: fmt.Sprintf("s%d", to), Departure: at, Arrival: at + 1 + int64(r.IntN(3)), Pickup: r.IntN(5) != 0, Dropoff: r.IntN(5) != 0}
			cs = append(cs, c)
			from = to
			at = c.Arrival + int64(r.IntN(3))
		}
		access := model.Yes
		if i == 6 && seed%2 == 0 {
			access = model.No
		}
		add(fmt.Sprintf("t%d", i), cs, access)
		if i == 0 {
			add("equal", cs, access)
		}
	}
	for i := 0; i < 6; i++ {
		for j := 0; j < 6; j++ {
			if i != j && r.IntN(3) == 0 {
				edge := model.Footpath{ID: fmt.Sprintf("w%d-%d", i, j), From: fmt.Sprintf("s%d", i), To: fmt.Sprintf("s%d", j), Duration: 1 + int64(r.IntN(5)), Accessible: model.Yes}
				if (i+j)%5 == 0 && seed%2 == 0 {
					edge.Accessible = model.No
				}
				d.Footpaths = append(d.Footpaths, edge)
				if (i+j)%4 == 0 {
					edge.ID += "-parallel"
					d.Footpaths = append(d.Footpaths, edge)
				}
			}
		}
	}
	switch seed % 4 {
	case 0:
		d.Transfers = []model.TransferRule{{ID: "station-zero", FromStop: "P", ToStop: "P", Type: 0}}
	case 1:
		d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "s1", ToStop: "s2", Type: 2, MinSeconds: 7}}
	case 2:
		d.Transfers = []model.TransferRule{{ID: "forbidden", FromStop: "s2", ToStop: "s3", Type: 3}}
	}
	return d
}

func denseQuery(from, to string, departure int64, variant int, accessible bool) model.Query {
	cap := []int64{0, 3, 12}[variant%3]
	return model.Query{ID: "dense-query", Origin: from, Destination: to, Departure: departure, LatestArrival: 30, TransferBuffer: int64(variant % 6), MaxWalkSeconds: 1000, MaxBoardings: variant % 4, MaxLegs: 128, AccessibleOnly: accessible, Walking: &model.WalkingLimits{Access: cap, Egress: cap, Direct: cap, Transfer: cap}}
}
func denseError(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}
func checkDenseResult(t *testing.T, d model.Dataset, q model.Query, got CappedResult, e error, want CappedResult, we error) {
	t.Helper()
	if denseError(e) != denseError(we) || !reflect.DeepEqual(got, want) {
		t.Fatalf("q=%+v got=%+v (%v) want=%+v (%v)", q, got, e, want, we)
	}
	if e == nil {
		checker, err := validate.New(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, j := range got.Journeys {
			if _, err = checker.Check(q, j); err != nil {
				t.Fatalf("invalid full raw witness: %v; %+v", err, j)
			}
		}
	}
}
func TestCappedDenseFrozenTinyNetworks(t *testing.T) {
	checks := 0
	for seed := uint64(0); seed < 48; seed++ {
		d := denseDataset(seed)
		for _, accessible := range []bool{false, true} {
			cfg := CappedConfig{TransferWalkingLimit: 12, AccessibleOnly: accessible, Limits: denseLimits()}
			x, e := BuildCapped(context.Background(), d, cfg)
			f, fe := frozenBuildCapped(context.Background(), d, cfg)
			if denseError(e) != denseError(fe) || !reflect.DeepEqual(x.Preparation(), f.Preparation()) {
				t.Fatalf("preparation seed=%d got=%+v (%v) want=%+v (%v)", seed, x.Preparation(), e, f.Preparation(), fe)
			}
			if e != nil {
				t.Fatal(e)
			}
			for _, from := range d.Stops {
				for _, to := range d.Stops {
					for variant := 0; variant < 6; variant++ {
						q := denseQuery(from.ID, to.ID, int64(variant%2)*4, variant, accessible)
						got, e := x.Search(context.Background(), q, denseLimits())
						want, we := f.Search(context.Background(), q, denseLimits())
						checkDenseResult(t, d, q, got, e, want, we)
						checks++
					}
				}
			}
		}
	}
	t.Logf("%d exact original-result/Stats/error and independent full-witness checks", checks)
}

// Counted cancellation checks poll placement, not wall-clock race behavior.
type denseCancelContext struct{ after, calls int }

func (c *denseCancelContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (c *denseCancelContext) Done() <-chan struct{}       { return nil }
func (c *denseCancelContext) Value(any) any               { return nil }
func (c *denseCancelContext) Err() error {
	c.calls++
	if c.calls >= c.after {
		return context.Canceled
	}
	return nil
}
func TestCappedDenseFrozenBudgetsAndCancellation(t *testing.T) {
	d := denseDataset(1)
	cfg := CappedConfig{TransferWalkingLimit: 12, Limits: denseLimits()}
	x, e := BuildCapped(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f, e := frozenBuildCapped(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	q := denseQuery("s0", "s4", 0, 3, false)
	q.Walking = &model.WalkingLimits{Access: 3, Egress: 3, Direct: 3, Transfer: 12}
	limits := []func(*Limits, int){func(l *Limits, n int) { l.MaxRuns = n }, func(l *Limits, n int) { l.MaxConnectionVisits = n }, func(l *Limits, n int) { l.MaxWalkSettled = n }, func(l *Limits, n int) { l.MaxWalkEdges = n }, func(l *Limits, n int) { l.MaxShortcuts = n }, func(l *Limits, n int) { l.MaxOutputLegs = n }}
	for _, set := range limits {
		for _, n := range []int{1, 2, 4, 7, 16, 64, 256} {
			l := denseLimits()
			set(&l, n)
			got, e := x.Search(context.Background(), q, l)
			want, we := f.Search(context.Background(), q, l)
			checkDenseResult(t, d, q, got, e, want, we)
			c := cfg
			c.Limits = l
			gx, e := BuildCapped(context.Background(), d, c)
			fx, we := frozenBuildCapped(context.Background(), d, c)
			if denseError(e) != denseError(we) || !reflect.DeepEqual(gx.Preparation(), fx.Preparation()) {
				t.Fatalf("budget=%+v preparation differs: %+v (%v), %+v (%v)", l, gx.Preparation(), e, fx.Preparation(), we)
			}
		}
	}
	for after := 1; after <= 350; after++ {
		gc, wc := &denseCancelContext{after: after}, &denseCancelContext{after: after}
		got, e := x.Search(gc, q, denseLimits())
		want, we := f.Search(wc, q, denseLimits())
		checkDenseResult(t, d, q, got, e, want, we)
		if gc.calls != wc.calls {
			t.Fatalf("search cancellation poll count %d != %d at %d", gc.calls, wc.calls, after)
		}
		gc, wc = &denseCancelContext{after: after}, &denseCancelContext{after: after}
		gx, e := BuildCapped(gc, d, cfg)
		fx, we := frozenBuildCapped(wc, d, cfg)
		if denseError(e) != denseError(we) || !reflect.DeepEqual(gx.Preparation(), fx.Preparation()) || gc.calls != wc.calls {
			t.Fatalf("preparation cancellation differs at %d: %+v (%v)/%d, %+v (%v)/%d", after, gx.Preparation(), e, gc.calls, fx.Preparation(), we, wc.calls)
		}
	}
}
func TestCappedDenseFrozenSparseCertificates(t *testing.T) {
	for seed := uint64(0); seed < 24; seed++ {
		d := denseDataset(seed)
		for _, accessible := range []bool{false, true} {
			cfg := SparseConfig{Capped: CappedConfig{TransferWalkingLimit: 12, AccessibleOnly: accessible, Limits: denseLimits()}, TransferBuffer: 4, MaxProofChecks: 1000000}
			x, e := BuildSparse(context.Background(), d, cfg)
			f, fe := frozenBuildSparse(context.Background(), d, cfg)
			if denseError(e) != denseError(fe) || !reflect.DeepEqual(x.Preparation(), f.Preparation()) {
				t.Fatalf("sparse certificates seed=%d differ: %+v (%v), %+v (%v)", seed, x.Preparation(), e, f.Preparation(), fe)
			}
			if e != nil {
				t.Fatal(e)
			}
			for _, from := range d.Stops {
				for _, to := range d.Stops {
					q := denseQuery(from.ID, to.ID, 0, 5, accessible)
					q.MaxBoardings = 3
					q.TransferBuffer = 4
					got, e := x.Search(context.Background(), q, denseLimits())
					want, we := f.Search(context.Background(), q, denseLimits())
					checkDenseResult(t, d, q, got, e, want, we)
				}
			}
		}
	}
}
func TestCappedDenseImmutableConcurrentQueries(t *testing.T) {
	d := denseDataset(1)
	cfg := CappedConfig{TransferWalkingLimit: 12, Limits: denseLimits()}
	x, e := BuildCapped(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	q := denseQuery("s0", "s4", 0, 5, false)
	q.MaxBoardings = 3
	want, e := x.Search(context.Background(), q, denseLimits())
	if e != nil {
		t.Fatal(e)
	}
	// All retained timetable/witness metadata must belong to the prepared clone.
	for i := range d.Connections {
		d.Connections[i].Departure = 500
		d.Connections[i].Arrival = 501
	}
	for i := range d.Footpaths {
		d.Footpaths[i].Duration = 500
	}
	for i := range d.Stops {
		d.Stops[i].Accessible = model.No
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				got, e := x.Search(context.Background(), q, denseLimits())
				if e != nil || !reflect.DeepEqual(got, want) {
					t.Errorf("concurrent immutable query differs: %+v %v", got, e)
					return
				}
			}
		}()
	}
	wg.Wait()
}

func TestCappedDenseExplicitZeroAndDestinationRepair(t *testing.T) {
	for _, kind := range []string{"default", "zero", "minimum", "forbidden"} {
		d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: "destination-repair", TimeZone: "Etc/UTC", ServiceDate: "2026-10-19"}
		for _, id := range []string{"A", "B", "C", "D", "X", "Y", "P"} {
			s := model.Stop{ID: id, Accessible: model.Yes}
			if id == "P" {
				s.LocationType = 1
			}
			if id == "B" || id == "C" {
				s.ParentStation = "P"
			}
			d.Stops = append(d.Stops, s)
		}
		for _, v := range []struct {
			id, from, to string
			dep, arr     int64
		}{{"feeder", "A", "B", 1, 2}, {"aa-service", "C", "D", 6, 7}, {"zz-service", "C", "D", 6, 7}, {"later", "C", "D", 9, 10}} {
			d.Trips = append(d.Trips, model.Trip{ID: v.id, Accessible: model.Yes})
			d.Connections = append(d.Connections, model.Connection{ID: v.id + ":0", TripID: v.id, From: v.from, To: v.to, Departure: v.dep, Arrival: v.arr, Pickup: true, Dropoff: true})
		}
		for _, v := range []struct {
			id, from, to string
			n            int64
		}{{"bd", "B", "D", 1}, {"dc", "D", "C", 1}, {"bx", "B", "X", 2}, {"xc", "X", "C", 2}, {"by", "B", "Y", 2}, {"yc", "Y", "C", 2}} {
			d.Footpaths = append(d.Footpaths, model.Footpath{ID: v.id, From: v.from, To: v.to, Duration: v.n, Accessible: model.Yes})
		}
		if kind != "default" {
			r := model.TransferRule{ID: "pair", FromStop: "P", ToStop: "P"}
			if kind == "minimum" {
				r.Type = 2
				r.MinSeconds = 6
			}
			if kind == "forbidden" {
				r.Type = 3
			}
			d.Transfers = append(d.Transfers, r)
		}
		cfg := CappedConfig{TransferWalkingLimit: 4, Limits: denseLimits()}
		x, e := BuildCapped(context.Background(), d, cfg)
		if e != nil {
			t.Fatal(e)
		}
		f, e := frozenBuildCapped(context.Background(), d, cfg)
		if e != nil {
			t.Fatal(e)
		}
		q := model.Query{ID: kind, Origin: "A", Destination: "D", LatestArrival: 20, TransferBuffer: 9, MaxWalkSeconds: 100, MaxBoardings: 2, MaxLegs: 128, Walking: &model.WalkingLimits{Transfer: 4}}
		got, e := x.Search(context.Background(), q, denseLimits())
		want, we := f.Search(context.Background(), q, denseLimits())
		checkDenseResult(t, d, q, got, e, want, we)
		if got.Stats.DestinationRepairs == 0 {
			t.Fatal("destination-crossing shortest witness was not repaired")
		}
		refs := []string{}
		if len(got.Journeys) > 0 {
			for _, l := range got.Journeys[0].Legs {
				refs = append(refs, l.Ref)
			}
		}
		expected := []string{}
		if kind == "zero" {
			expected = []string{"feeder:0", "bx", "xc", "aa-service:0"}
		}
		if kind == "minimum" {
			expected = []string{"feeder:0", "bx", "xc", "later:0"}
		}
		if !reflect.DeepEqual(refs, expected) {
			t.Fatalf("%s witness %v != independently expected %v", kind, refs, expected)
		}
	}
}

func TestCappedDenseFrozenCapabilityGuards(t *testing.T) {
	d := denseDataset(1)
	cfg := CappedConfig{TransferWalkingLimit: 12, Limits: denseLimits()}
	x, e := BuildCapped(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	f, e := frozenBuildCapped(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	base := denseQuery("s0", "s4", 0, 5, false)
	changes := []func(*model.Query){func(q *model.Query) { q.Walking = nil }, func(q *model.Query) { q.Origin = "missing" }, func(q *model.Query) { q.AccessibleOnly = true }, func(q *model.Query) { q.Walking.Transfer = 13 }, func(q *model.Query) { q.MaxWalkSeconds = 0 }, func(q *model.Query) { q.MaxLegs = 0 }, func(q *model.Query) { q.Origin = q.Destination }}
	for _, change := range changes {
		q := base
		w := *q.Walking
		q.Walking = &w
		change(&q)
		got, e := x.Search(context.Background(), q, denseLimits())
		want, we := f.Search(context.Background(), q, denseLimits())
		checkDenseResult(t, d, q, got, e, want, we)
	}
	got, e := x.Search(context.Background(), base, Limits{})
	want, we := f.Search(context.Background(), base, Limits{})
	checkDenseResult(t, d, base, got, e, want, we)
	var nilDense *CappedIndex
	var nilFrozen *frozenCappedIndex
	got, e = nilDense.Search(context.Background(), base, denseLimits())
	want, we = nilFrozen.Search(context.Background(), base, denseLimits())
	checkDenseResult(t, d, base, got, e, want, we)
}
