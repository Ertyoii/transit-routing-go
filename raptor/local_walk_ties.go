package raptor

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

// SearchLocalWalkTies retains the native 2D arrival/boardings frontier and one
// readiness label per stop. It refines equal-time stop labels, equal-trip
// onboard alternatives and equal-arrival answers toward less total walking.
// Later readiness/lower-walking states are still discarded: this is NOT an
// exhaustive minimum-walking representative algorithm or a 3D McRAPTOR fallback.
// It is separately named because witness ties and work counters may change.
func (x *Index) SearchLocalWalkTies(ctx context.Context, q model.Query, early bool) (paper.Result, EarlyPruningStats, error) {
	return x.searchLocalWalkTies(ctx, q, early)
}

func (x *Index) searchLocalWalkTies(ctx context.Context, q model.Query, early bool) (out paper.Result, pruning EarlyPruningStats, err error) {
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
	walks := x.indexedWalks(walker, q, early)
	defer func() { out.Stats.WalkClosures = walker.Count }()
	previous := make([]paper.Label, len(x.atStop))
	previousSet := make([]bool, len(x.atStop))
	previousWalk := make([]int64, len(x.atStop))
	previousStops := []int{}
	bestArrival := paper.Infinity
	paths, states, e := walks.paths(ctx, q.Origin, x.stopIDs[q.Origin])
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
			stop := states[i].stop
			if !previousSet[stop] {
				previousStops = append(previousStops, stop)
			}
			previousSet[stop] = true
			previous[stop] = paper.Label{Time: at, Parent: n}
			previousWalk[stop] = w.Duration
		}
	}
	next := make([]paper.Label, len(x.atStop))
	nextSet := make([]bool, len(x.atStop))
	nextWalk := make([]int64, len(x.atStop))
	nextStops := []int{}
	queue := make([]int, len(x.routes))
	routes := []int{}
	// Exact-round readiness is kept separate from raw alighting and access
	// labels: role caps and stop-pair rules make ordinary stop-local pruning unsafe.
	for b := 1; b <= q.MaxBoardings && len(previousStops) > 0; b++ {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.Rounds++
		queue, routes = x.collectRoutesInto(previousStops, queue, routes)
		for _, stop := range nextStops {
			nextSet[stop] = false
			next[stop] = paper.Label{}
		}
		nextStops = nextStops[:0]
		best := paper.Label{Time: paper.Infinity}
		bestWalk := paper.Infinity
		for _, r := range routes {
			if err = ctx.Err(); err != nil {
				return
			}
			out.Stats.Routes++
			route := x.routes[r]
			current := -1
			currentWalk := int64(0)
			var parent *paper.Node
			for i := queue[r] - 1; i < len(route.trips[0].cs); i++ {
				if err = ctx.Err(); err != nil {
					return
				}
				prototype := route.trips[0].cs[i]
				if label := previous[route.from[i]]; previousSet[route.from[i]] && prototype.Pickup && x.p.Accessible(q, prototype.From) {
					hi := len(route.trips)
					if current >= 0 {
						hi = current + 1
					}
					t := sort.Search(hi, func(j int) bool { return route.trips[j].cs[i].Departure >= label.Time })
					if t < len(route.trips) && (current < 0 || t < current || t == current && previousWalk[route.from[i]] < currentWalk) && x.p.TripAllowed(q, route.trips[t].id) {
						current = t
						parent = label.Parent
						currentWalk = previousWalk[route.from[i]]
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
					if c.Arrival < best.Time || c.Arrival == best.Time && currentWalk < bestWalk {
						bestWalk = currentWalk
						best = paper.Label{Time: c.Arrival, Parent: parent}
					}
					current = -1
					continue
				}
				paths, states, e := walks.paths(ctx, c.To, route.to[i])
				if e != nil {
					err = e
					return
				}
				for i, w := range paths {
					at := c.Arrival + w.Duration
					if early {
						pruning.Checks++
						if at >= bestArrival || at > best.Time {
							pruning.skip(len(paths) - i)
							break
						}
					}
					if at > q.LatestArrival {
						continue
					}
					if w.To == q.Destination {
						if w.Duration <= q.Walking.Egress && (at < best.Time || at == best.Time && currentWalk+w.Duration < bestWalk) {
							bestWalk = currentWalk + w.Duration
							best = paper.Label{Time: at, Parent: w.Append(parent, c.Arrival)}
						}
						continue
					}
					if b == q.MaxBoardings || w.Duration > q.Walking.Transfer {
						continue
					}
					at, allowed, e := walks.readyTime(&states[i], c.To, w, c.Arrival)
					if e != nil {
						err = e
						return
					}
					if !allowed || at > q.LatestArrival {
						continue
					}
					stop := states[i].stop
					if !nextSet[stop] || at < next[stop].Time || at == next[stop].Time && currentWalk+w.Duration < nextWalk[stop] {
						if !nextSet[stop] {
							nextStops = append(nextStops, stop)
						}
						nextSet[stop] = true
						nextWalk[stop] = currentWalk + w.Duration
						next[stop] = paper.Label{Time: at, Parent: w.Append(parent, c.Arrival)}
					}
				}
			}
		}
		if best.Time < bestArrival {
			bestArrival = best.Time
			out.Journeys = append(out.Journeys, paper.Journey(best.Parent))
		}
		previous, next = next, previous
		previousSet, nextSet = nextSet, previousSet
		previousWalk, nextWalk = nextWalk, previousWalk
		previousStops, nextStops = nextStops, previousStops
	}
	return
}
