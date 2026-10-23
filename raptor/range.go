package raptor

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/paper"
	"github.com/ertyoii/transit-routing-go/profilequery"
	"sort"
)

type rangeLabel struct {
	time, start int64
	parent      *paper.Node
}

// SearchRange implements rRAPTOR's descending departure iterations with
// persistent per-round labels (ALENEX 2012, Section 4.2). It does not call Search.
// Exact-round transfer-ready state is separated from access and terminal state,
// preserving the common static stop-pair/role-specific walking extension.
func (x *Index) SearchRange(ctx context.Context, q profilequery.Query, limits profilequery.Limits) (out profilequery.Result, err error) {
	out.Query = q
	out.Points = []profilequery.Point{}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		} else {
			out.Finalize()
		}
	}()
	if err = q.CheckContext(ctx, x.p, limits); err != nil {
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	bq := q.Base
	if !x.p.Accessible(bq, bq.Origin) || !x.p.Accessible(bq, bq.Destination) {
		return
	}
	walker := x.p.Walker(bq)
	defer func() { out.Stats.WalkClosures = walker.Count }()
	paths, e := walker.Paths(ctx, bq.Origin)
	if e != nil {
		err = e
		return
	}
	type accessStop struct{ stop, path int }
	accessStops := []accessStop{}
	for i, w := range paths {
		if w.To == bq.Destination {
			if w.Duration <= bq.Walking.Direct && bq.Departure+w.Duration <= bq.LatestArrival {
				out.Direct = &profilequery.Direct{Duration: w.Duration, Journey: paper.Journey(w.Append(nil, bq.Departure))}
			}
		} else if w.Duration <= bq.Walking.Access {
			accessStops = append(accessStops, accessStop{x.stopIDs[w.To], i})
		}
	}
	if bq.Origin == bq.Destination || bq.MaxBoardings == 0 {
		return
	}
	// Zero means absent; other entries reference the immutable origin closure.
	accessPath := make([]int, len(x.atStop))
	for _, a := range accessStops {
		accessPath[a.stop] = a.path + 1
	}
	// The upper ready boundary also represents all useful first departures after
	// that boundary. Other critical times are vehicle departure minus access walk.
	times := map[int64]bool{q.End: true}
	for _, c := range x.p.Data.Connections {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.DepartureEventChecks++
		stop := x.stopIDs[c.From]
		path := accessPath[stop]
		if path == 0 || !c.Pickup || c.Arrival > bq.LatestArrival || !x.p.TripAllowed(bq, c.TripID) {
			continue
		}
		t := c.Departure - paths[path-1].Duration
		if t >= bq.Departure && t <= q.End {
			times[t] = true
		}
	}
	departures := make([]int64, 0, len(times))
	for t := range times {
		departures = append(departures, t)
	}
	sort.Slice(departures, func(i, j int) bool { return departures[i] > departures[j] })
	if len(departures) > limits.MaxDepartures {
		err = fmt.Errorf("range RAPTOR incomplete: departure iteration budget exhausted")
		return
	}
	// Exact-round readiness persists across descending departure iterations. Rows
	// are allocated lazily, so an unused boarding round has no stop-sized cost.
	ready := make([][]rangeLabel, bq.MaxBoardings+1)
	readySet := make([][]bool, bq.MaxBoardings+1)
	best := make([]rangeLabel, bq.MaxBoardings+1)
	for b := range ready {
		best[b].time = paper.Infinity
	}
	spend := func() error {
		if out.Stats.Labels == limits.MaxLabels {
			return fmt.Errorf("range RAPTOR incomplete: label budget exhausted")
		}
		out.Stats.Labels++
		return nil
	}
	offer := func(b int, arrival, start int64, parent *paper.Node) error {
		if arrival > bq.LatestArrival || arrival > best[b].time || arrival == best[b].time && start <= best[b].start {
			return nil
		}
		if e := spend(); e != nil {
			return e
		}
		best[b] = rangeLabel{arrival, start, parent}
		out.Points = append(out.Points, profilequery.Point{LatestReady: start, Arrival: arrival, Boardings: b, Journey: paper.Journey(parent)})
		return nil
	}
	// Native access insertion is stop-name sorted; retain that witness/budget order.
	sort.Slice(accessStops, func(i, j int) bool {
		return paths[accessStops[i].path].To < paths[accessStops[j].path].To
	})
	walks := x.indexedWalks(walker, bq, false)
	ready[0] = make([]rangeLabel, len(x.atStop))
	readySet[0] = make([]bool, len(x.atStop))
	changed, nextChanged := []int{}, []int{}
	nextSet := make([]bool, len(x.atStop))
	queue := make([]int, len(x.routes))
	routes := []int{}
	for _, departure := range departures {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.Departures++
		changed = changed[:0]
		for _, a := range accessStops {
			s := a.stop
			w := paths[a.path]
			at := departure + w.Duration
			if at > bq.LatestArrival {
				continue
			}
			if err = spend(); err != nil {
				return
			}
			ready[0][s] = rangeLabel{at, departure, w.Append(nil, departure)}
			readySet[0][s] = true
			changed = append(changed, s)
		}
		for b := 1; b <= bq.MaxBoardings && len(changed) > 0; b++ {
			out.Stats.Rounds++
			// Only touched routes reset. Sorting retains the native route scan order.
			queue, routes = x.collectRoutesInto(changed, queue, routes)
			nextChanged = nextChanged[:0]
			for _, r := range routes {
				out.Stats.Routes++
				route := x.routes[r]
				current := -1
				var parent *paper.Node
				var start int64
				for i := queue[r] - 1; i < len(route.trips[0].cs); i++ {
					if err = ctx.Err(); err != nil {
						return
					}
					prototype := route.trips[0].cs[i]
					stop := route.from[i]
					if label := ready[b-1][stop]; readySet[b-1][stop] && prototype.Pickup && x.p.Accessible(bq, prototype.From) {
						t := sort.Search(len(route.trips), func(j int) bool { return route.trips[j].cs[i].Departure >= label.time })
						if t < len(route.trips) && (current < 0 || t < current || t == current && label.start > start) && x.p.TripAllowed(bq, route.trips[t].id) {
							// This exact continuation has already been scanned in b-1;
							// do not charge a fictitious extra boarding for staying seated.
							continuing := i > 0 && label.parent != nil && label.parent.Leg.Kind == "ride" && label.parent.Leg.Ref == route.trips[t].cs[i-1].ID
							if !continuing {
								current = t
								parent = label.parent
								start = label.start
							}
						}
					}
					if current < 0 {
						continue
					}
					if out.Stats.Connections == limits.MaxConnectionVisits {
						err = fmt.Errorf("range RAPTOR incomplete: connection budget exhausted")
						return
					}
					out.Stats.Connections++
					c := route.trips[current].cs[i]
					if c.Arrival > bq.LatestArrival {
						current = -1
						continue
					}
					parent = paper.Ride(parent, c)
					if !c.Dropoff || !x.p.Accessible(bq, c.To) {
						continue
					}
					if c.To == bq.Destination {
						if err = offer(b, c.Arrival, start, parent); err != nil {
							return
						}
						current = -1
						continue
					}
					ws, states, e := walks.paths(ctx, c.To, route.to[i])
					if e != nil {
						err = e
						return
					}
					for wi, w := range ws {
						if w.To == bq.Destination {
							if w.Duration <= bq.Walking.Egress {
								if err = offer(b, c.Arrival+w.Duration, start, w.Append(parent, c.Arrival)); err != nil {
									return
								}
							}
							continue
						}
						if b == bq.MaxBoardings || w.Duration > bq.Walking.Transfer {
							continue
						}
						at, allowed, e := walks.readyTime(&states[wi], c.To, w, c.Arrival)
						if e != nil {
							err = e
							return
						}
						if !allowed || at > bq.LatestArrival {
							continue
						}
						stop := states[wi].stop
						if ready[b] == nil {
							ready[b] = make([]rangeLabel, len(x.atStop))
							readySet[b] = make([]bool, len(x.atStop))
						}
						old := ready[b][stop]
						if !readySet[b][stop] || at < old.time || at == old.time && start > old.start {
							if err = spend(); err != nil {
								return
							}
							ready[b][stop] = rangeLabel{at, start, w.Append(parent, c.Arrival)}
							readySet[b][stop] = true
							if !nextSet[stop] {
								nextSet[stop] = true
								nextChanged = append(nextChanged, stop)
							}
						}
					}
				}
			}
			for _, stop := range nextChanged {
				nextSet[stop] = false
			}
			changed, nextChanged = nextChanged, changed
		}
	}
	return
}
