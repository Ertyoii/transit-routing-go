package tripbased

import (
	"context"
	"fmt"
)

// ClusterPatternDAG groups border patterns by destination cluster (ALENEX2016
// §3.4). Nodes with a common physical stop are merged only while acyclicity is
// preserved. A cycle requires another DAG; a repeated-stop word gets a finite
// occurrence DAG. Query graphs may safely overapproximate the recorded language.
type ClusterPatternArc struct {
	From int    `json:"from"`
	To   int    `json:"to"`
	Kind string `json:"kind"`
}
type ClusterPatternDAG struct {
	Cell          int                 `json:"cell"`
	Stops         []string            `json:"stops"`
	Arcs          []ClusterPatternArc `json:"arcs"`
	OccurrenceDAG bool                `json:"occurrence_dag,omitempty"`
}
type ClusterPatternStats struct {
	Work           int `json:"work"` // diagnostic subset of scalable main work, not additive
	Patterns       int `json:"patterns"`
	DAGs           int `json:"dags"`
	Nodes          int `json:"nodes"`
	Edges          int `json:"edges"`
	RejectedMerges int `json:"rejected_merges"`
	OccurrenceDAGs int `json:"occurrence_dags"`
}
type clusterPatternGraph struct {
	view    ClusterPatternDAG
	ids     map[string]int
	has     map[ClusterPatternArc]bool
	out, in [][]int
}
type clusterPatterns struct {
	graphs                      []*clusterPatternGraph
	byCell                      map[int][]int
	stats                       ClusterPatternStats
	maxDAGs, maxNodes, maxEdges int
	charge                      func(context.Context) error
}

func newClusterPatterns(dags, nodes, edges int, charge func(context.Context) error) *clusterPatterns {
	return &clusterPatterns{byCell: map[int][]int{}, maxDAGs: dags, maxNodes: nodes, maxEdges: edges, charge: charge}
}
func (x *clusterPatterns) tick(ctx context.Context) error {
	if e := x.charge(ctx); e != nil {
		return e
	}
	x.stats.Work++
	return nil
}
func (x *clusterPatterns) finish() {
	x.charge = nil
	for _, g := range x.graphs {
		g.ids = nil
		g.has = nil
	}
}
func (x *clusterPatterns) snapshot() ([]ClusterPatternDAG, ClusterPatternStats) {
	out := make([]ClusterPatternDAG, len(x.graphs))
	for i, g := range x.graphs {
		out[i] = g.view
		out[i].Stops = append([]string{}, g.view.Stops...)
		out[i].Arcs = append([]ClusterPatternArc{}, g.view.Arcs...)
	}
	return out, x.stats
}
func (x *clusterPatterns) newGraph(ctx context.Context, cell int, occurrence bool) (*clusterPatternGraph, error) {
	if e := x.tick(ctx); e != nil {
		return nil, e
	}
	if x.stats.DAGs >= x.maxDAGs {
		return nil, fmt.Errorf("Scalable TP incomplete: cluster DAG count budget")
	}
	g := &clusterPatternGraph{view: ClusterPatternDAG{Cell: cell, Stops: []string{}, Arcs: []ClusterPatternArc{}, OccurrenceDAG: occurrence}, ids: map[string]int{}, has: map[ClusterPatternArc]bool{}}
	x.byCell[cell] = append(x.byCell[cell], len(x.graphs))
	x.graphs = append(x.graphs, g)
	x.stats.DAGs++
	if occurrence {
		x.stats.OccurrenceDAGs++
	}
	return g, nil
}
func (x *clusterPatterns) commit(ctx context.Context, g *clusterPatternGraph, names []string, arcs []ClusterPatternArc) error {
	for _, name := range names {
		if e := x.tick(ctx); e != nil {
			return e
		}
		if x.stats.Nodes >= x.maxNodes {
			return fmt.Errorf("Scalable TP incomplete: shared cluster DAG node budget")
		}
		g.ids[name] = len(g.view.Stops)
		g.view.Stops = append(g.view.Stops, name)
		g.out = append(g.out, nil)
		g.in = append(g.in, nil)
		x.stats.Nodes++
	}
	for _, a := range arcs {
		if e := x.tick(ctx); e != nil {
			return e
		}
		if x.stats.Edges >= x.maxEdges {
			return fmt.Errorf("Scalable TP incomplete: shared cluster DAG edge budget")
		}
		g.view.Arcs = append(g.view.Arcs, a)
		g.has[a] = true
		g.out[a.From] = append(g.out[a.From], a.To)
		g.in[a.To] = append(g.in[a.To], a.From)
		x.stats.Edges++
	}
	return nil
}

