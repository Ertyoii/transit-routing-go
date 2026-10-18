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
	access := map[string]paper.Path{}
	for _, w := range paths {
		if w.To == bq.Destination {
			if w.Duration <= bq.Walking.Direct && bq.Departure+w.Duration <= bq.LatestArrival {
				out.Direct = &profilequery.Direct{Duration: w.Duration, Journey: paper.Journey(w.Append(nil, bq.Departure))}
			}
		} else if w.Duration <= bq.Walking.Access {
			access[w.To] = w
		}
	}
	if bq.Origin == bq.Destination || bq.MaxBoardings == 0 {
		return
	}
	// The upper ready boundary also represents all useful first departures after
	// that boundary. Other critical times are vehicle departure minus access walk.
	times := map[int64]bool{q.End: true}
	for _, c := range x.p.Data.Connections {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.DepartureEventChecks++
		w, ok := access[c.From]
		if !ok || !c.Pickup || c.Arrival > bq.LatestArrival || !x.p.TripAllowed(bq, c.TripID) {
			continue
		}
		t := c.Departure - w.Duration
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
	ready := make([]map[string]rangeLabel, bq.MaxBoardings+1)
	best := make([]rangeLabel, bq.MaxBoardings+1)
	for b := range ready {
		ready[b] = map[string]rangeLabel{}
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
	accessStops := []string{}
	for s := range access {
		accessStops = append(accessStops, s)
	}
	sort.Strings(accessStops)
	for _, departure := range departures {
		if err = ctx.Err(); err != nil {
			return
		}
		out.Stats.Departures++
		changed := map[string]bool{}
		for _, s := range accessStops {
			w := access[s]
			at := departure + w.Duration
			if at > bq.LatestArrival {
				continue
			}
			if err = spend(); err != nil {
				return
			}
			ready[0][s] = rangeLabel{at, departure, w.Append(nil, departure)}
			changed[s] = true
		}
		for b := 1; b <= bq.MaxBoardings && len(changed) > 0; b++ {
			out.Stats.Rounds++
			queue := map[int]int{}
			for s := range changed {
				for _, o := range x.at[s] {
					p, ok := queue[o.route]
					if !ok || o.position < p {
						queue[o.route] = o.position
					}
				}
			}
			routes := []int{}
			for r := range queue {
				routes = append(routes, r)
			}
			sort.Ints(routes)
			nextChanged := map[string]bool{}
			for _, r := range routes {
				out.Stats.Routes++
				route := x.routes[r]
				current := -1
				var parent *paper.Node
				var start int64
				for i := queue[r]; i < len(route.trips[0].cs); i++ {
					if err = ctx.Err(); err != nil {
						return
					}
					prototype := route.trips[0].cs[i]
					if label, ok := ready[b-1][prototype.From]; ok && prototype.Pickup && x.p.Accessible(bq, prototype.From) {
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
					ws, e := walker.Paths(ctx, c.To)
					if e != nil {
						err = e
						return
					}
					for _, w := range ws {
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
						at, allowed, e := x.p.ReadyTime(bq, c.To, w.To, c.Arrival, w.Duration)
						if e != nil {
							err = e
							return
						}
						if !allowed || at > bq.LatestArrival {
							continue
						}
						old, ok := ready[b][w.To]
						if !ok || at < old.time || at == old.time && start > old.start {
							if err = spend(); err != nil {
								return
							}
							ready[b][w.To] = rangeLabel{at, start, w.Append(parent, c.Arrival)}
							nextChanged[w.To] = true
						}
					}
				}
			}
			changed = nextChanged
		}
	}
	return
}
