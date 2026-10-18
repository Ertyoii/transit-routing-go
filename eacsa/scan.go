// Package eacsa implements the basic earliest-arrival connection scan (CSA,
// Figure 3), on explicit boarding layers for a finite boarding-cap query.
package eacsa

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type Index struct {
	p           *paper.Profile
	connections []model.Connection
}

func New(d model.Dataset) (*Index, error) {
	p, e := paper.New(d)
	if e != nil {
		return nil, e
	}
	cs := append([]model.Connection{}, p.Data.Connections...)
	sort.Slice(cs, func(i, j int) bool {
		if cs[i].Departure == cs[j].Departure {
			return cs[i].ID < cs[j].ID
		}
		return cs[i].Departure < cs[j].Departure
	})
	return &Index{p, cs}, nil
}
func (x *Index) Capabilities() paper.Capability { return x.p.Capability }

// Search returns one representative for every strict arrival improvement as
// boarding cap increases. EA is the minimum arrival; these are NOT top-k paths.
func (x *Index) Search(ctx context.Context, q model.Query) (out paper.Result, err error) {
	out.Journeys = []model.Journey{}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if err = x.p.CheckContext(ctx, q); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if !x.p.Accessible(q, q.Origin) || !x.p.Accessible(q, q.Destination) {
		return
	}
	if q.Origin == q.Destination {
		out.Journeys = append(out.Journeys, paper.Journey(nil))
		return
	}
	walker := x.p.Walker(q)
	defer func() { out.Stats.WalkClosures = walker.Count }()
	ready := make([]map[string]paper.Label, q.MaxBoardings+1)
	onboard := make([]map[string]*paper.Node, q.MaxBoardings+1)
	lastSeq := make([]map[string]int, q.MaxBoardings+1)
	best := make([]paper.Label, q.MaxBoardings+1)
	for b := range ready {
		ready[b] = map[string]paper.Label{}
		onboard[b] = map[string]*paper.Node{}
		lastSeq[b] = map[string]int{}
		best[b].Time = paper.Infinity
	}
	paths, e := walker.Paths(ctx, q.Origin)
	if e != nil {
		err = e
		return
	}
	for _, w := range paths {
		at := q.Departure + w.Duration
		if at > q.LatestArrival {
			continue
		}
		n := w.Append(nil, q.Departure)
		if w.To == q.Destination {
			if w.Duration <= q.Walking.Direct {
				best[0] = paper.Label{Time: at, Parent: n}
			}
			continue
		}
		if w.Duration <= q.Walking.Access {
			ready[0][w.To] = paper.Label{Time: at, Parent: n}
		}
	}
	start := sort.Search(len(x.connections), func(i int) bool { return x.connections[i].Departure >= q.Departure })
	for _, c := range x.connections[start:] {
		if err = ctx.Err(); err != nil {
			return
		}
		if c.Departure > q.LatestArrival {
			break
		}
		if c.Arrival > q.LatestArrival || !x.p.TripAllowed(q, c.TripID) {
			continue
		}
		for b := 1; b <= q.MaxBoardings; b++ {
			out.Stats.Connections++
			parent, aboard := onboard[b][c.TripID]
			aboard = aboard && lastSeq[b][c.TripID] == c.Sequence-1
			if !aboard {
				label, ok := ready[b-1][c.From]
				if !ok || label.Time > c.Departure || !c.Pickup || !x.p.Accessible(q, c.From) {
					continue
				}
				parent = label.Parent
			}
			n := paper.Ride(parent, c)
			onboard[b][c.TripID] = n
			lastSeq[b][c.TripID] = c.Sequence
			if !c.Dropoff || !x.p.Accessible(q, c.To) {
				continue
			}
			if c.To == q.Destination {
				if c.Arrival < best[b].Time {
					best[b] = paper.Label{Time: c.Arrival, Parent: n}
				}
				delete(onboard[b], c.TripID)
				continue
			}
			paths, e := walker.Paths(ctx, c.To)
			if e != nil {
				err = e
				return
			}
			for _, w := range paths {
				at := c.Arrival + w.Duration
				if at > q.LatestArrival {
					continue
				}
				if w.To == q.Destination {
					if w.Duration <= q.Walking.Egress && at < best[b].Time {
						best[b] = paper.Label{Time: at, Parent: w.Append(n, c.Arrival)}
					}
					continue
				}
				if b == q.MaxBoardings || w.Duration > q.Walking.Transfer {
					continue
				}
				at, allowed, e := x.p.ReadyTime(q, c.To, w.To, c.Arrival, w.Duration)
				if e != nil {
					err = e
					return
				}
				if !allowed || at > q.LatestArrival {
					continue
				}
				old, ok := ready[b][w.To]
				if !ok || at < old.Time {
					ready[b][w.To] = paper.Label{Time: at, Parent: w.Append(n, c.Arrival)}
				}
			}
		}
	}
	arrival := paper.Infinity
	for _, b := range best {
		if b.Time < arrival {
			out.Journeys = append(out.Journeys, paper.Journey(b.Parent))
			arrival = b.Time
		}
	}
	out.Stats.Rounds = q.MaxBoardings
	return
}
