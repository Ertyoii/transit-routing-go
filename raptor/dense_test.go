package raptor

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"math/rand/v2"
	"reflect"
	"slices"
	"sync"
	"testing"
)

func denseFixture(seed uint64) (model.Dataset, model.Query) {
	r := rand.New(rand.NewPCG(seed, 4171))
	d := model.Dataset{SchemaVersion: model.SchemaVersion, ID: "dense-rounds", TimeZone: "Asia/Singapore", ServiceDate: "2026-10-16"}
	for _, id := range []string{"Z", "A", "B", "C", "D", "E"} {
		d.Stops = append(d.Stops, model.Stop{ID: id, Accessible: model.Yes})
	}
	// Repeated B makes minimum boarding position matter when multiple stops activate a route.
	routes := [][]string{{"A", "B", "C", "B", "D"}, {"A", "B", "C", "B", "D"}, {"B", "D"}, {"C", "D"}, {"A", "E", "D"}}
	for ti, stops := range routes {
		id := fmt.Sprint("t", ti)
		d.Trips = append(d.Trips, model.Trip{ID: id, Accessible: model.Yes})
		at := int64(ti*2 + r.IntN(3))
		for j := 0; j < len(stops)-1; j++ {
			dep := at
			at += int64(1 + r.IntN(4))
			d.Connections = append(d.Connections, model.Connection{ID: fmt.Sprintf("%s-%d", id, j), TripID: id, Sequence: j, From: stops[j], To: stops[j+1], Departure: dep, Arrival: at, Pickup: j == 0 || r.IntN(3) != 0, Dropoff: j == len(stops)-2 || r.IntN(3) != 0})
		}
	}
	for i, pair := range [][2]string{{"A", "B"}, {"C", "E"}, {"B", "C"}, {"E", "D"}} {
		d.Footpaths = append(d.Footpaths, model.Footpath{ID: fmt.Sprint("w", i), From: pair[0], To: pair[1], Duration: int64(1 + r.IntN(4)), Accessible: model.Yes})
	}
	d.Transfers = []model.TransferRule{{ID: "minimum", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: int64(r.IntN(8))}}
	// Stop ordinal assignment may follow any input permutation without changing output.
	r.Shuffle(len(d.Stops), func(i, j int) { d.Stops[i], d.Stops[j] = d.Stops[j], d.Stops[i] })
	q := model.Query{ID: "q", Origin: "A", Destination: "D", Departure: 0, LatestArrival: 40, MaxBoardings: 3, MaxLegs: 128, MaxWalkSeconds: 100, TransferBuffer: int64(r.IntN(3)), Walking: &model.WalkingLimits{Access: 5, Transfer: 5, Egress: 5, Direct: 5}}
	return d, q
}

// Cancel after a fixed number of the public Context.Err polls. This checks partial
// results and exact cancellation boundaries, independently of wall-clock speed.
type denseCancel struct {
	context.Context
	left int
}

func (c *denseCancel) Err() error {
	c.left--
	if c.left <= 0 {
		return context.Canceled
	}
	return nil
}
func denseContext(polls int) context.Context {
	if polls < 0 {
		return context.Background()
	}
	return &denseCancel{context.Background(), polls}
}
func denseEqual(t testing.TB, x *Index, q model.Query, polls int, early bool, labels, connections int) {
	t.Helper()
	a, ap, ae := x.frozenSearchV1(denseContext(polls), q, early)
	b, bp, be := x.search(denseContext(polls), q, early)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ap, bp) || fmt.Sprint(ae) != fmt.Sprint(be) {
		t.Fatalf("point mismatch q=%+v polls=%d early=%v\nold=%+v %v %v\nnew=%+v %v %v", q, polls, early, a, ap, ae, b, bp, be)
	}
	cfg := MultiConfig{EarlyPruning: early, Criterion: "walking_seconds", MaxLabels: labels, MaxConnectionVisits: connections}
	am, ae := x.frozenMultiV1(denseContext(polls), q, cfg)
	bm, be := x.SearchMulti(denseContext(polls), q, cfg)
	if !reflect.DeepEqual(am, bm) || fmt.Sprint(ae) != fmt.Sprint(be) {
		t.Fatalf("multi mismatch q=%+v polls=%d cfg=%+v\nold=%+v %v\nnew=%+v %v", q, polls, cfg, am, ae, bm, be)
	}
}

func TestDenseRoundsFullAndPartialResults(t *testing.T) {
	for seed := uint64(0); seed < 48; seed++ {
		d, q := denseFixture(seed)
		x, e := New(d)
		if e != nil {
			t.Fatal(e)
		}
		for _, early := range []bool{false, true} {
			for _, limits := range [][2]int{{10000, 10000}, {1, 10000}, {7, 10000}, {10000, 1}, {10000, 5}} {
				denseEqual(t, x, q, -1, early, limits[0], limits[1])
			}
			for _, polls := range []int{1, 3, 20, 70, 150, 300, 600} {
				denseEqual(t, x, q, polls, early, 10000, 10000)
			}
			for _, pair := range [][2]string{{"Z", "D"}, {"A", "Z"}, {"B", "C"}, {"A", "A"}, {"missing", "D"}} {
				v := q
				v.Origin, v.Destination = pair[0], pair[1]
				denseEqual(t, x, v, -1, early, 10000, 10000)
			}
		}
	}
}

func TestDenseRoundsOwnedIndexAndConcurrentQueries(t *testing.T) {
	d, q := denseFixture(82)
	x, e := New(d)
	if e != nil {
		t.Fatal(e)
	}
	cfg := MultiConfig{EarlyPruning: true, Criterion: "walking_seconds", MaxLabels: 10000, MaxConnectionVisits: 10000}
	old, e := x.SearchMulti(context.Background(), q, cfg)
	if e != nil || !old.Complete || len(old.Journeys) == 0 {
		t.Fatalf("fixture needs output: %v %+v", e, old)
	}
	// Mutation must not change the index's owned route/ordinal correspondence.
	d.Stops[0].ID = "caller-mutated"
	d.Connections[0].From = "caller-mutated"
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := q
			if i%2 == 1 {
				v.Origin, v.Destination = "B", "C"
			}
			denseEqual(t, x, v, -1, i%3 == 0, 10000, 10000)
		}(i)
	}
	wg.Wait()
	again, e := x.SearchMulti(context.Background(), q, cfg)
	if e != nil || !reflect.DeepEqual(old, again) {
		t.Fatal("result/index changed", e)
	}
	d2, q2 := denseFixture(82)
	slices.Reverse(d2.Stops)
	y, e := New(d2)
	if e != nil {
		t.Fatal(e)
	}
	permuted, e := y.SearchMulti(context.Background(), q2, cfg)
	if e != nil || !reflect.DeepEqual(old, permuted) {
		t.Fatal("stop order changed outputs", e)
	}
}

func FuzzDenseRounds(f *testing.F) {
	for _, s := range []uint64{0, 1, 13, 82, 193} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, seed uint64) {
		d, q := denseFixture(seed)
		x, e := New(d)
		if e != nil {
			t.Fatal(e)
		}
		denseEqual(t, x, q, -1, seed%2 == 0, 10000, 10000)
	})
}
