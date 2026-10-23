package ultra

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

type walkTieVector struct {
	arrival int64
	boards  int
}

func walkTieMetrics(t testing.TB, d model.Dataset, q model.Query, js []model.Journey) map[walkTieVector]int64 {
	t.Helper()
	c, e := validate.New(d)
	if e != nil {
		t.Fatal(e)
	}
	m := map[walkTieVector]int64{}
	for _, j := range js {
		v, e := c.Check(q, j)
		if e != nil {
			t.Fatal(e, j)
		}
		k := walkTieVector{v.Metrics.Arrival, v.Metrics.Boardings}
		if old, ok := m[k]; !ok || v.Metrics.WalkingSeconds < old {
			m[k] = v.Metrics.WalkingSeconds
		}
	}
	return m
}
func walkTieKeys(m map[walkTieVector]int64) []walkTieVector {
	v := []walkTieVector{}
	for k := range m {
		v = append(v, k)
	}
	sort.Slice(v, func(i, j int) bool {
		if v[i].boards != v[j].boards {
			return v[i].boards < v[j].boards
		}
		return v[i].arrival < v[j].arrival
	})
	return v
}
func walkTieFixture() (model.Dataset, model.Query) {
	d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: "local-ties", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-20"}
	for _, id := range []string{"O", "A", "X", "D"} {
		d.Stops = append(d.Stops, model.Stop{ID: id, Accessible: model.Yes})
	}
	q := model.Query{ID: "tie", Origin: "O", Destination: "D", Departure: 0, LatestArrival: 40, MaxBoardings: 2, MaxLegs: 100, MaxWalkSeconds: 100, Walking: &model.WalkingLimits{Access: 5, Transfer: 5, Egress: 5, Direct: 5}}
	return d, q
}
func walkTieTrip(d *model.Dataset, id, from, to string, dep, arr int64) {
	d.Trips = append(d.Trips, model.Trip{ID: id, Accessible: model.Yes})
	d.Connections = append(d.Connections, model.Connection{ID: id, TripID: id, From: from, To: to, Departure: dep, Arrival: arr, Pickup: true, Dropoff: true})
}

func TestCappedLocalWalkTiesRefinesWithoutChanging2DVectors(t *testing.T) {
	d, q := walkTieFixture()
	d.Footpaths = []model.Footpath{{ID: "access", From: "O", To: "A", Duration: 5, Accessible: model.Yes}}
	walkTieTrip(&d, "walking-first", "A", "D", 10, 20)
	walkTieTrip(&d, "seated-tie", "O", "D", 0, 20)
	x, e := BuildCapped(context.Background(), d, CappedConfig{TransferWalkingLimit: 5, Limits: denseLimits()})
	if e != nil {
		t.Fatal(e)
	}
	base, e := x.Search(context.Background(), q, denseLimits())
	if e != nil {
		t.Fatal(e)
	}
	got, e := x.SearchLocalWalkTies(context.Background(), q, denseLimits())
	if e != nil || !got.Complete {
		t.Fatal(e, got)
	}
	bm, gm := walkTieMetrics(t, d, q, base.Journeys), walkTieMetrics(t, d, q, got.Journeys)
	if !reflect.DeepEqual(walkTieKeys(bm), walkTieKeys(gm)) || bm[walkTieVector{20, 1}] != 5 || gm[walkTieVector{20, 1}] != 0 {
		t.Fatal("tie not refined", bm, gm)
	}
}

