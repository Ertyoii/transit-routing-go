package capverify

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

type Point struct {
	Arrival   int64
	Boardings int
}

// Frontier independently projects raw legs. Input journeys come either from
// complete enumeration or from the separate original-query replay gate.
func Frontier(ctx context.Context, d model.Dataset, q model.Query, js []model.Journey, budget int) (points []Point, work int, err error) {
	charge := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if work >= budget {
			return &TruncatedError{"projection scans"}
		}
		work++
		return nil
	}
	cs := map[string]model.Connection{}
	for _, c := range d.Connections {
		if e := charge(); e != nil {
			return nil, work, e
		}
		cs[c.ID] = c
	}
	best := map[int]int64{}
	for _, j := range js {
		if e := charge(); e != nil {
			return nil, work, e
		}
		boards := 0
		var last model.Connection
		immediate := false
		for _, l := range j.Legs {
			if e := charge(); e != nil {
				return nil, work, e
			}
			if l.Kind == "ride" {
				c, ok := cs[l.Ref]
				if !ok {
					return nil, work, fmt.Errorf("unknown raw connection")
				}
				if !immediate || c.TripID != last.TripID || c.Sequence != last.Sequence+1 {
					boards++
				}
				last = c
				immediate = true
			} else {
				immediate = false
			}
		}
		// A feasible zero-leg journey is already at the destination and
		// arrives at the original departure, not at service second zero.
		// No journeys still projects to an empty (unreachable) frontier.
		arrival := q.Departure
		if len(j.Legs) == 0 {
			if q.Origin != q.Destination {
				return nil, work, fmt.Errorf("zero-leg journey requires equal endpoints")
			}
		} else {
			arrival = j.Legs[len(j.Legs)-1].Arrival
		}
		if old, ok := best[boards]; !ok || arrival < old {
			best[boards] = arrival
		}
	}
	keys := []int{}
	for k := range best {
		if e := charge(); e != nil {
			return nil, work, e
		}
		keys = append(keys, k)
	}
	sort.Ints(keys)
	out := []Point{}
	var earliest int64
	have := false
	for _, k := range keys {
		if e := charge(); e != nil {
			return nil, work, e
		}
		if !have || best[k] < earliest {
			out = append(out, Point{best[k], k})
			earliest = best[k]
			have = true
		}
	}
	return out, work, nil
}
