package raptor

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/validate"
)

type tieVector struct {
	arrival int64
	boards  int
}

func tieMetrics(t testing.TB, d model.Dataset, q model.Query, js []model.Journey) map[tieVector]int64 {
	t.Helper()
	c, e := validate.New(d)
	if e != nil {
		t.Fatal(e)
	}
	m := map[tieVector]int64{}
	for _, j := range js {
		v, e := c.Check(q, j)
		if e != nil {
			t.Fatal(e, j)
		}
		k := tieVector{v.Metrics.Arrival, v.Metrics.Boardings}
		if old, ok := m[k]; !ok || v.Metrics.WalkingSeconds < old {
			m[k] = v.Metrics.WalkingSeconds
		}
	}
	return m
}
func tieKeys(m map[tieVector]int64) []tieVector {
	v := []tieVector{}
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
func tieFixture() (model.Dataset, model.Query) {
	d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: "local-ties", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-20"}
	for _, id := range []string{"O", "A", "X", "D"} {
		d.Stops = append(d.Stops, model.Stop{ID: id, Accessible: model.Yes})
	}
	q := model.Query{ID: "tie", Origin: "O", Destination: "D", Departure: 0, LatestArrival: 40, MaxBoardings: 2, MaxLegs: 100, MaxWalkSeconds: 100, Walking: &model.WalkingLimits{Access: 5, Transfer: 5, Egress: 5, Direct: 5}}
	return d, q
}
func tieTrip(d *model.Dataset, id, from, to string, dep, arr int64) {
	d.Trips = append(d.Trips, model.Trip{ID: id, Accessible: model.Yes})
	d.Connections = append(d.Connections, model.Connection{ID: id, TripID: id, From: from, To: to, Departure: dep, Arrival: arr, Pickup: true, Dropoff: true})
}

func TestLocalWalkTiesRefinesWithoutChanging2DVectors(t *testing.T) {
	d, q := tieFixture()
	d.Footpaths = []model.Footpath{{ID: "access", From: "O", To: "A", Duration: 5, Accessible: model.Yes}}
	tieTrip(&d, "walking-first", "A", "D", 10, 20)
	tieTrip(&d, "seated-tie", "O", "D", 0, 20)
	x, e := New(d)
	if e != nil {
		t.Fatal(e)
	}
	for _, early := range []bool{false, true} {
		base, _, e := x.search(context.Background(), q, early)
		if e != nil {
			t.Fatal(e)
		}
		got, _, e := x.SearchLocalWalkTies(context.Background(), q, early)
		if e != nil || !got.Complete {
			t.Fatal(e, got)
		}
		bm, gm := tieMetrics(t, d, q, base.Journeys), tieMetrics(t, d, q, got.Journeys)
		if !reflect.DeepEqual(tieKeys(bm), tieKeys(gm)) || bm[tieVector{20, 1}] != 5 || gm[tieVector{20, 1}] != 0 {
			t.Fatal("tie not refined", bm, gm)
		}
	}
}

func TestLocalWalkTiesExplicitlyNotMinimumWalkingRepresentative(t *testing.T) {
	d, q := tieFixture()
	d.Footpaths = []model.Footpath{{ID: "access", From: "O", To: "X", Duration: 2, Accessible: model.Yes}}
	tieTrip(&d, "fast-walking", "X", "A", 2, 3)
	tieTrip(&d, "slower-seated", "O", "A", 0, 5)
	tieTrip(&d, "outbound", "A", "D", 10, 20)
	x, e := New(d)
	if e != nil {
		t.Fatal(e)
	}
	local, _, e := x.SearchLocalWalkTies(context.Background(), q, true)
	if e != nil {
		t.Fatal(e)
	}
	multi, e := x.SearchMulti(context.Background(), q, MultiConfig{EarlyPruning: true, Criterion: "walking_seconds", MaxLabels: 10000, MaxConnectionVisits: 10000})
	if e != nil {
		t.Fatal(e)
	}
	lm, mm := tieMetrics(t, d, q, local.Journeys), tieMetrics(t, d, q, multi.Journeys)
	if lm[tieVector{20, 2}] != 2 || mm[tieVector{20, 2}] != 0 {
		t.Fatal("expected declared later-readiness limitation", lm, mm)
	}
}

func TestLocalWalkTiesNative2DAndWeakWalkingDominance(t *testing.T) {
	for seed := uint64(0); seed < 120; seed++ {
		d, q := indexedFixture(seed, int(seed%9))
		q.MaxBoardings = 4
		q.AccessibleOnly = seed%3 == 0
		x, e := New(d)
		if e != nil {
			t.Fatal(e)
		}
		for _, early := range []bool{false, true} {
			base, _, e := x.search(context.Background(), q, early)
			if e != nil {
				t.Fatal(e)
			}
			got, _, e := x.SearchLocalWalkTies(context.Background(), q, early)
			if e != nil {
				t.Fatal(e)
			}
			bm, gm := tieMetrics(t, d, q, base.Journeys), tieMetrics(t, d, q, got.Journeys)
			if !reflect.DeepEqual(tieKeys(bm), tieKeys(gm)) {
				t.Fatalf("2D vectors changed seed=%d early=%v %v %v", seed, early, bm, gm)
			}
			for k, w := range bm {
				if gm[k] > w {
					t.Fatalf("walking worsened seed=%d early=%v %v %v", seed, early, bm, gm)
				}
			}
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		got, _, e := x.SearchLocalWalkTies(canceled, q, true)
		if e == nil || got.Complete {
			t.Fatal("cancel ignored")
		}
	}
}
func FuzzLocalWalkTiesVectors(f *testing.F) {
	for _, s := range []uint64{0, 1, 13, 82, 193} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		d, q := indexedFixture(seed, int(seed%9))
		x, e := New(d)
		if e != nil {
			t.Fatal(e)
		}
		base, _, e := x.search(context.Background(), q, false)
		if e != nil {
			t.Fatal(e)
		}
		got, _, e := x.SearchLocalWalkTies(context.Background(), q, seed%2 == 0)
		if e != nil {
			t.Fatal(e)
		}
		bm, gm := tieMetrics(t, d, q, base.Journeys), tieMetrics(t, d, q, got.Journeys)
		if fmt.Sprint(tieKeys(bm)) != fmt.Sprint(tieKeys(gm)) {
			t.Fatal("vectors changed", bm, gm)
		}
	})
}
