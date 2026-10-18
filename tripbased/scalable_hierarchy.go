package tripbased

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"slices"
	"sort"
)

func (x *ScalableIndex) classifyHierarchy(ctx context.Context, d model.Dataset) error {
	base := x.local.base
	x.longTrips = map[string]bool{}
	known := map[string]bool{}
	for _, t := range d.Trips {
		if e := ctx.Err(); e != nil {
			return e
		}
		known[t.ID] = true
	}
	for _, id := range x.cfg.LongDistanceTrips {
		if e := x.tick(ctx); e != nil {
			return e
		}
		if !known[id] || x.longTrips[id] {
			return fmt.Errorf("Scalable TP hierarchy: unknown/duplicate long-distance trip %q", id)
		}
		x.longTrips[id] = true
	}
	x.longStops = make([]bool, len(base.names))
	x.hubs = make([]bool, len(base.names))
	for _, c := range d.Connections {
		if len(x.cfg.LongDistanceTrips) > 0 {
			if e := x.tick(ctx); e != nil {
				return e
			}
		}
		if x.longTrips[c.TripID] {
			x.longStops[base.ids[c.From]] = true
			x.longStops[base.ids[c.To]] = true
		}
	}
	for _, id := range x.cfg.Hubs {
		u, ok := base.ids[id]
		if !ok || !x.longStops[u] || x.hubs[u] {
			return fmt.Errorf("Scalable TP hierarchy: hub must be a unique long-distance stop: %q", id)
		}
		x.hubs[u] = true
	}
	for u, ok := range x.longStops {
		if ok {
			x.longStations = append(x.longStations, u)
			x.snap.LongStations = append(x.snap.LongStations, base.names[u])
		}
	}
	x.snap.Stats.LongDistanceStations = len(x.longStations)
	x.snap.Stats.Hubs = len(x.cfg.Hubs)
	slices.Sort(x.cfg.LongDistanceTrips)
	slices.Sort(x.cfg.Hubs)
	if len(x.longStations) > 0 && x.cfg.BorderSearchMode == "expanded" {
		return fmt.Errorf("Scalable TP hierarchy: expanded ablation requires empty classification")
	}
	return nil
}
func (x *ScalableIndex) initializeHierarchy(ctx context.Context) error {
	x.portals = slices.Clone(x.borderSet)
	for _, u := range x.longStations {
		x.portals[u] = true
	}
	if len(x.longStations) == 0 {
		return nil
	}
	// Retain line indices/occurrences; filtering a strict FIFO trip list preserves FIFO.
	p := *x.local
	b := *p.base
	cp := *b.cp
	cp.lines = make([]line, len(b.cp.lines))
	for li, l := range b.cp.lines {
		for _, ti := range l.trips {
			if e := x.tick(ctx); e != nil {
				return e
			}
			if !x.longTrips[cp.trips[ti].id] {
				cp.lines[li].trips = append(cp.lines[li].trips, ti)
			}
		}
	}
	b.cp = &cp
	p.base = &b
	x.localDirect = &p
	x.long = emptyPatterns(x.cfg.Patterns)
	x.long.base = x.local.base
	x.long.incident = x.local.incident
	x.long.linePairs = x.local.linePairs
	x.long.pairCache = x.local.pairCache
	x.long.connections = x.local.connections
	x.long.footpaths = x.local.footpaths
	x.long.buildCharge = x.charge
	x.access = map[int]map[int]bool{}
	x.longGraphs = map[int]map[int][]PatternEdge{}
	x.snap.HubAccess = map[string][]string{}
	return nil
}
func (x *ScalableIndex) portalTargets(cell int) []int {
	if len(x.longStations) == 0 {
		return x.borders[cell]
	}
	a := []int{}
	for u, ok := range x.portals {
		if ok && x.cells[u] == cell {
			a = append(a, u)
		}
	}
	return a
}
func (x *ScalableIndex) buildLongPatterns(ctx context.Context, cross []PatternEdge) error {
	targets := map[int]bool{}
	for _, u := range x.longStations {
		targets[u] = true
	}
	if e := x.profilesMode(ctx, x.long, cross, x.longStations, targets, "long"); e != nil {
		return e
	}
	for source, hs := range x.access {
		names := []string{}
		for h := range hs {
			names = append(names, x.local.base.names[h])
		}
		sort.Strings(names)
		x.snap.HubAccess[x.local.base.names[source]] = names
	}
	return nil
}
func (x *ScalableIndex) recordAccess(ctx context.Context, source int, labels map[patternState]*patternLabel) error {
	if x.hubs[source] {
		return nil
	}
	if x.access[source] == nil {
		x.access[source] = map[int]bool{}
	}
	for k := range labels {
		if e := x.tick(ctx); e != nil {
			return e
		}
		if x.hubs[k.stop] {
			x.access[source][k.stop] = true
		}
	}
	return nil
}
func (x *ScalableIndex) localCrossGraph(ctx context.Context) (map[PatternEdge]bool, error) {
	out := map[PatternEdge]bool{}
	base := x.local.base
	for li, l := range x.localDirect.base.cp.lines {
		if len(l.trips) == 0 {
			continue
		}
		cs := base.cp.trips[base.cp.lines[li].trips[0]].cs
		for i, a := range cs {
			for j := i; j < len(cs); j++ {
				if e := x.tick(ctx); e != nil {
					return nil, e
				}
				if x.cells[base.ids[a.From]] != x.cells[base.ids[cs[j].To]] {
					out[PatternEdge{a.From, cs[j].To, "ride"}] = true
				}
			}
		}
	}
	return out, nil
}
func (x *ScalableIndex) longGraph(ctx context.Context, source int) (map[int][]PatternEdge, error) {
	if g, ok := x.longGraphs[source]; ok {
		return g, nil
	}
	base := x.local.base
	set := map[PatternEdge]bool{}
	s := PatternQueryStats{}
	tick := func() error { return x.tick(ctx) }
	starts := []int{source}
	for h := range x.access[source] {
		starts = append(starts, h)
	}
	sort.Ints(starts)
	for _, u := range starts {
		for _, v := range x.longStations {
			if e := x.long.extract(base.names[u], base.names[v], set, &s, tick); e != nil {
				return nil, e
			}
		}
	}
	if len(set) > x.cfg.MaxDirectArcs-x.snap.Stats.LongGraphEdges {
		return nil, fmt.Errorf("Scalable TP incomplete: cached hierarchy graph edge budget")
	}
	g := map[int][]PatternEdge{}
	for _, e := range sortedPatternEdges(set) {
		if er := tick(); er != nil {
			return nil, er
		}
		g[base.ids[e.From]] = append(g[base.ids[e.From]], e)
	}
	x.snap.Stats.LongGraphEdges += len(set)
	x.longGraphs[source] = g
	return g, nil
}