// Equal-time refinements must not be mistaken for a third Pareto criterion.
// The later, less-walking readiness at A is deliberately discarded by native
// 2D scanning, even though it can catch the same final trip.
func TestCappedLocalWalkTiesExplicitlyNotMinimumWalkingRepresentative(t *testing.T) {
	d, q := walkTieFixture()
	d.Footpaths = []model.Footpath{{ID: "access", From: "O", To: "X", Duration: 2, Accessible: model.Yes}}
	walkTieTrip(&d, "fast-walking", "X", "A", 2, 3)
	walkTieTrip(&d, "slower-seated", "O", "A", 0, 5)
	walkTieTrip(&d, "outbound", "A", "D", 10, 20)
	x, e := BuildCapped(context.Background(), d, CappedConfig{TransferWalkingLimit: 5, Limits: denseLimits()})
	if e != nil {
		t.Fatal(e)
	}
	got, e := x.SearchLocalWalkTies(context.Background(), q, denseLimits())
	if e != nil {
		t.Fatal(e)
	}
	m := walkTieMetrics(t, d, q, got.Journeys)
	if walk, ok := m[walkTieVector{20, 2}]; !ok || walk != 2 {
		t.Fatal("expected native later-readiness limitation", m)
	}
	checker, e := validate.New(d)
	if e != nil {
		t.Fatal(e)
	}
	lessWalking := model.Journey{Legs: []model.Leg{
		{Kind: "ride", Ref: "slower-seated", Departure: 0, Arrival: 5},
		{Kind: "ride", Ref: "outbound", Departure: 10, Arrival: 20},
	}}
	v, e := checker.Check(q, lessWalking)
	if e != nil || v.Metrics.Arrival != 20 || v.Metrics.Boardings != 2 || v.Metrics.WalkingSeconds != 0 {
		t.Fatal("independent less-walking witness invalid", v, e)
	}
}

func TestCappedLocalWalkTiesPreservesCapabilityGuards(t *testing.T) {
	d := denseDataset(1)
	x, e := BuildCapped(context.Background(), d, CappedConfig{TransferWalkingLimit: 12, Limits: denseLimits()})
	if e != nil {
		t.Fatal(e)
	}
	base := denseQuery("s0", "s4", 0, 5, false)
	changes := []func(*model.Query){
		func(q *model.Query) { q.Walking = nil },
		func(q *model.Query) { q.Origin = "missing" },
		func(q *model.Query) { q.AccessibleOnly = true },
		func(q *model.Query) { q.Walking.Transfer = 13 },
		func(q *model.Query) { q.MaxWalkSeconds = 0 },
		func(q *model.Query) { q.MaxLegs = 0 },
	}
	for _, change := range changes {
		q := base
		walking := *q.Walking
		q.Walking = &walking
		change(&q)
		want, we := x.Search(context.Background(), q, denseLimits())
		got, ge := x.SearchLocalWalkTies(context.Background(), q, denseLimits())
		if ge == nil || denseError(ge) != denseError(we) || !reflect.DeepEqual(got, want) {
			t.Fatalf("capability guard changed: got=%+v (%v), want=%+v (%v)", got, ge, want, we)
		}
	}
	for _, index := range []*CappedIndex{nil, {}} {
		want, we := index.Search(context.Background(), base, denseLimits())
		got, ge := index.SearchLocalWalkTies(context.Background(), base, denseLimits())
		if ge == nil || denseError(ge) != denseError(we) || !reflect.DeepEqual(got, want) {
			t.Fatal("incomplete-index guard changed", got, ge, want, we)
		}
	}
	want, we := x.Search(context.Background(), base, Limits{})
	got, ge := x.SearchLocalWalkTies(context.Background(), base, Limits{})
	if ge == nil || denseError(ge) != denseError(we) || !reflect.DeepEqual(got, want) {
		t.Fatal("query-budget guard changed", got, ge, want, we)
	}
}

