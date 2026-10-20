package tripbased

import (
	"context"
	"reflect"
	"testing"

	"github.com/ertyoii/transit-routing-go/model"
)

func TestRetainedCappedSeedChunkAndOrder(t *testing.T) {
	c := capCases(t)[0]
	d := c.Dataset.Clone()
	two := d.Trips[0]
	two.ID += "-equal-ready"
	d.Trips = append(d.Trips, two)
	conn := d.Connections[0]
	conn.ID += "-equal-ready"
	conn.TripID = two.ID
	conn.Arrival--
	d.Connections = append(d.Connections, conn)
	cfg := DestinationRetainedCappedConfig{Scope: capConfig(c), MaxPendingSegments: 10000, MaxSourceSeeds: 10000}
	cfg.Scope.MaxProfileRecords = 100
	x, e := BuildDestinationRetainedCappedPatterns(context.Background(), d, cfg)
	if e != nil {
		t.Fatal(e)
	}
	for _, order := range [][]int{{0, 1}, {1, 0}} {
		for _, chunk := range []int{1, 2} {
			// Exercise the real private owning state with valid immutable base data.
			// No production chunking flag or test-only hook is introduced.
			p := &PatternIndex{base: x.p.base, cfg: x.p.cfg, connections: x.p.connections, footpaths: x.p.footpaths, dags: map[string]*PatternDAG{}, prefix: map[string]map[patternPrefix]int{}, seen: map[string]bool{}, snap: PatternSnapshot{PhasesNS: map[string]int64{}}}
			y := &DestinationCappedPatternIndex{p: p, cfg: cfg.Scope, retained: &DestinationRetainedCappedSnapshot{Config: cfg}}
			s, e := y.newCappedRetainedSweep(context.Background(), p.base.ids[c.Query.Origin])
			if e != nil {
				t.Fatal(e)
			}
			walk := p.base.walkMap[s.source][s.source]
			for i, ti := range order {
				if e = s.enqueue(1, ti, 0, nil, walk); e != nil {
					t.Fatal(e)
				}
				if (i+1)%chunk == 0 {
					if e = s.propagate(); e != nil {
						t.Fatal(e)
					}
				}
			}
			if e = s.emit(d.Connections[0].Departure); e != nil {
				t.Fatal(e)
			}
			want := []int64{1 << 62, conn.Arrival, conn.Arrival, conn.Arrival, conn.Arrival}
			if got := y.retained.Profiles[0].Frontier; !reflect.DeepEqual(got, want) {
				t.Fatal(order, chunk, got, want)
			}
			for _, w := range p.snap.Witnesses {
				q := c.Query
				q.Departure = w.Departure
				capVector(t, d, q, []model.Journey{w.Journey})
			}
		}
	}
}
