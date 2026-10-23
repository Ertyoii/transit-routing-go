package raptor

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"reflect"
	"slices"
	"testing"
)

func v2Equal(t testing.TB, x *Index, q model.Query, polls int, early bool, labels, connections int) {
	t.Helper()
	a, ap, ae := x.frozenV2Search(denseContext(polls), q, early)
	b, bp, be := x.search(denseContext(polls), q, early)
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(ap, bp) || fmt.Sprint(ae) != fmt.Sprint(be) {
		t.Fatalf("V2 point difference polls=%d early=%v q=%+v\nold=%+v %v %v\nnew=%+v %v %v", polls, early, q, a, ap, ae, b, bp, be)
	}
	cfg := MultiConfig{EarlyPruning: early, Criterion: "walking_seconds", MaxLabels: labels, MaxConnectionVisits: connections}
	am, ae := x.frozenV2Multi(denseContext(polls), q, cfg)
	bm, be := x.SearchMulti(denseContext(polls), q, cfg)
	if !reflect.DeepEqual(am, bm) || fmt.Sprint(ae) != fmt.Sprint(be) {
		t.Fatalf("V2 multi difference polls=%d cfg=%+v q=%+v\nold=%+v %v\nnew=%+v %v", polls, cfg, q, am, ae, bm, be)
	}
}

func TestV2RoundStorageExactBaseline(t *testing.T) {
	for seed := uint64(0); seed < 90; seed++ {
		d, q := indexedFixture(seed, int(seed%9))
		x, e := New(d)
		if e != nil {
			t.Fatal(e)
		}
		q.MaxBoardings = 4
		q.AccessibleOnly = seed%3 == 0
		for _, early := range []bool{false, true} {
			v2Equal(t, x, q, -1, early, 10000, 10000)
			for _, limits := range [][2]int{{1, 10000}, {7, 10000}, {10000, 1}, {10000, 5}} {
				v2Equal(t, x, q, -1, early, limits[0], limits[1])
			}
			for polls := 1; polls <= 500; polls += 7 {
				v2Equal(t, x, q, polls, early, 10000, 10000)
			}
		}
	}
}

func TestV2RouteStorageResetsTouchedEntries(t *testing.T) {
	d, _ := denseFixture(82)
	x, e := New(d)
	if e != nil {
		t.Fatal(e)
	}
	positions := make([]int, len(x.routes))
	routes := []int{}
	for _, names := range [][]string{{"A", "B", "C"}, {"E"}, {"Z"}, {"B", "C"}, {"A"}} {
		stops := []int{}
		for _, name := range names {
			stops = append(stops, x.stopIDs[name])
		}
		wp, wr := x.collectRoutes(stops)
		positions, routes = x.collectRoutesInto(stops, positions, routes)
		if !slices.Equal(wp, positions) || !slices.Equal(wr, routes) {
			t.Fatal("route reuse changed minima/order", names, wp, positions, wr, routes)
		}
	}
}

func BenchmarkV2SearchRoundStorage(b *testing.B) {
	d, q := denseFixture(82)
	x, e := New(d)
	if e != nil {
		b.Fatal(e)
	}
	cfg := MultiConfig{Criterion: "walking_seconds", MaxLabels: 10000, MaxConnectionVisits: 10000}
	for _, v2 := range []bool{false, true} {
		name := "frozen"
		if v2 {
			name = "round_reuse"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				if v2 {
					_, e = x.SearchMulti(context.Background(), q, cfg)
				} else {
					_, e = x.frozenV2Multi(context.Background(), q, cfg)
				}
				if e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