// Only border labels enter this outer PQ. Hierarchy evaluations contribute
// feasible labels; ordinary objective dominance prunes slower local alternatives.
func (x *ScalableIndex) hierarchyBorderRun(ctx context.Context, source int, dep int64, cross map[int][]PatternEdge) (map[patternState]*patternLabel, error) {
	base := x.local.base
	labels := map[patternState]*patternLabel{}
	queue := &patternQueue{}
	count := 0
	stats := PatternQueryStats{}
	tick := func() error { return x.tick(ctx) }
	merge := func(stop, b int, walked bool, at int64, parent func() (*paper.Node, error)) error {
		if e := tick(); e != nil {
			return e
		}
		if !x.borderSet[stop] {
			return fmt.Errorf("Scalable TP internal: hierarchy yielded non-border outer label")
		}
		for n := 0; n <= b; n++ {
			if old := labels[patternState{stop, n, walked}]; old != nil && old.at <= at {
				return nil
			}
		}
		if count >= x.cfg.MaxProfileLabels {
			return fmt.Errorf("Scalable TP incomplete: global border label budget")
		}
		p, e := parent()
		if e != nil {
			return e
		}
		l := &patternLabel{stop, b, walked, at, p, count}
		count++
		x.snap.Stats.ProfileLabels++
		x.snap.Stats.GlobalQueueLabels++
		labels[patternState{stop, b, walked}] = l
		heap.Push(queue, l)
		return nil
	}
	if e := merge(source, 0, false, dep, func() (*paper.Node, error) { return nil, nil }); e != nil {
		return nil, e
	}
	for queue.Len() > 0 {
		if e := tick(); e != nil {
			return nil, e
		}
		l := heap.Pop(queue).(*patternLabel)
		if labels[patternState{l.stop, l.boards, l.walked}] != l {
			continue
		}
		local, e := x.localClosure(ctx, l.stop, l.at, l.walked, x.cfg.Patterns.Profile.MaxBoardings-l.boards)
		if e != nil {
			return nil, e
		}
		for _, r := range local {
			if x.borderSet[r.stop] {
				if e := merge(r.stop, l.boards+r.boards, r.walked, r.at, func() (*paper.Node, error) { return x.appendLocal(ctx, l.parent, r.parent) }); e != nil {
					return nil, e
				}
			}
			if !x.longStops[r.stop] {
				continue
			}
			g, e := x.longGraph(ctx, r.stop)
			if e != nil {
				return nil, e
			}
			x.snap.Stats.LongEvaluations++
			ls, e := x.localRun(ctx, r.stop, r.at, r.walked, x.cfg.Patterns.Profile.MaxBoardings-l.boards-r.boards, g)
			if e != nil {
				return nil, e
			}
			// Stable traversal matters for representative ties and deterministic budgets.
			keys := []patternState{}
			for k := range ls {
				if x.longStops[k.stop] {
					keys = append(keys, k)
				}
			}
			sort.Slice(keys, func(i, j int) bool {
				a, b := keys[i], keys[j]
				if a.stop != b.stop {
					return a.stop < b.stop
				}
				if a.boards != b.boards {
					return a.boards < b.boards
				}
				return !a.walked && b.walked
			})
			for _, k := range keys {
				z := ls[k]
				tail, e := x.localClosure(ctx, z.stop, z.at, z.walked, x.cfg.Patterns.Profile.MaxBoardings-l.boards-r.boards-z.boards)
				if e != nil {
					return nil, e
				}
				for _, v := range tail {
					if !x.borderSet[v.stop] {
						continue
					}
					if e := merge(v.stop, l.boards+r.boards+z.boards+v.boards, v.walked, v.at, func() (*paper.Node, error) {
						p, e := x.appendLocal(ctx, l.parent, r.parent)
						if e != nil {
							return nil, e
						}
						p, e = x.appendLocal(ctx, p, z.parent)
						if e != nil {
							return nil, e
						}
						return x.appendLocal(ctx, p, v.parent)
					}); e != nil {
						return nil, e
					}
				}
			}
		}
		for _, edge := range cross[l.stop] {
			if e := tick(); e != nil {
				return nil, e
			}
			if l.boards == x.cfg.Patterns.Profile.MaxBoardings {
				continue
			}
			v := base.ids[edge.To]
			r, ok, e := x.localDirect.direct(l.stop, v, l.at, &stats, tick)
			if e != nil {
				return nil, e
			}
			if !ok {
				continue
			}
			cs := base.cp.trips[r.trip].cs[r.board:r.exit]
			if e := merge(v, l.boards+1, false, cs[len(cs)-1].Arrival, func() (*paper.Node, error) {
				p := l.parent
				for _, c := range cs {
					if e := tick(); e != nil {
						return nil, e
					}
					p = paper.Ride(p, c)
				}
				return p, nil
			}); e != nil {
				return nil, e
			}
		}
	}
	return labels, nil
}
