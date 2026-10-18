package tripbased

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type PatternQueryStats struct {
	DirectCacheHits   int  `json:"direct_cache_hits,omitempty"`
	DirectCacheMisses int  `json:"direct_cache_misses,omitempty"`
	ClusterDAGs       int  `json:"cluster_dags,omitempty"`
	ClusterNodes      int  `json:"cluster_nodes,omitempty"`
	LocalOnly         bool `json:"local_only,omitempty"`
	Work              int  `json:"work"`
	GraphNodes        int  `json:"graph_nodes"`
	GraphEdges        int  `json:"graph_edges"`
	PrefixNodes       int  `json:"prefix_nodes"`
	Labels            int  `json:"labels"`
	Settled           int  `json:"settled"`
	DirectLookups     int  `json:"direct_lookups"`
	LinePairs         int  `json:"line_pairs"`
	BinarySteps       int  `json:"binary_steps"`
	RawLegs           int  `json:"raw_legs"`
}
type PatternResult struct {
	Journeys []model.Journey   `json:"journeys"`
	Complete bool              `json:"complete"`
	Error    string            `json:"error,omitempty"`
	Stats    PatternQueryStats `json:"stats"`
	Graph    []PatternEdge     `json:"query_graph"`
}
type patternLabel struct {
	stop, boards int
	walked       bool
	at           int64
	parent       *paper.Node
	serial       int
}
type patternQueue []*patternLabel

func (q patternQueue) Len() int { return len(q) }
func (q patternQueue) Less(i, j int) bool {
	a, b := q[i], q[j]
	if a.at != b.at {
		return a.at < b.at
	}
	if a.boards != b.boards {
		return a.boards < b.boards
	}
	return a.serial < b.serial
}
func (q patternQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *patternQueue) Push(v any)   { *q = append(*q, v.(*patternLabel)) }
func (q *patternQueue) Pop() any     { a := *q; v := a[len(a)-1]; *q = a[:len(a)-1]; return v }

type patternState struct {
	stop, boards int
	walked       bool
}
type patternRide struct{ trip, board, exit int }

