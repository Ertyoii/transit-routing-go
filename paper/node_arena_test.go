package paper

import (
	"github.com/ertyoii/transit-routing-go/model"
	"reflect"
	"runtime"
	"testing"
)

func TestNodeArenaStableOwnedLegChains(t *testing.T) {
	var arena NodeArena
	var got, want *Node
	saved := map[int]*Node{}
	for i := 0; i < nodeSlabSize*5+17; i++ {
		c := model.Connection{ID: "ride", Departure: int64(i * 5), Arrival: int64(i*5 + 2)}
		got, want = arena.Ride(got, c), Ride(want, c)
		if i%37 == 0 {
			saved[i] = got
		}
		p := Path{Edges: []model.Footpath{{ID: "w1", Duration: 1}, {ID: "w2", Duration: 2}}}
		got, want = arena.Append(p, got, int64(i*5+2)), p.Append(want, int64(i*5+2))
	}
	runtime.GC()
	if !reflect.DeepEqual(Journey(got), Journey(want)) {
		t.Fatal("slab chain changed witness")
	}
	for i, n := range saved {
		if n.Leg.Ref != "ride" || n.Leg.Departure != int64(i*5) {
			t.Fatal("earlier slab reused", i)
		}
	}
	if arena.Append(Path{}, got, 99) != got {
		t.Fatal("empty path changed parent")
	}
	// Returned legs own their values, independent of the query's slab chain.
	j := Journey(got)
	got.Leg.Ref = "mutated"
	if j.Legs[len(j.Legs)-1].Ref == "mutated" {
		t.Fatal("journey aliases nodes")
	}
}

func BenchmarkNodeWitnessAllocation(b *testing.B) {
	c := model.Connection{ID: "ride", Departure: 3, Arrival: 7}
	for _, slabs := range []bool{false, true} {
		name := "individual"
		if slabs {
			name = "slabs"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				var a NodeArena
				var p *Node
				for i := 0; i < 4096; i++ {
					if slabs {
						p = a.Ride(p, c)
					} else {
						p = Ride(p, c)
					}
				}
				runtime.KeepAlive(p)
			}
		})
	}
}
