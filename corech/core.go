// Package corech implements exact partial contraction for a positive directed
// transfer graph. Protected vertices remain in the core. It is not ULTRA's
// timetable-dependent shortcut computation. See docs/CORE_CH_DESIGN.md.
package corech

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

const Infinity int64 = 1 << 62

type Config struct {
	TargetCoreVertices          int     `json:"target_core_vertices"`
	MaximumAverageCoreOutDegree float64 `json:"maximum_average_core_out_degree"`
	WitnessSettledPerSearch     int     `json:"witness_settled_per_search"`
	MaxContractions             int     `json:"max_contractions"`
	MaxArcs                     int     `json:"max_archived_arcs"`
	MaxWitnessSettled           int     `json:"max_witness_settled"`
	MaxWitnessEdges             int     `json:"max_witness_edges"`
}
type Node struct {
	ID        string `json:"id"`
	Protected bool   `json:"protected"`
	Rank      int    `json:"rank"`
	Core      bool   `json:"core"`
}
type Arc struct {
	From     int    `json:"from"`
	To       int    `json:"to"`
	Duration int64  `json:"duration"`
	Left     int    `json:"left"`
	Right    int    `json:"right"`
	RawRef   string `json:"raw_ref,omitempty"`
}
type Stats struct {
	OriginalVertices       int    `json:"original_vertices"`
	OriginalEdges          int    `json:"original_edges"`
	SuppressedEdges        int    `json:"suppressed_parallel_or_self_edges"`
	Contractions           int    `json:"contractions"`
	Shortcuts              int    `json:"shortcuts"`
	WitnessSearches        int    `json:"witness_searches"`
	LimitedWitnessSearches int    `json:"limited_witness_searches"`
	WitnessSettled         int    `json:"witness_settled"`
	WitnessEdges           int    `json:"witness_edge_visits"`
	WitnessOmissions       int    `json:"witness_omissions"`
	CoreVertices           int    `json:"core_vertices"`
	CoreEdges              int    `json:"core_edges"`
	StopReason             string `json:"stop_reason"`
}
type Snapshot struct {
	Complete   bool   `json:"complete"`
	Error      string `json:"error,omitempty"`
	Config     Config `json:"config"`
	Stats      Stats  `json:"stats"`
	Nodes      []Node `json:"nodes"`
	Arcs       []Arc  `json:"arcs"`
	CoreArcIDs []int  `json:"core_arc_ids"`
}
type Index struct {
	s   Snapshot
	ids map[string]int
	out [][]int
}

func (x *Index) Snapshot() Snapshot {
	s := x.s
	s.Nodes = append([]Node{}, s.Nodes...)
	s.Arcs = append([]Arc{}, s.Arcs...)
	s.CoreArcIDs = append([]int{}, s.CoreArcIDs...)
	return s
}
func (x *Index) Stats() Stats   { return x.s.Stats }
func (x *Index) Complete() bool { return x.s.Complete }

type item struct {
	v        int
	distance int64
	version  int
}
type queue []item

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	if q[i].distance != q[j].distance {
		return q[i].distance < q[j].distance
	}
	return q[i].v < q[j].v
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(v any)   { *q = append(*q, v.(item)) }
func (q *queue) Pop() any     { a := *q; v := a[len(a)-1]; *q = a[:len(a)-1]; return v }
func keys(m map[int]int) []int {
	a := make([]int, 0, len(m))
	for k := range m {
		a = append(a, k)
	}
	sort.Ints(a)
	return a
}