func TestSparseLocalWalkTiesPreservesGuardsAndNative2DVectors(t *testing.T) {
	base := denseQuery("s0", "s4", 0, 5, false)
	for _, index := range []*SparseIndex{nil, {}} {
		want, we := index.Search(context.Background(), base, denseLimits())
		got, ge := index.SearchLocalWalkTies(context.Background(), base, denseLimits())
		if ge == nil || denseError(ge) != denseError(we) || !reflect.DeepEqual(got, want) {
			t.Fatal("incomplete sparse guard changed", got, ge, want, we)
		}
	}
	for seed := uint64(0); seed < 24; seed++ {
		d := denseDataset(seed)
		for _, accessible := range []bool{false, true} {
			x, e := BuildSparse(context.Background(), d, SparseConfig{
				Capped:         CappedConfig{TransferWalkingLimit: 12, AccessibleOnly: accessible, Limits: denseLimits()},
				TransferBuffer: 4, MaxProofChecks: 1000000,
			})
			if e != nil {
				t.Fatal(e)
			}
			q := base
			q.AccessibleOnly = accessible
			want, we := x.Search(context.Background(), q, denseLimits())
			got, ge := x.SearchLocalWalkTies(context.Background(), q, denseLimits())
			if ge == nil || denseError(ge) != denseError(we) || !reflect.DeepEqual(got, want) {
				t.Fatal("fixed-buffer proof guard changed", got, ge, want, we)
			}
			for _, pair := range [][2]string{{"s0", "s4"}, {"s1", "s5"}, {"s4", "s2"}, {"s2", "s3"}, {"s0", "s0"}} {
				q.Origin, q.Destination, q.TransferBuffer = pair[0], pair[1], 4
				want, we := x.Search(context.Background(), q, denseLimits())
				got, ge := x.SearchLocalWalkTies(context.Background(), q, denseLimits())
				if we != nil || ge != nil || !got.Complete {
					t.Fatal("native sparse search failed", ge, we)
				}
				wm, gm := walkTieMetrics(t, d, q, want.Journeys), walkTieMetrics(t, d, q, got.Journeys)
				if !reflect.DeepEqual(walkTieKeys(wm), walkTieKeys(gm)) {
					t.Fatal("sparse 2D frontier changed", seed, pair, wm, gm)
				}
				for k, walking := range wm {
					if gm[k] > walking {
						t.Fatal("sparse walking tie worsened", seed, pair, wm, gm)
					}
				}
			}
		}
	}
}

func TestCappedLocalWalkTiesNative2DAndWeakWalkingDominance(t *testing.T) {
	for seed := uint64(0); seed < 64; seed++ {
		d := denseDataset(seed)
		for _, accessible := range []bool{false, true} {
			x, e := BuildCapped(context.Background(), d, CappedConfig{TransferWalkingLimit: 12, AccessibleOnly: accessible, Limits: denseLimits()})
			if e != nil {
				t.Fatal(e)
			}
			for _, pair := range [][2]string{{"s0", "s4"}, {"s1", "s5"}, {"s4", "s2"}, {"s2", "s3"}, {"s0", "s0"}} {
				for variant := 0; variant < 6; variant++ {
					q := denseQuery(pair[0], pair[1], int64(variant%2)*4, variant, accessible)
					base, e := x.Search(context.Background(), q, denseLimits())
					if e != nil {
						t.Fatal(e)
					}
					got, e := x.SearchLocalWalkTies(context.Background(), q, denseLimits())
					if e != nil {
						t.Fatal(e)
					}
					bm, gm := walkTieMetrics(t, d, q, base.Journeys), walkTieMetrics(t, d, q, got.Journeys)
					if !reflect.DeepEqual(walkTieKeys(bm), walkTieKeys(gm)) {
						t.Fatalf("2D vectors changed seed=%d accessible=%v variant=%d %v %v", seed, accessible, variant, bm, gm)
					}
					for k, w := range bm {
						if gm[k] > w {
							t.Fatalf("walking worsened seed=%d %v %v", seed, bm, gm)
						}
					}
				}
			}
		}
	}
}

func TestCappedLocalWalkTiesQueryOwnershipCancellation(t *testing.T) {
	d := denseDataset(82)
	q := denseQuery("s0", "s4", 0, 3, false)
	x, e := BuildCapped(context.Background(), d, CappedConfig{TransferWalkingLimit: 12, Limits: denseLimits()})
	if e != nil {
		t.Fatal(e)
	}
	want, e := x.SearchLocalWalkTies(context.Background(), q, denseLimits())
	if e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, e := x.SearchLocalWalkTies(context.Background(), q, denseLimits())
			if e != nil || !reflect.DeepEqual(want, got) {
				t.Error("concurrent difference", e)
			}
		}()
	}
	wg.Wait()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, e := x.SearchLocalWalkTies(ctx, q, denseLimits())
	if e == nil || got.Complete {
		t.Fatal("cancel ignored")
	}
}