// A direct edge is evaluated against every common FIFO line and all occurrences.
// The incidence intersection is independent of the precomputed pattern witnesses.
func (x *PatternIndex) direct(from, to int, ready int64, s *PatternQueryStats, tick func() error) (patternRide, bool, error) {
	if x.linePairs != nil {
		return x.directIndexed(from, to, ready, s, tick)
	}
	if x.pairCache != nil {
		return x.directDemand(from, to, ready, s, tick)
	}
	return x.directIncidence(from, to, ready, s, tick)
}
func (x *PatternIndex) directIncidence(from, to int, ready int64, s *PatternQueryStats, tick func() error) (patternRide, bool, error) {
	s.DirectLookups++
	a, b := x.incident[from], x.incident[to]
	best := paper.Infinity
	ride := patternRide{}
	i, j := 0, 0
	for i < len(a) && j < len(b) {
		if e := tick(); e != nil {
			return ride, false, e
		}
		if a[i].line < b[j].line {
			i++
			continue
		}
		if b[j].line < a[i].line {
			j++
			continue
		}
		ai, bj := i, j
		for i < len(a) && a[i].line == a[ai].line {
			i++
		}
		for j < len(b) && b[j].line == b[bj].line {
			j++
		}
		line := x.base.cp.lines[a[ai].line]
		for _, u := range a[ai:i] {
			for _, v := range b[bj:j] {
				if e := tick(); e != nil {
					return ride, false, e
				}
				if u.pos >= v.pos {
					continue
				}
				s.LinePairs++
				lo, hi := 0, len(line.trips)
				for lo < hi {
					if e := tick(); e != nil {
						return ride, false, e
					}
					s.BinarySteps++
					m := lo + (hi-lo)/2
					if x.base.cp.trips[line.trips[m]].cs[u.pos].Departure < ready {
						lo = m + 1
					} else {
						hi = m
					}
				}
				if lo == len(line.trips) {
					continue
				}
				ti := line.trips[lo]
				at := x.base.cp.trips[ti].cs[v.pos-1].Arrival
				if at < best {
					best = at
					ride = patternRide{ti, u.pos, v.pos}
				}
			}
		}
	}
	return ride, best < paper.Infinity, nil
}
func (x *PatternIndex) Search(ctx context.Context, input model.Query) (PatternResult, error) {
	return x.searchGraph(ctx, input, nil)
}
func (x *PatternIndex) searchGraph(ctx context.Context, input model.Query, assemble func(model.Query, map[PatternEdge]bool, *PatternQueryStats, func() error) error) (out PatternResult, err error) {
	out.Journeys = []model.Journey{}
	out.Graph = []PatternEdge{}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
			out.Journeys = []model.Journey{}
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if !x.Complete() {
		err = fmt.Errorf("Transfer Patterns incomplete index cannot answer queries")
		return
	}
	q, e := x.base.checkQuery(ctx, input)
	if e != nil {
		err = e
		return
	}
	if q.Origin == q.Destination {
		out.Journeys = append(out.Journeys, paper.Journey(nil))
		return
	}
	tick := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if out.Stats.Work >= x.cfg.MaxQueryWork {
			return fmt.Errorf("Transfer Patterns incomplete: query work budget")
		}
		out.Stats.Work++
		return nil
	}
	edges := map[PatternEdge]bool{}
	if assemble != nil {
		err = assemble(q, edges, &out.Stats, tick)
	} else {
		err = x.extract(q.Origin, q.Destination, edges, &out.Stats, tick)
	}
	if err != nil {
		return
	}
	vertices := map[string]bool{}
	for edge := range edges {
		out.Graph = append(out.Graph, edge)
		vertices[edge.From] = true
		vertices[edge.To] = true
	}
	sort.Slice(out.Graph, func(i, j int) bool {
		a, b := out.Graph[i], out.Graph[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Kind < b.Kind
	})
	out.Stats.GraphNodes = len(vertices)
	out.Stats.GraphEdges = len(out.Graph)
	adj := map[int][]PatternEdge{}
	for _, edge := range out.Graph {
		if err = tick(); err != nil {
			return
		}
		sid := x.base.ids[edge.From]
		adj[sid] = append(adj[sid], edge)
	}
	origin, target := x.base.ids[q.Origin], x.base.ids[q.Destination]
	labels := map[patternState]*patternLabel{}
	queue := &patternQueue{}
	heap.Init(queue)
	merge := func(s, boards int, walked bool, at int64, parent *paper.Node) error {
		if e := tick(); e != nil {
			return e
		}
		if at > q.LatestArrival || boards > q.MaxBoardings {
			return nil
		}
		// Mode-specific labels preserve the alternating walking/vehicle graph.
		for b := 0; b <= boards; b++ {
			if old := labels[patternState{s, b, walked}]; old != nil && old.at <= at {
				return nil
			}
		}
		if out.Stats.Labels >= x.cfg.MaxQueryLabels {
			return fmt.Errorf("Transfer Patterns incomplete: query label budget")
		}
		l := &patternLabel{s, boards, walked, at, parent, out.Stats.Labels}
		out.Stats.Labels++
		labels[patternState{s, boards, walked}] = l
		heap.Push(queue, l)
		return nil
	}
	if err = merge(origin, 0, false, q.Departure, nil); err != nil {
		return
	}
	for queue.Len() > 0 {
		if err = tick(); err != nil {
			return
		}
		l := heap.Pop(queue).(*patternLabel)
		if labels[patternState{l.stop, l.boards, l.walked}] != l {
			continue
		}
		dominated := false
		for b := 0; b < l.boards; b++ {
			if old := labels[patternState{l.stop, b, l.walked}]; old != nil && old.at <= l.at {
				dominated = true
				break
			}
		}
		if dominated || l.stop == target {
			continue
		}
		out.Stats.Settled++
		for _, edge := range adj[l.stop] {
			if err = tick(); err != nil {
				return
			}
			v := x.base.ids[edge.To]
			parent := l.parent
			at := l.at
			boards := l.boards
			walked := edge.Kind == "walk"
			if walked {
				if l.walked {
					continue
				}
				p := x.base.walkMap[l.stop][v]
				if p == nil {
					err = fmt.Errorf("Transfer Patterns internal: absent metric footpath")
					return
				}
				cap := q.Walking.Transfer
				if boards == 0 {
					cap = q.Walking.Access
				}
				if v == target {
					if boards == 0 {
						cap = q.Walking.Direct
					} else {
						cap = q.Walking.Egress
					}
				}
				if p.Duration > cap {
					continue
				}
				for _, w := range p.Edges {
					if err = tick(); err != nil {
						return
					}
					out.Stats.RawLegs++
					parent = &paper.Node{Previous: parent, Leg: model.Leg{Kind: "walk", Ref: w.ID, Departure: at, Arrival: at + w.Duration}}
					at += w.Duration
					if w.To == q.Destination {
						v = target
						break
					}
				}
			} else {
				if boards == q.MaxBoardings {
					continue
				}
				r, ok, e := x.direct(l.stop, v, at, &out.Stats, tick)
				if e != nil {
					err = e
					return
				}
				if !ok {
					continue
				}
				boards++
				for _, c := range x.base.cp.trips[r.trip].cs[r.board:r.exit] {
					if err = tick(); err != nil {
						return
					}
					out.Stats.RawLegs++
					parent = paper.Ride(parent, c)
					at = c.Arrival
					if c.To == q.Destination {
						v = target
						break
					}
				}
			}
			if err = merge(v, boards, walked, at, parent); err != nil {
				return
			}
		}
	}
	// Recombining patterns may split one contiguous vehicle block over two graph
	// arcs. Graph-hop costs are safe upper bounds; reconstruct actual maximal
	// blocks and retain the Pareto representatives in the common journey contract.
	type candidate struct {
		boards  int
		arrival int64
		journey model.Journey
		serial  int
	}
	candidates := []candidate{}
	for key, l := range labels {
		if key.stop != target {
			continue
		}
		j := paper.Journey(l.parent)
		b := 0
		lastTrip := ""
		lastSequence := -2
		for _, leg := range j.Legs {
			if err = tick(); err != nil {
				return
			}
			if leg.Kind == "walk" {
				lastTrip = ""
				lastSequence = -2
				continue
			}
			c := x.connections[leg.Ref]
			if c.TripID != lastTrip || c.Sequence != lastSequence+1 {
				b++
			}
			lastTrip = c.TripID
			lastSequence = c.Sequence
		}
		candidates = append(candidates, candidate{b, l.at, j, l.serial})
	}
	sort.Slice(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if a.boards != b.boards {
			return a.boards < b.boards
		}
		if a.arrival != b.arrival {
			return a.arrival < b.arrival
		}
		return a.serial < b.serial
	})
	best := paper.Infinity
	for _, c := range candidates {
		if c.arrival < best {
			out.Journeys = append(out.Journeys, c.journey)
			best = c.arrival
		}
	}
	return
}

func (x *PatternIndex) extract(source, target string, edges map[PatternEdge]bool, stats *PatternQueryStats, tick func() error) error {
	dag := x.dags[source]
	if dag == nil {
		return nil
	}
	seen := map[int]bool{}
	stack := []int{}
	for _, a := range dag.Targets[target] {
		if err := tick(); err != nil {
			return err
		}
		edges[PatternEdge{dag.Nodes[a.Parent].Stop, target, a.Kind}] = true
		stack = append(stack, a.Parent)
	}
	for len(stack) > 0 {
		if err := tick(); err != nil {
			return err
		}
		i := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[i] {
			continue
		}
		seen[i] = true
		stats.PrefixNodes++
		n := dag.Nodes[i]
		if n.Parent < 0 {
			continue
		}
		edges[PatternEdge{dag.Nodes[n.Parent].Stop, n.Stop, n.Kind}] = true
		stack = append(stack, n.Parent)
	}
	return nil
}