// Build preserves all shortest distances between retained core vertices. A local
// witness cap can only add redundant exact shortcuts. A global work failure
// returns an unusable, explicitly incomplete index with diagnostic counters.
func Build(ctx context.Context, vertices []string, edges []model.Footpath, protected []string, cfg Config) (x *Index, err error) {
	x = &Index{s: Snapshot{Config: cfg, Nodes: []Node{}, Arcs: []Arc{}, CoreArcIDs: []int{}}, ids: map[string]int{}}
	defer func() {
		x.s.Complete = err == nil
		if err != nil {
			x.s.Error = err.Error()
		}
	}()
	if cfg.TargetCoreVertices < 0 || cfg.MaximumAverageCoreOutDegree <= 0 || cfg.WitnessSettledPerSearch <= 0 || cfg.MaxContractions <= 0 || cfg.MaxArcs <= 0 || cfg.MaxWitnessSettled <= 0 || cfg.MaxWitnessEdges <= 0 {
		err = fmt.Errorf("Core-CH requires explicit positive limits and nonnegative target size")
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	vs := append([]string{}, vertices...)
	sort.Strings(vs)
	for _, id := range vs {
		if id == "" {
			err = fmt.Errorf("empty vertex")
			return
		}
		if _, ok := x.ids[id]; ok {
			err = fmt.Errorf("duplicate vertex %q", id)
			return
		}
		x.ids[id] = len(x.s.Nodes)
		x.s.Nodes = append(x.s.Nodes, Node{ID: id, Rank: -1, Core: true})
	}
	for _, id := range protected {
		v, ok := x.ids[id]
		if !ok {
			err = fmt.Errorf("unknown protected vertex %q", id)
			return
		}
		x.s.Nodes[v].Protected = true
	}
	n := len(vs)
	if n == 0 || cfg.TargetCoreVertices > n {
		err = fmt.Errorf("invalid core target or empty graph")
		return
	}
	outs, ins := make([]map[int]int, n), make([]map[int]int, n)
	activeEdges := 0
	insert := func(a Arc) error {
		if len(x.s.Arcs) == cfg.MaxArcs {
			return fmt.Errorf("Core-CH incomplete: archived arc budget exhausted")
		}
		id := len(x.s.Arcs)
		x.s.Arcs = append(x.s.Arcs, a)
		if outs[a.From] == nil {
			outs[a.From] = map[int]int{}
		}
		if ins[a.To] == nil {
			ins[a.To] = map[int]int{}
		}
		if _, ok := outs[a.From][a.To]; !ok {
			activeEdges++
		}
		x.s.Stats.CoreEdges = activeEdges
		outs[a.From][a.To] = id
		ins[a.To][a.From] = id
		return nil
	}
	es := append([]model.Footpath{}, edges...)
	sort.Slice(es, func(i, j int) bool { return es[i].ID < es[j].ID })
	seen := map[string]bool{}
	x.s.Stats.OriginalVertices = n
	x.s.Stats.OriginalEdges = len(es)
	for i, e := range es {
		if i%1024 == 0 {
			if err = ctx.Err(); err != nil {
				return
			}
		}
		u, uo := x.ids[e.From]
		v, vo := x.ids[e.To]
		if e.ID == "" || seen[e.ID] || !uo || !vo || e.Duration <= 0 || e.Duration >= Infinity/int64(n+1) {
			err = fmt.Errorf("invalid/duplicate transfer edge %q", e.ID)
			return
		}
		seen[e.ID] = true
		if u == v {
			x.s.Stats.SuppressedEdges++
			continue
		}
		if prior, ok := outs[u][v]; ok && x.s.Arcs[prior].Duration <= e.Duration {
			x.s.Stats.SuppressedEdges++
			continue
		}
		if _, ok := outs[u][v]; ok {
			x.s.Stats.SuppressedEdges++
		}
		if err = insert(Arc{From: u, To: v, Duration: e.Duration, Left: -1, Right: -1, RawRef: e.ID}); err != nil {
			return
		}
	}
	order := &queue{}
	versions := make([]int, n)
	coreN := n
	x.s.Stats.CoreVertices = coreN
	update := func(v int) {
		if !x.s.Nodes[v].Core || x.s.Nodes[v].Protected {
			return
		}
		versions[v]++
		a, b := int64(len(ins[v])), int64(len(outs[v]))
		heap.Push(order, item{v, a*b - a - b, versions[v]})
	}
	for v := range x.s.Nodes {
		update(v)
	}
	// Distances are actual paths avoiding forbidden. Even unsettled tentative
	// distances are valid witnesses; no lower-bound approximation can omit an arc.
	witness := func(source, forbidden int, cap int64) (map[int]int64, error) {
		x.s.Stats.WitnessSearches++
		dist := map[int]int64{source: 0}
		h := &queue{{v: source}}
		settled := 0
		for h.Len() > 0 {
			if e := ctx.Err(); e != nil {
				return nil, e
			}
			u := heap.Pop(h).(item)
			if dist[u.v] != u.distance {
				continue
			}
			if u.distance > cap {
				break
			}
			if settled == cfg.WitnessSettledPerSearch {
				x.s.Stats.LimitedWitnessSearches++
				break
			}
			if x.s.Stats.WitnessSettled == cfg.MaxWitnessSettled {
				return nil, fmt.Errorf("Core-CH incomplete: global witness settled budget exhausted")
			}
			settled++
			x.s.Stats.WitnessSettled++
			// Sorting makes equal-distance witness discovery and local-cap behavior
			// deterministic under input permutation and Go map iteration.
			for _, to := range keys(outs[u.v]) {
				if x.s.Stats.WitnessEdges == cfg.MaxWitnessEdges {
					return nil, fmt.Errorf("Core-CH incomplete: global witness edge budget exhausted")
				}
				x.s.Stats.WitnessEdges++
				if to == forbidden {
					continue
				}
				a := x.s.Arcs[outs[u.v][to]]
				next := u.distance + a.Duration
				if next > cap {
					continue
				}
				if old, ok := dist[to]; !ok || next < old {
					dist[to] = next
					heap.Push(h, item{v: to, distance: next})
				}
			}
		}
		return dist, nil
	}
	for order.Len() > 0 {
		if err = ctx.Err(); err != nil {
			return
		}
		if coreN <= cfg.TargetCoreVertices {
			x.s.Stats.StopReason = "target_core_size"
			break
		}
		if float64(activeEdges)/float64(coreN) >= cfg.MaximumAverageCoreOutDegree {
			x.s.Stats.StopReason = "average_core_out_degree"
			break
		}
		top := heap.Pop(order).(item)
		v := top.v
		if !x.s.Nodes[v].Core || x.s.Nodes[v].Protected || versions[v] != top.version {
			continue
		}
		if x.s.Stats.Contractions == cfg.MaxContractions {
			err = fmt.Errorf("Core-CH incomplete: contraction budget exhausted")
			return
		}
		incoming, outgoing := keys(ins[v]), keys(outs[v])
		neighbors := map[int]bool{}
		for _, u := range incoming {
			left := ins[v][u]
			cap := int64(0)
			for _, w := range outgoing {
				if w != u {
					cap = max(cap, x.s.Arcs[left].Duration+x.s.Arcs[outs[v][w]].Duration)
				}
			}
			if cap >= Infinity {
				err = fmt.Errorf("Core-CH path weight exceeds supported range")
				return
			}
			if cap == 0 {
				continue
			}
			var ds map[int]int64
			ds, err = witness(u, v, cap)
			if err != nil {
				return
			}
			for _, w := range outgoing {
				if w == u {
					continue
				}
				right := outs[v][w]
				length := x.s.Arcs[left].Duration + x.s.Arcs[right].Duration
				if length >= Infinity {
					err = fmt.Errorf("Core-CH path weight exceeds supported range")
					return
				}
				if found, ok := ds[w]; ok && found <= length {
					x.s.Stats.WitnessOmissions++
					continue
				}
				if old, ok := outs[u][w]; ok && x.s.Arcs[old].Duration <= length {
					x.s.Stats.WitnessOmissions++
					continue
				}
				if err = insert(Arc{From: u, To: w, Duration: length, Left: left, Right: right}); err != nil {
					return
				}
				x.s.Stats.Shortcuts++
			}
		}
		for _, u := range incoming {
			delete(outs[u], v)
			activeEdges--
			neighbors[u] = true
		}
		for _, w := range outgoing {
			delete(ins[w], v)
			activeEdges--
			neighbors[w] = true
		}
		outs[v], ins[v] = nil, nil
		x.s.Nodes[v].Core = false
		x.s.Nodes[v].Rank = x.s.Stats.Contractions
		x.s.Stats.Contractions++
		coreN--
		x.s.Stats.CoreVertices = coreN
		x.s.Stats.CoreEdges = activeEdges
		ns := []int{}
		for u := range neighbors {
			ns = append(ns, u)
		}
		sort.Ints(ns)
		for _, u := range ns {
			update(u)
		}
	}
	if x.s.Stats.StopReason == "" {
		x.s.Stats.StopReason = "all_unprotected_contracted"
	}
	x.out = make([][]int, n)
	for u := range x.s.Nodes {
		if !x.s.Nodes[u].Core {
			continue
		}
		for _, v := range keys(outs[u]) {
			id := outs[u][v]
			x.out[u] = append(x.out[u], id)
			x.s.CoreArcIDs = append(x.s.CoreArcIDs, id)
		}
	}
	x.s.Stats.CoreVertices = coreN
	x.s.Stats.CoreEdges = activeEdges
	return
}
