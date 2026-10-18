// Package raptor implements round-based route scanning with one current trip per
// non-overtaking route. Static role-specific walking is an explicit extension.
package raptor

import (
	"context"
	"encoding/json"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type trip struct {
	id string
	cs []model.Connection
}
type route struct{ trips []trip }
type occurrence struct{ route, position int }
type Index struct {
	p        *paper.Profile
	routes   []route
	at       map[string][]occurrence
	patterns int
}
type Preparation struct {
	Patterns          int `json:"patterns"`
	AlgorithmicRoutes int `json:"algorithmic_routes"`
	OvertakingSplits  int `json:"overtaking_splits"`
}

func (x *Index) Capabilities() paper.Capability { return x.p.Capability }
func (x *Index) Preparation() Preparation {
	return Preparation{x.patterns, len(x.routes), len(x.routes) - x.patterns}
}
func New(d model.Dataset) (*Index, error) {
	p, e := paper.New(d)
	if e != nil {
		return nil, e
	}
	x := &Index{p: p, at: map[string][]occurrence{}}
	chains := map[string][]model.Connection{}
	for _, c := range p.Data.Connections {
		chains[c.TripID] = append(chains[c.TripID], c)
	}
	patterns := map[string][]trip{}
	for id, cs := range chains {
		sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
		type segment struct {
			From, To        string
			Pickup, Dropoff bool
		}
		sig := struct {
			Segments   []segment
			Accessible model.Access
		}{Accessible: p.Trips[id].Accessible}
		for _, c := range cs {
			sig.Segments = append(sig.Segments, segment{c.From, c.To, c.Pickup, c.Dropoff})
		}
		key, _ := json.Marshal(sig)
		patterns[string(key)] = append(patterns[string(key)], trip{id, cs})
	}
	keys := make([]string, 0, len(patterns))
	for k := range patterns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	x.patterns = len(keys)
	for _, key := range keys {
		ts := patterns[key]
		sort.Slice(ts, func(i, j int) bool {
			if ts[i].cs[0].Departure == ts[j].cs[0].Departure {
				return ts[i].id < ts[j].id
			}
			return ts[i].cs[0].Departure < ts[j].cs[0].Departure
		})
		groups := []route{}
		for _, t := range ts {
			placed := false
			for g := range groups {
				previous := groups[g].trips[len(groups[g].trips)-1]
				ordered := true
				for i, c := range t.cs {
					if c.Departure < previous.cs[i].Departure || c.Arrival < previous.cs[i].Arrival {
						ordered = false
						break
					}
				}
				if ordered {
					groups[g].trips = append(groups[g].trips, t)
					placed = true
					break
				}
			}
			if !placed {
				groups = append(groups, route{trips: []trip{t}})
			}
		}
		x.routes = append(x.routes, groups...)
	}
	for r, route := range x.routes {
		for i, c := range route.trips[0].cs {
			x.at[c.From] = append(x.at[c.From], occurrence{r, i})
		}
	}
	return x, nil
}
func (x *Index) Search(ctx context.Context, q model.Query) (paper.Result, error) {
	out, _, err := x.search(ctx, q, false)
	return out, err
}

// SearchEarlyPruning preserves arrival/boarding Pareto vectors, with one
// representative per vector. It does not preserve every equal-vector identity.
func (x *Index) SearchEarlyPruning(ctx context.Context, q model.Query) (paper.Result, EarlyPruningStats, error) {
	return x.search(ctx, q, true)
}

func (x *Index) search(ctx context.Context, q model.Query, early bool) (out paper.Result, pruning EarlyPruningStats, err error) {
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
	walks := orderedWalks{walker: walker, enabled: early, cache: map[string][]paper.Path{}}
	defer func() { out.Stats.WalkClosures = walker.Count }()
	previous := map[string]paper.Label{}
	bestArrival := paper.Infinity
	paths, e := walks.paths(ctx, q.Origin)
	if e != nil {
		err = e
		return
	}
	for i, w := range paths {
		at := q.Departure + w.Duration
		if early {
			pruning.Checks++
			if at >= bestArrival {
				pruning.skip(len(paths) - i)
				break
			}
		}
		if at > q.LatestArrival {
			continue
		}
		n := w.Append(nil, q.Departure)
		if w.To == q.Destination {
			if w.Duration <= q.Walking.Direct {
				bestArrival = at
				out.Journeys = append(out.Journeys, paper.Journey(n))
			}
			continue
		}
		if w.Duration <= q.Walking.Access {
			previous[w.To] = paper.Label{Time: at, Parent: n}
		}
	}
	// Exact-round readiness is kept separate from raw alighting and access
	// labels: role caps and stop-pair rules make ordinary stop-local pruning unsafe.
	for b := 1; b <= q.MaxBoardings && len(previous) > 0; b++ {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.Rounds++
		queue := map[int]int{}
		for stop := range previous {
			for _, o := range x.at[stop] {
				p, ok := queue[o.route]
				if !ok || o.position < p {
					queue[o.route] = o.position
				}
			}
		}
		routes := make([]int, 0, len(queue))
		for r := range queue {
			routes = append(routes, r)
		}
		sort.Ints(routes)
		next := map[string]paper.Label{}
		best := paper.Label{Time: paper.Infinity}
		for _, r := range routes {
			if err = ctx.Err(); err != nil {
				return
			}
			out.Stats.Routes++
			route := x.routes[r]
			current := -1
			var parent *paper.Node
			for i := queue[r]; i < len(route.trips[0].cs); i++ {
				if err = ctx.Err(); err != nil {
					return
				}
				prototype := route.trips[0].cs[i]
				if label, ok := previous[prototype.From]; ok && prototype.Pickup && x.p.Accessible(q, prototype.From) {
					t := sort.Search(len(route.trips), func(j int) bool { return route.trips[j].cs[i].Departure >= label.Time })
					if t < len(route.trips) && (current < 0 || t < current) && x.p.TripAllowed(q, route.trips[t].id) {
						current = t
						parent = label.Parent
					}
				}
				if current < 0 {
					continue
				}
				c := route.trips[current].cs[i]
				out.Stats.Connections++
				if c.Arrival > q.LatestArrival {
					current = -1
					continue
				}
				parent = paper.Ride(parent, c)
				if !c.Dropoff || !x.p.Accessible(q, c.To) {
					continue
				}
				if c.To == q.Destination {
					if c.Arrival < best.Time {
						best = paper.Label{Time: c.Arrival, Parent: parent}
					}
					current = -1
					continue
				}
				paths, e := walks.paths(ctx, c.To)
				if e != nil {
					err = e
					return
				}
				for i, w := range paths {
					at := c.Arrival + w.Duration
					if early {
						pruning.Checks++
						if at >= min(bestArrival, best.Time) {
							pruning.skip(len(paths) - i)
							break
						}
					}
					if at > q.LatestArrival {
						continue
					}
					if w.To == q.Destination {
						if w.Duration <= q.Walking.Egress && at < best.Time {
							best = paper.Label{Time: at, Parent: w.Append(parent, c.Arrival)}
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
					old, ok := next[w.To]
					if !ok || at < old.Time {
						next[w.To] = paper.Label{Time: at, Parent: w.Append(parent, c.Arrival)}
					}
				}
			}
		}
		if best.Time < bestArrival {
			bestArrival = best.Time
			out.Journeys = append(out.Journeys, paper.Journey(best.Parent))
		}
		previous = next
	}
	return
}
