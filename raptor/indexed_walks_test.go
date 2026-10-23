package raptor

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/transfer"
)

func indexedEqual(t testing.TB, x *Index, q model.Query, polls int, early bool, labels, connections int) {
	t.Helper()
	a, ap, ae := x.frozenIndexedBaselineSearch(denseContext(polls), q, early)
	b, bp, be := x.search(denseContext(polls), q, early)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ap, bp) || fmt.Sprint(ae) != fmt.Sprint(be) {
		t.Fatalf("point mismatch q=%+v polls=%d early=%v\nold=%+v %v %v\nnew=%+v %v %v", q, polls, early, a, ap, ae, b, bp, be)
	}
	cfg := MultiConfig{EarlyPruning: early, Criterion: "walking_seconds", MaxLabels: labels, MaxConnectionVisits: connections}
	am, ae := x.frozenIndexedBaselineMulti(denseContext(polls), q, cfg)
	bm, be := x.SearchMulti(denseContext(polls), q, cfg)
	if !reflect.DeepEqual(am, bm) || fmt.Sprint(ae) != fmt.Sprint(be) {
		t.Fatalf("multi mismatch q=%+v polls=%d cfg=%+v\nold=%+v %v\nnew=%+v %v", q, polls, cfg, am, ae, bm, be)
	}
}

func indexedFixture(seed uint64, rule int) (model.Dataset, model.Query) {
	d, q := denseFixture(seed)
	d.Stops = append(d.Stops, model.Stop{ID: "P", LocationType: 1, Accessible: model.Yes})
	for i := range d.Stops {
		if d.Stops[i].ID == "B" || d.Stops[i].ID == "C" {
			d.Stops[i].ParentStation = "P"
		}
		if seed%3 == 0 && d.Stops[i].ID == "E" {
			d.Stops[i].Accessible = model.No
		}
	}
	for i := range d.Trips {
		if seed%4 == 0 && i == 2 {
			d.Trips[i].Accessible = model.No
		}
	}
	rules := [][]model.TransferRule{
		nil,
		{{ID: "minimum", FromStop: "B", ToStop: "C", Type: 2, MinSeconds: 9}},
		{{ID: "station-default", FromStop: "P", ToStop: "E", Type: 0}},
		{{ID: "station-forbid", FromStop: "P", ToStop: "E", Type: 3}},
		{{ID: "child-station-minimum", FromStop: "B", ToStop: "P", Type: 2, MinSeconds: 7}},
		{{ID: "station-station-minimum", FromStop: "P", ToStop: "P", Type: 2, MinSeconds: 8}},
		{{ID: "station-station-forbid", FromStop: "P", ToStop: "P", Type: 3}},
		{{ID: "stay-forbid", FromStop: "B", ToStop: "B", Type: 3}},
		{{ID: "explicit-zero", FromStop: "B", ToStop: "C", Type: 0}},
	}
	d.Transfers = rules[rule%len(rules)]
	return d, q
}

func TestIndexedWalksExactBaselineAdversarial(t *testing.T) {
	for seed := uint64(0); seed < 18; seed++ {
		for rule := 0; rule < 9; rule++ {
			d, q := indexedFixture(seed, rule)
			x, err := New(d)
			if err != nil {
				t.Fatal(err)
			}
			for _, early := range []bool{false, true} {
				for _, cap := range []int64{0, 1, 3, 5} {
					for _, buffer := range []int64{0, 2, 10} {
						v := q
						v.AccessibleOnly = seed%2 == 0
						v.TransferBuffer = buffer
						v.Walking = &model.WalkingLimits{Access: cap, Transfer: 5 - cap, Egress: cap, Direct: 5}
						indexedEqual(t, x, v, -1, early, 10000, 10000)
					}
				}
				for _, budget := range [][2]int{{1, 10000}, {7, 10000}, {10000, 1}, {10000, 5}, {0, 1}} {
					indexedEqual(t, x, q, -1, early, budget[0], budget[1])
				}
				for _, boards := range []int{0, 1, 2} {
					v := q
					v.MaxBoardings = boards
					indexedEqual(t, x, v, -1, early, 10000, 10000)
				}
				for _, pair := range [][2]string{{"Z", "D"}, {"A", "Z"}, {"A", "A"}, {"missing", "D"}} {
					v := q
					v.Origin, v.Destination = pair[0], pair[1]
					indexedEqual(t, x, v, -1, early, 10000, 10000)
				}
			}
		}
	}
}

func TestIndexedWalksExactCancellationBoundaries(t *testing.T) {
	for _, seed := range []uint64{13, 82, 193} {
		d, q := indexedFixture(seed, 5)
		x, err := New(d)
		if err != nil {
			t.Fatal(err)
		}
		for _, early := range []bool{false, true} {
			for polls := 1; polls <= 300; polls++ {
				indexedEqual(t, x, q, polls, early, 10000, 10000)
			}
		}
	}
}

func TestIndexedWalksOwnedConcurrentQueries(t *testing.T) {
	d, q := indexedFixture(82, 5)
	x, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	// Index construction owns both stop ordinals and the transfer resolver.
	d.Stops[0].ID = "mutated"
	d.Connections[0].To = "mutated"
	d.Transfers[0].MinSeconds = 999
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			v := q
			v.TransferBuffer = int64(i)
			v.Walking = &model.WalkingLimits{Access: 5, Transfer: int64(i % 6), Egress: 5, Direct: 5}
			indexedEqual(t, x, v, -1, i%2 == 0, 10000, 10000)
		}(i)
	}
	wg.Wait()
}

func TestIndexedWalksLazyReadinessAndCachedPolls(t *testing.T) {
	d, q := indexedFixture(82, 5)
	x, err := New(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, early := range []bool{false, true} {
		walker := x.p.Walker(q)
		walks := x.indexedWalks(walker, q, early)
		paths, states, err := walks.paths(context.Background(), "B", x.stopIDs["B"])
		if err != nil || len(paths) == 0 {
			t.Fatal(err, paths)
		}
		for i, p := range paths {
			if states[i].ready || states[i].stop != x.stopIDs[p.To] {
				t.Fatal("metadata resolved early or wrong ordinal")
			}
			for _, arrival := range []int64{0, 3, 17, 100} {
				a, aa, ae := x.p.ReadyTime(q, "B", p.To, arrival, p.Duration)
				b, ba, be := walks.readyTime(&states[i], "B", p, arrival)
				if a != b || aa != ba || fmt.Sprint(ae) != fmt.Sprint(be) {
					t.Fatalf("ready mismatch %q: %v %v %v vs %v %v %v", p.To, a, aa, ae, b, ba, be)
				}
			}
		}
		if _, _, err := walks.paths(denseContext(1), "B", x.stopIDs["B"]); err != context.Canceled {
			t.Fatal("cached path lost cancellation poll", err)
		}
		if walker.Count != 1 {
			t.Fatal("closure count changed", walker.Count)
		}
	}
	// Normally New rejects ambiguous rules. Inject them only to verify that the
	// lazy view still reports the baseline's exact first error and partial stats.
	d.Transfers = []model.TransferRule{
		{ID: "conflict-a", FromStop: "B", ToStop: "C", Type: 0},
		{ID: "conflict-b", FromStop: "B", ToStop: "C", Type: 3},
	}
	x.p.Rules = transfer.New(d)
	for _, early := range []bool{false, true} {
		indexedEqual(t, x, q, -1, early, 10000, 10000)
	}
}
