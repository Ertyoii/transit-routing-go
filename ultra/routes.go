package ultra

import (
	"encoding/json"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

const IndependentTrips = "independent_trips"
const NonOvertakingGroups = "non_overtaking_groups"

type route struct{ trips []trip }
type RouteSummary struct {
	TripIDs []string `json:"trip_ids"`
	Stops   []string `json:"stops"`
}

// Identical stop sequences are partitioned into non-overtaking chains. Every
// departure and arrival position is strictly ordered; equal-time trips split.
// This restores the paper's same-route/arrival uniqueness premise.
// Sorted pattern keys and stable trip ties define the canonical route order.
func (x *Index) buildRoutes(byTrip map[string][]model.Connection, mode string, w *work) error {
	ids := []string{}
	for id := range byTrip {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	patterns := map[string][]trip{}
	for _, id := range ids {
		if e := w.check(); e != nil {
			return e
		}
		cs := byTrip[id]
		sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
		t := trip{id, cs}
		if mode == IndependentTrips {
			x.routes = append(x.routes, route{[]trip{t}})
			continue
		}
		stops := []string{cs[0].From}
		for _, c := range cs {
			stops = append(stops, c.To)
		}
		// Repeated-stop patterns can tie at different positions across distinct
		// trips even when each position is strictly ordered. Keep such trips in
		// separate routes to preserve same-route/arrival uniqueness.
		seenStops := map[string]bool{}
		repeated := false
		for _, stop := range stops {
			if seenStops[stop] {
				repeated = true
			}
			seenStops[stop] = true
		}
		if repeated {
			x.routes = append(x.routes, route{[]trip{t}})
			continue
		}
		key, _ := json.Marshal(stops)
		patterns[string(key)] = append(patterns[string(key)], t)
	}
	keys := []string{}
	for k := range patterns {
		keys = append(keys, k)
	}
	sort.Strings(keys)
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
				last := groups[g].trips[len(groups[g].trips)-1]
				ordered := true
				for i, c := range t.cs {
					if e := w.connection(); e != nil {
						return e
					}
					w.s.GroupingComparisons++
					if c.Departure <= last.cs[i].Departure || c.Arrival <= last.cs[i].Arrival {
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
				groups = append(groups, route{[]trip{t}})
			}
		}
		x.routes = append(x.routes, groups...)
	}
	for _, r := range x.routes {
		s := RouteSummary{TripIDs: []string{}, Stops: []string{r.trips[0].cs[0].From}}
		for _, t := range r.trips {
			s.TripIDs = append(s.TripIDs, t.id)
		}
		for _, c := range r.trips[0].cs {
			s.Stops = append(s.Stops, c.To)
		}
		x.s.Routes = append(x.s.Routes, s)
	}
	return nil
}

// Earliest-trip route scan from canonical MR. A switch is permitted only to an
// earlier trip, keeping the earliest boarding index for equal feasible trips.
// Departure/arrival monotonicity is established during grouping. Probe work and
// scanned segments share the explicit connection-work ceiling.
func (x *Index) scanGrouped(prev, curr []label, maximum int64, destination int, w *work) error {
	selected, err := x.collectRoutes(prev, maximum, destination, nil, w)
	if err != nil {
		return err
	}
	for _, selectedRoute := range selected {
		r := x.routes[selectedRoute.route]
		if e := w.check(); e != nil {
			return e
		}
		w.s.RouteScans++
		current := -1
		var parent *trace
		trips := 0
		for pos := selectedRoute.position; pos < len(r.trips[0].cs); pos++ {
			prototype := r.trips[0].cs[pos]
			if e := w.connection(); e != nil {
				return e
			}
			from, to := x.ids[prototype.From], x.ids[prototype.To]
			if from != destination {
				lo, hi := 0, len(r.trips)
				if current >= 0 {
					hi = current
				}
				for lo < hi {
					if e := w.connection(); e != nil {
						return e
					}
					w.s.TripProbes++
					mid := lo + (hi-lo)/2
					if r.trips[mid].cs[pos].Departure >= prev[from].time {
						hi = mid
					} else {
						lo = mid + 1
					}
				}
				// When already on a trip, equal index is deliberately not a switch.
				if lo < len(r.trips) && (current < 0 || lo < current) {
					current = lo
					parent = prev[from].path
					trips = prev[from].trips + 1
				}
			}
			if current < 0 {
				continue
			}
			c := r.trips[current].cs[pos]
			// A later stop can permit an earlier trip even if this arrival is outside
			// the query horizon; do not end the entire route prematurely.
			if c.Arrival <= maximum {
				parent = &trace{previous: parent, kind: "ride", ref: c.ID, departure: c.Departure, arrival: c.Arrival}
				if c.Arrival < curr[to].time {
					curr[to] = label{c.Arrival, trips, parent}
				}
			} else {
				current = -1
				parent = nil
				trips = 0
			}
			if to == destination {
				// Absorb this boarded path, but later positions may board an
				// earlier trip after a previous occurrence of the destination.
				current, parent, trips = -1, nil, 0
			}
		}
	}
	return nil
}
