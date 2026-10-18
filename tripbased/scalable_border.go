package tripbased

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type scalableLocalKey struct {
	source, boards int
	at             int64
	walked         bool
	pruneHubs      bool
}
type scalableLocalResult struct {
	stop, boards int
	at           int64
	walked       bool
	parent       *paper.Node
}

// localClosure evaluates the union of local patterns from this entry border to
// every border in its cluster. Interior labels belong only to the inner search.
// The cache is immutable, build-local and bounded by key+result-record count;
// a full cache simply stops admitting entries, never drops routing work.
func (x *ScalableIndex) localClosure(ctx context.Context, source int, at int64, walked bool, boards int) ([]scalableLocalResult, error) {
	return x.localClosureMode(ctx, source, at, walked, boards, false)
}
func (x *ScalableIndex) localClosureMode(ctx context.Context, source int, at int64, walked bool, boards int, pruneHubs bool) ([]scalableLocalResult, error) {
	key := scalableLocalKey{source, boards, at, walked, pruneHubs}
	if a, ok := x.localCache[key]; ok {
		x.snap.Stats.LocalCacheHits++
		return a, nil
	}
	if e := x.tick(ctx); e != nil {
		return nil, e
	}
	graph, ok := x.localGraphs[source]
	if !ok {
		base := x.local.base
		edges := map[PatternEdge]bool{}
		stats := PatternQueryStats{}
		for _, target := range x.portalTargets(x.cells[source]) {
			if e := x.local.extract(base.names[source], base.names[target], edges, &stats, func() error { return x.tick(ctx) }); e != nil {
				return nil, e
			}
		}
		graph = map[int][]PatternEdge{}
		for _, e := range sortedPatternEdges(edges) {
			if err := x.tick(ctx); err != nil {
				return nil, err
			}
			graph[base.ids[e.From]] = append(graph[base.ids[e.From]], e)
		}
		x.snap.Stats.LocalGraphEdges += len(edges)
		if x.snap.Stats.LocalGraphEdges > x.cfg.MaxDirectArcs {
			return nil, fmt.Errorf("Scalable TP incomplete: cached local graph edge budget")
		}
		x.localGraphs[source] = graph
	}
	x.snap.Stats.LocalEvaluations++
	labels, e := x.localRunMode(ctx, source, at, walked, boards, graph, pruneHubs)
	if e != nil {
		return nil, e
	}
	out := []scalableLocalResult{}
	for k, l := range labels {
		if e := x.tick(ctx); e != nil {
			return nil, e
		}
		if x.portals[k.stop] {
			out = append(out, scalableLocalResult{k.stop, k.boards, l.at, k.walked, l.parent})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.stop != b.stop {
			return a.stop < b.stop
		}
		if a.boards != b.boards {
			return a.boards < b.boards
		}
		if a.walked != b.walked {
			return !a.walked
		}
		return a.at < b.at
	})
	size := len(out) + 1
	if size <= x.cfg.MaxLocalCacheEntries-x.snap.Stats.LocalCacheEntries {
		x.localCache[key] = out
		x.snap.Stats.LocalCacheEntries += size
	}
	return out, nil
}
func (x *ScalableIndex) appendLocal(ctx context.Context, prefix, local *paper.Node) (*paper.Node, error) {
	rev := []*paper.Node{}
	for n := local; n != nil; n = n.Previous {
		if e := x.tick(ctx); e != nil {
			return nil, e
		}
		rev = append(rev, n)
	}
	for i := len(rev) - 1; i >= 0; i-- {
		if e := x.tick(ctx); e != nil {
			return nil, e
		}
		prefix = &paper.Node{Previous: prefix, Leg: rev[i].Leg}
	}
	return prefix, nil
}
func (x *ScalableIndex) borderProfileRun(ctx context.Context, source int, dep int64, cross map[int][]PatternEdge) (map[patternState]*patternLabel, error) {
	if len(x.longStations) > 0 {
		return x.hierarchyBorderRun(ctx, source, dep, cross)
	}
	return x.portalProfileRun(ctx, source, dep, cross, false)
}
func (x *ScalableIndex) portalProfileRun(ctx context.Context, source int, dep int64, cross map[int][]PatternEdge, longPhase bool) (map[patternState]*patternLabel, error) {
	labels := map[patternState]*patternLabel{}
	queue := &patternQueue{}
	count := 0
	stats := PatternQueryStats{}
	tick := func() error { return x.tick(ctx) }
	merge := func(stop, b int, walked bool, at int64, parent func() (*paper.Node, error)) error {
		if e := tick(); e != nil {
			return e
		}
		if !x.portals[stop] {
			return fmt.Errorf("Scalable TP internal: non-border in global queue")
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
		if longPhase {
			x.snap.Stats.LongQueueLabels++
		} else {
			x.snap.Stats.GlobalQueueLabels++
		}
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
		if longPhase && !x.hubs[source] && x.hubs[l.stop] {
			x.snap.Stats.HubStopLabels++
			continue
		}
		for _, edge := range cross[l.stop] {
			if e := tick(); e != nil {
				return nil, e
			}
			if edge.Kind != "ride" {
				return nil, fmt.Errorf("Scalable TP internal: cross-cluster walk")
			}
			if l.boards == x.cfg.Patterns.Profile.MaxBoardings {
				continue
			}
			v := x.local.base.ids[edge.To]
			r, ok, e := x.local.direct(l.stop, v, l.at, &stats, tick)
			if e != nil {
				return nil, e
			}
			if !ok {
				continue
			}
			cs := x.local.base.cp.trips[r.trip].cs[r.board:r.exit]
			at := cs[len(cs)-1].Arrival
			if e := merge(v, l.boards+1, false, at, func() (*paper.Node, error) {
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
		local, e := x.localClosureMode(ctx, l.stop, l.at, l.walked, x.cfg.Patterns.Profile.MaxBoardings-l.boards, longPhase && !x.hubs[source])
		if e != nil {
			return nil, e
		}
		for _, r := range local {
			if e := merge(r.stop, l.boards+r.boards, r.walked, r.at, func() (*paper.Node, error) { return x.appendLocal(ctx, l.parent, r.parent) }); e != nil {
				return nil, e
			}
		}
	}
	return labels, nil
}

// This certifies objective sufficiency for local-only queries over the complete
// bounded profile, not geometric convexity or identity of every equal-cost path.
func (x *ScalableIndex) certifyLocal(ctx context.Context, source int, dep int64, global map[patternState]*patternLabel) error {
	cell := x.cells[source]
	if !x.snap.LocalSufficient[cell] {
		return nil
	}
	local, e := x.localClosure(ctx, source, dep, false, x.cfg.Patterns.Profile.MaxBoardings)
	if e != nil {
		return e
	}
	for _, target := range x.borders[cell] {
		ga, la := paper.Infinity, paper.Infinity
		for b := 0; b <= x.cfg.Patterns.Profile.MaxBoardings; b++ {
			if e := x.tick(ctx); e != nil {
				return e
			}
			for _, walked := range []bool{false, true} {
				if l := global[patternState{target, b, walked}]; l != nil {
					ga = min(ga, l.at)
				}
			}
			for _, l := range local {
				if l.stop == target && l.boards == b {
					la = min(la, l.at)
				}
			}
			if ga < la {
				x.snap.LocalSufficient[cell] = false
				return nil
			}
		}
	}
	return nil
}