// Evaluate a merge transaction without changing the existing graph. Every new
// arc is checked against both old arcs and earlier arcs of this same word.
func (x *clusterPatterns) try(ctx context.Context, g *clusterPatternGraph, word []PatternEdge) (bool, error) {
	added := map[string]int{}
	names := []string{}
	arcs := []ClusterPatternArc{}
	pending := map[int][]int{}
	has := map[ClusterPatternArc]bool{}
	id := func(s string) int {
		if n, ok := g.ids[s]; ok {
			return n
		}
		if n, ok := added[s]; ok {
			return n
		}
		n := len(g.view.Stops) + len(names)
		added[s] = n
		names = append(names, s)
		return n
	}
	for _, e := range word {
		if err := x.tick(ctx); err != nil {
			return false, err
		}
		a := ClusterPatternArc{id(e.From), id(e.To), e.Kind}
		if g.has[a] || has[a] {
			continue
		}
		seen := map[int]bool{a.To: true}
		queue := []int{a.To}
		cycle := false
		for head := 0; head < len(queue); head++ {
			if err := x.tick(ctx); err != nil {
				return false, err
			}
			u := queue[head]
			if u == a.From {
				cycle = true
				break
			}
			visit := func(v int) error {
				if err := x.tick(ctx); err != nil {
					return err
				}
				if !seen[v] {
					seen[v] = true
					queue = append(queue, v)
				}
				return nil
			}
			if u < len(g.out) {
				for _, v := range g.out[u] {
					if err := visit(v); err != nil {
						return false, err
					}
				}
			}
			for _, v := range pending[u] {
				if err := visit(v); err != nil {
					return false, err
				}
			}
		}
		if cycle {
			return false, nil
		}
		has[a] = true
		arcs = append(arcs, a)
		pending[a.From] = append(pending[a.From], a.To)
	}
	if err := x.commit(ctx, g, names, arcs); err != nil {
		return false, err
	}
	return true, nil
}
func (x *clusterPatterns) add(ctx context.Context, cell int, word []PatternEdge) error {
	if len(word) == 0 {
		return fmt.Errorf("Scalable TP internal: empty cluster pattern")
	}
	seen := map[string]bool{word[0].From: true}
	repeated := false
	for i, e := range word {
		if err := x.tick(ctx); err != nil {
			return err
		}
		if i > 0 && word[i-1].To != e.From {
			return fmt.Errorf("Scalable TP internal: disconnected pattern")
		}
		if seen[e.To] {
			repeated = true
		}
		seen[e.To] = true
	}
	if repeated {
		// A finite word can revisit a physical stop. Keep separate occurrences so its
		// storage is acyclic without discarding any edge or suppressing a witness.
		g, e := x.newGraph(ctx, cell, true)
		if e != nil {
			return e
		}
		names := []string{word[0].From}
		arcs := []ClusterPatternArc{}
		for i, e := range word {
			names = append(names, e.To)
			arcs = append(arcs, ClusterPatternArc{i, i + 1, e.Kind})
		}
		if e = x.commit(ctx, g, names, arcs); e != nil {
			return e
		}
		x.stats.Patterns++
		return nil
	}
	for _, i := range x.byCell[cell] {
		g := x.graphs[i]
		if g.view.OccurrenceDAG {
			continue
		}
		ok, e := x.try(ctx, g, word)
		if e != nil {
			return e
		}
		if ok {
			x.stats.Patterns++
			return nil
		}
		x.stats.RejectedMerges++
	}
	g, e := x.newGraph(ctx, cell, false)
	if e != nil {
		return e
	}
	ok, e := x.try(ctx, g, word)
	if e != nil {
		return e
	}
	if !ok {
		return fmt.Errorf("Scalable TP internal: simple word cycles")
	}
	x.stats.Patterns++
	return nil
}

// Extract all edges lying on a source-border to target-border path in each
// destination-cell DAG. Coalescing physical stops may add feasible recombinations
// but cannot delete an inserted pattern. The timetable query validates time/order.
func (x *clusterPatterns) extract(cell int, sources, targets []string, edges map[PatternEdge]bool, stats *PatternQueryStats, tick func() error) error {
	sourceSet, targetSet := map[string]bool{}, map[string]bool{}
	for _, s := range sources {
		sourceSet[s] = true
	}
	for _, t := range targets {
		targetSet[t] = true
	}
	for _, i := range x.byCell[cell] {
		if e := tick(); e != nil {
			return e
		}
		g := x.graphs[i]
		stats.ClusterDAGs++
		forward, backward := make([]bool, len(g.view.Stops)), make([]bool, len(g.view.Stops))
		fq, bq := []int{}, []int{}
		for u, s := range g.view.Stops {
			if e := tick(); e != nil {
				return e
			}
			stats.ClusterNodes++
			if sourceSet[s] {
				forward[u] = true
				fq = append(fq, u)
			}
			if targetSet[s] {
				backward[u] = true
				bq = append(bq, u)
			}
		}
		walk := func(queue []int, seen []bool, adj [][]int) error {
			for head := 0; head < len(queue); head++ {
				if e := tick(); e != nil {
					return e
				}
				for _, v := range adj[queue[head]] {
					if e := tick(); e != nil {
						return e
					}
					if !seen[v] {
						seen[v] = true
						queue = append(queue, v)
					}
				}
			}
			return nil
		}
		if e := walk(fq, forward, g.out); e != nil {
			return e
		}
		if e := walk(bq, backward, g.in); e != nil {
			return e
		}
		for _, a := range g.view.Arcs {
			if e := tick(); e != nil {
				return e
			}
			if forward[a.From] && backward[a.To] {
				edges[PatternEdge{g.view.Stops[a.From], g.view.Stops[a.To], a.Kind}] = true
			}
		}
	}
	return nil
}
