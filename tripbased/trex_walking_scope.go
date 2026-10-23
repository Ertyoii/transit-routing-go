package tripbased

import (
	"container/heap"
	"context"
	"fmt"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// endpointWalks is query-local: only explicit fixed-transfer-cap adapters use it.
// target < 0 emits every reachable endpoint; otherwise it emits only target.
// Every raw walking vertex remains available as an intermediate. Positive edges
// make radius pruning exact and mean reaching stopAt is terminal, never a new
// walking-block origin. Output paths retain raw directed edge provenance.
func (x *TREXIndex) endpointWalks(ctx context.Context, source, target int, limit int64, stopAt int, charge func() error, stats *TREXQueryStats) (out []paper.Path, err error) {
	before := stats.Work
	defer func() { stats.EndpointWalkWork += stats.Work - before }()
	dist := map[int]int64{source: 0}
	prev := map[int]model.Footpath{}
	h := &flashHeap{{source, 0}}
	found := false
	for h.Len() > 0 {
		if err = charge(); err != nil {
			return nil, err
		}
		u := heap.Pop(h).(flashHeapItem)
		if dist[u.v] != u.distance {
			continue
		}
		if stats.EndpointWalkSettled >= x.cfg.Limits.MaxWalkSettled {
			return nil, fmt.Errorf("T-REX incomplete: query walking settlement budget exhausted")
		}
		stats.EndpointWalkSettled++
		if u.v == target {
			found = true
			break
		}
		if u.v == stopAt {
			continue
		}
		for _, edge := range x.base.p.Outgoing[x.names[u.v]] {
			if err = charge(); err != nil {
				return nil, err
			}
			value := u.distance + edge.Duration
			if value > limit {
				continue
			}
			v := x.ids[edge.To]
			old, ok := dist[v]
			if !ok || value < old {
				dist[v] = value
				prev[v] = edge
				heap.Push(h, flashHeapItem{v, value})
			}
		}
	}
	stops := []int{}
	if target >= 0 {
		if found {
			stops = append(stops, target)
		}
	} else {
		for v := range dist {
			stops = append(stops, v)
		}
		sort.Ints(stops)
	}
	for _, v := range stops {
		if err = charge(); err != nil {
			return nil, err
		}
		p := paper.Path{To: x.names[v], Duration: dist[v], Edges: []model.Footpath{}}
		for u := v; u != source; {
			if err = charge(); err != nil {
				return nil, err
			}
			edge := prev[u]
			p.Edges = append(p.Edges, edge)
			u = x.ids[edge.From]
		}
		for i, j := 0, len(p.Edges)-1; i < j; i, j = i+1, j-1 {
			p.Edges[i], p.Edges[j] = p.Edges[j], p.Edges[i]
		}
		out = append(out, p)
	}
	return out, ctx.Err()
}

// A fixed-cap index still exposes the original nonbinding query entrypoints.
// Reconstruct their original complete walking metric locally instead of ever
// using a truncated diameter as a nonbinding certificate. This fallback is not
// used by the capped adapters and never changes the prepared index.
func (x *TREXIndex) searchTREXCompleteWalking(ctx context.Context, q model.Query, mode string, capCertificate bool) (out TREXResult, err error) {
	out.Journeys = []model.Journey{}
	out.Stats.Mode = mode
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if err = x.checkTREXResources(ctx, q); err != nil {
		return
	}
	l := x.cfg.Limits
	// Original closure construction began immediately after timetable work.
	startWork := x.snap.Phases["timetable"].Work
	maxWork := startWork + min(l.MaxWork-startWork, l.MaxQueryWork)
	carrier := &FlashIndex{names: x.names, ids: x.ids,
		cfg:  FlashConfig{Limits: FlashLimits{MaxWork: maxWork, MaxClosurePaths: l.MaxClosurePaths, MaxWalkSettled: l.MaxWalkSettled}},
		snap: FlashSnapshot{Stats: FlashStats{Work: startWork}}}
	err = carrier.closeWalks(x.base.p.Data, &flashWork{ctx, carrier})
	work := carrier.snap.Stats.Work - startWork
	out.Stats.Work, out.Stats.EndpointWalkWork = work, work
	out.Stats.EndpointWalkSettled = carrier.snap.Stats.WalkSettled
	if err != nil {
		return
	}
	if work >= l.MaxQueryWork {
		err = fmt.Errorf("T-REX incomplete: query work budget exhausted by complete walking fallback")
		return
	}
	view := *x
	view.snap = x.snap
	view.snap.WalkingClosureLimit = nil // terminates the fallback, not the fixed transfer policy
	view.snap.ClosureDiameter = carrier.snap.ClosureDiameter
	view.walks, view.walkMap = carrier.walks, carrier.walkMap
	view.cfg.Limits.MaxQueryWork -= work
	out, err = view.searchTREXForCertificate(ctx, q, mode, capCertificate)
	out.Stats.Work += work
	out.Stats.EndpointWalkWork += work
	out.Stats.EndpointWalkSettled += carrier.snap.Stats.WalkSettled
	return
}
