package ultra

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

// Search implements exact-round ULTRA-RAPTOR using one-hop completed transfers.
// Apply pair-specific readiness before merging labels; never chain shortcuts.
func (x *CappedIndex) Search(ctx context.Context, q model.Query, limits Limits) (out CappedResult, err error) {
	out.Journeys = []model.Journey{}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if x == nil || !x.Complete() {
		return out, fmt.Errorf("ULTRA capped: incomplete index")
	}
	if !validLimits(limits) {
		return out, fmt.Errorf("ULTRA capped: explicit positive query limits required")
	}
	if err = x.x.p.CheckContext(ctx, q); err != nil {
		return
	}
	if q.AccessibleOnly != x.prep.Config.AccessibleOnly {
		return out, fmt.Errorf("ULTRA capped capability: accessibility differs from prepared graph")
	}
	if q.Walking.Transfer > x.prep.Config.TransferWalkingLimit {
		return out, fmt.Errorf("ULTRA capped capability: transfer cap exceeds prepared ceiling")
	}
	if err = ctx.Err(); err != nil {
		return
	}
	p := x.x.p
	if !p.Accessible(q, q.Origin) || !p.Accessible(q, q.Destination) {
		return
	}
	if q.Origin == q.Destination {
		out.Journeys = append(out.Journeys, paper.Journey(nil))
		return
	}
	w := work{ctx, limits, &out.Stats.Stats}
	forward, e := x.walk(q.Origin, min(max(q.Walking.Access, q.Walking.Direct), q.LatestArrival-q.Departure), false, q.Destination, &w)
	out.Stats.WalkSearches++
	if e != nil {
		return out, e
	}
	var backward rawSearch
	if q.MaxBoardings > 0 {
		backward, err = x.walk(q.Destination, min(q.Walking.Egress, q.LatestArrival-q.Departure), true, "", &w)
		out.Stats.WalkSearches++
		if err != nil {
			return
		}
	}
	emit := func(parent *paper.Node) error {
		for n := parent; n != nil; n = n.Previous {
			if e := w.leg(); e != nil {
				return e
			}
		}
		out.Journeys = append(out.Journeys, paper.Journey(parent))
		return nil
	}
	previous := map[string]paper.Label{}
	endpoints := []string{}
	for stop := range forward.distance {
		endpoints = append(endpoints, stop)
	}
	sort.Strings(endpoints)
	bestArrival := paper.Infinity
	for _, stop := range endpoints {
		at := q.Departure + forward.distance[stop]
		if at > q.LatestArrival {
			continue
		}
		if stop == q.Destination {
			if forward.distance[stop] <= q.Walking.Direct {
				edges, e := x.path(forward, stop, &w)
				if e != nil {
					return out, e
				}
				bestArrival = at
				if err = emit((paper.Path{Edges: edges}).Append(nil, q.Departure)); err != nil {
					return
				}
			}
		} else if q.MaxBoardings > 0 && x.x.transit[stop] && forward.distance[stop] <= q.Walking.Access {
			edges, e := x.path(forward, stop, &w)
			if e != nil {
				return out, e
			}
			previous[stop] = paper.Label{Time: at, Parent: (paper.Path{Edges: edges}).Append(nil, q.Departure)}
		}
	}
	rows := map[string][]paper.Path{}
	getRow := func(from string) ([]paper.Path, error) {
		if r, ok := rows[from]; ok {
			return r, nil
		}
		out.Stats.RowUses++
		r := []paper.Path{}
		var detour *rawSearch
		for _, path := range x.rows[from] {
			if e := w.check(); e != nil {
				return nil, e
			}
			if path.To == q.Destination || path.Duration > q.Walking.Transfer {
				continue
			}
			crosses := false
			for _, edge := range path.Edges {
				if e := w.edge(); e != nil {
					return nil, e
				}
				if edge.To == q.Destination {
					crosses = true
					break
				}
			}
			if crosses {
				if detour == nil {
					d, e := x.walk(from, q.Walking.Transfer, false, q.Destination, &w)
					out.Stats.WalkSearches++
					out.Stats.DestinationRepairs++
					if e != nil {
						return nil, e
					}
					detour = &d
				}
				duration, ok := detour.distance[path.To]
				if !ok {
					continue
				}
				edges, e := x.path(*detour, path.To, &w)
				if e != nil {
					return nil, e
				}
				path = paper.Path{To: path.To, Duration: duration, Edges: edges}
			}
			r = append(r, path)
		}
		rows[from] = r
		return r, nil
	}
	egress := map[string][]model.Footpath{}
	for boards := 1; boards <= q.MaxBoardings && len(previous) > 0; boards++ {
		if err = w.check(); err != nil {
			return
		}
		out.Stats.Rounds++
		selected := map[int]int{}
		for stop := range previous {
			for _, o := range x.at[stop] {
				if err = w.connection(); err != nil {
					return
				}
				out.Stats.RouteOccurrenceChecks++
				if old, ok := selected[o.route]; !ok || o.position < old {
					selected[o.route] = o.position
				}
			}
		}
		routeIDs := []int{}
		for id := range selected {
			routeIDs = append(routeIDs, id)
		}
		sort.Ints(routeIDs)
		next := map[string]paper.Label{}
		best := paper.Label{Time: bestArrival}
		for _, ri := range routeIDs {
			if err = w.check(); err != nil {
				return
			}
			out.Stats.RouteScans++
			r := x.x.routes[ri]
			if !p.TripAllowed(q, r.trips[0].id) {
				continue
			}
			current := -1
			var parent *paper.Node
			for pos := selected[ri]; pos < len(r.trips[0].cs); pos++ {
				if err = w.connection(); err != nil {
					return
				}
				proto := r.trips[0].cs[pos]
				if l, ok := previous[proto.From]; ok && proto.Pickup && p.Accessible(q, proto.From) {
					lo, hi := 0, len(r.trips)
					if current >= 0 {
						hi = current
					}
					for lo < hi {
						if err = w.connection(); err != nil {
							return
						}
						out.Stats.TripProbes++
						mid := lo + (hi-lo)/2
						if r.trips[mid].cs[pos].Departure < l.Time {
							lo = mid + 1
						} else {
							hi = mid
						}
					}
					if lo < len(r.trips) && (current < 0 || lo < current) {
						current = lo
						parent = l.Parent
					}
				}
				if current < 0 {
					continue
				}
				c := r.trips[current].cs[pos]
				if c.Arrival > q.LatestArrival {
					current = -1
					continue
				}
				parent = paper.Ride(parent, c)
				if !c.Dropoff || !p.Accessible(q, c.To) {
					continue
				}
				if c.To == q.Destination {
					if c.Arrival < best.Time {
						best = paper.Label{Time: c.Arrival, Parent: parent}
					}
					current = -1
					continue
				}
				if duration, ok := backward.distance[c.To]; ok && c.Arrival+duration < best.Time && c.Arrival+duration <= q.LatestArrival {
					edges, exists := egress[c.To]
					if !exists {
						edges, err = x.path(backward, c.To, &w)
						if err != nil {
							return
						}
						egress[c.To] = edges
					}
					best = paper.Label{Time: c.Arrival + duration, Parent: (paper.Path{Edges: edges}).Append(parent, c.Arrival)}
				}
				if boards == q.MaxBoardings || c.Arrival >= best.Time {
					continue
				}
				paths, e := getRow(c.To)
				if e != nil {
					return out, e
				}
				for _, path := range paths {
					if err = w.edge(); err != nil {
						return
					}
					out.Stats.TransferPairVisits++
					ready, allowed, e := p.ReadyTime(q, c.To, path.To, c.Arrival, path.Duration)
					if e != nil {
						return out, e
					}
					if !allowed || ready > q.LatestArrival || ready >= best.Time {
						continue
					}
					old, ok := next[path.To]
					if !ok || ready < old.Time {
						next[path.To] = paper.Label{Time: ready, Parent: path.Append(parent, c.Arrival)}
					}
				}
			}
		}
		if best.Time < bestArrival {
			if err = emit(best.Parent); err != nil {
				return
			}
			bestArrival = best.Time
		}
		previous = next
	}
	return
}
