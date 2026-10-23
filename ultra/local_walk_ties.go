package ultra

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

// SearchLocalWalkTies is a separately named 2D representative refinement. It
// retains one ready label per stop and replaces only equal-time stop labels,
// equal-trip onboard labels and equal-arrival answers toward less walking.
// Later readiness/lower-walking alternatives remain discarded: no exhaustive
// minimum-walking representative or 3D frontier is claimed.
func (x *CappedIndex) SearchLocalWalkTies(ctx context.Context, q model.Query, limits Limits) (out CappedResult, err error) {
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
	n := len(x.stopIDs)
	destination := x.stopIDs[q.Destination]
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
	previous := make([]paper.Label, n)
	previousSet := make([]bool, n)
	previousWalk := make([]int64, n)
	previousStops := []int{}
	next := make([]paper.Label, n)
	nextSet := make([]bool, n)
	nextWalk := make([]int64, n)
	nextStops := []int{}
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
		} else if q.MaxBoardings > 0 && x.transit[x.stopIDs[stop]] && forward.distance[stop] <= q.Walking.Access {
			edges, e := x.path(forward, stop, &w)
			if e != nil {
				return out, e
			}
			id := x.stopIDs[stop]
			previousStops = append(previousStops, id)
			previousSet[id] = true
			previousWalk[id] = forward.distance[stop]
			previous[id] = paper.Label{Time: at, Parent: (paper.Path{Edges: edges}).Append(nil, q.Departure)}
		}
	}
	rows := make([][]cappedPath, n)
	rowSet := make([]bool, n)
	getRow := func(from int, fromName string) ([]cappedPath, error) {
		if rowSet[from] {
			return rows[from], nil
		}
		out.Stats.RowUses++
		r := []cappedPath{}
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
					d, e := x.walk(fromName, q.Walking.Transfer, false, q.Destination, &w)
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
				path.Path = paper.Path{To: path.To, Duration: duration, Edges: edges}
			}
			r = append(r, path)
		}
		rows[from] = r
		rowSet[from] = true
		return r, nil
	}
	egress := make([][]model.Footpath, n)
	egressSet := make([]bool, n)
	backwardDistance := make([]int64, n)
	backwardSet := make([]bool, n)
	for name, duration := range backward.distance {
		id := x.stopIDs[name]
		backwardDistance[id], backwardSet[id] = duration, true
	}
	selected := make([]int, len(x.x.routes))
	routeIDs := []int{}
	for boards := 1; boards <= q.MaxBoardings && len(previousStops) > 0; boards++ {
		if err = w.check(); err != nil {
			return
		}
		out.Stats.Rounds++
		for _, ri := range routeIDs {
			selected[ri] = 0
		}
		routeIDs = routeIDs[:0]
		for _, stop := range previousStops {
			for _, o := range x.at[stop] {
				if err = w.connection(); err != nil {
					return
				}
				out.Stats.RouteOccurrenceChecks++
				if old := selected[o.route]; old == 0 || o.position+1 < old {
					if old == 0 {
						routeIDs = append(routeIDs, o.route)
					}
					selected[o.route] = o.position + 1
				}
			}
		}
		sort.Ints(routeIDs)
		for _, stop := range nextStops {
			nextSet[stop] = false
		}
		nextStops = nextStops[:0]
		best := paper.Label{Time: bestArrival}
		bestWalk := paper.Infinity
		for _, ri := range routeIDs {
			if err = w.check(); err != nil {
				return
			}
			out.Stats.RouteScans++
			r := x.x.routes[ri]
			if q.AccessibleOnly && !x.tripAllowed[ri] {
				continue
			}
			current := -1
			currentWalk := int64(0)
			var parent *paper.Node
			for pos := selected[ri] - 1; pos < len(r.trips[0].cs); pos++ {
				if err = w.connection(); err != nil {
					return
				}
				proto := r.trips[0].cs[pos]
				fromID := x.from[ri][pos]
				if l := previous[fromID]; previousSet[fromID] && proto.Pickup && (!q.AccessibleOnly || x.accessible[fromID]) {
					lo, hi := 0, len(r.trips)
					if current >= 0 {
						hi = current + 1
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
					if lo < len(r.trips) && (current < 0 || lo < current || lo == current && previousWalk[fromID] < currentWalk) {
						current = lo
						parent = l.Parent
						currentWalk = previousWalk[fromID]
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
				toID := x.to[ri][pos]
				if !c.Dropoff || q.AccessibleOnly && !x.accessible[toID] {
					continue
				}
				if toID == destination {
					if c.Arrival < best.Time || c.Arrival == best.Time && currentWalk < bestWalk {
						bestWalk = currentWalk
						best = paper.Label{Time: c.Arrival, Parent: parent}
					}
					current = -1
					continue
				}
				if duration := backwardDistance[toID]; backwardSet[toID] && (c.Arrival+duration < best.Time || c.Arrival+duration == best.Time && currentWalk+duration < bestWalk) && c.Arrival+duration <= q.LatestArrival {
					edges := egress[toID]
					if !egressSet[toID] {
						edges, err = x.path(backward, c.To, &w)
						if err != nil {
							return
						}
						egress[toID] = edges
						egressSet[toID] = true
					}
					bestWalk = currentWalk + duration
					best = paper.Label{Time: c.Arrival + duration, Parent: (paper.Path{Edges: edges}).Append(parent, c.Arrival)}
				}
				if boards == q.MaxBoardings || c.Arrival >= best.Time {
					continue
				}
				paths, e := getRow(toID, c.To)
				if e != nil {
					return out, e
				}
				for _, path := range paths {
					if err = w.edge(); err != nil {
						return
					}
					out.Stats.TransferPairVisits++
					if path.ruleError != nil {
						return out, path.ruleError
					}
					minimum := path.minimum
					if minimum < 0 {
						minimum = q.TransferBuffer
					}
					ready, allowed := c.Arrival+max(path.Duration, minimum), path.allowed
					if !allowed || ready > q.LatestArrival || ready >= best.Time {
						continue
					}
					old := next[path.to]
					if !nextSet[path.to] || ready < old.Time || ready == old.Time && currentWalk+path.Duration < nextWalk[path.to] {
						if !nextSet[path.to] {
							nextStops = append(nextStops, path.to)
							nextSet[path.to] = true
						}
						nextWalk[path.to] = currentWalk + path.Duration
						next[path.to] = paper.Label{Time: ready, Parent: path.Path.Append(parent, c.Arrival)}
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
		previous, next = next, previous
		previousSet, nextSet = nextSet, previousSet
		previousWalk, nextWalk = nextWalk, previousWalk
		previousStops, nextStops = nextStops, previousStops
	}
	return
}

// SearchLocalWalkTies applies the separately named local tie refinement to the
// cap-safe subset. The original preparation completeness and fixed-buffer
// certificate requirements remain mandatory.
func (x *SparseIndex) SearchLocalWalkTies(ctx context.Context, q model.Query, l Limits) (CappedResult, error) {
	fail := func(e error) (CappedResult, error) {
		return CappedResult{Journeys: []model.Journey{}, Error: e.Error()}, e
	}
	if !x.Complete() {
		return fail(fmt.Errorf("ULTRA sparse: incomplete index"))
	}
	if q.TransferBuffer != x.prep.Config.TransferBuffer {
		return fail(fmt.Errorf("ULTRA sparse capability: transfer buffer differs from proof"))
	}
	return x.index.SearchLocalWalkTies(ctx, q, l)
}
