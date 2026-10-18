package tripbased

import (
	"context"
	"fmt"
	"sort"
)

// Walking weak components are indivisible even for directed footpaths. Recursive
// weighted growth is an explicit heuristic alternative to the paper's native
// partitioners, not a claim to reproduce their balance/cut quality or timings.
func (x *TREXIndex) partitionTREX(ctx context.Context) error {
	n := len(x.names)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(a int) int {
		for parent[a] != a {
			parent[a] = parent[parent[a]]
			a = parent[a]
		}
		return a
	}
	for _, e := range x.base.p.Data.Footpaths {
		if err := x.tick(ctx); err != nil {
			return err
		}
		a, b := root(x.ids[e.From]), root(x.ids[e.To])
		if a > b {
			a, b = b, a
		}
		parent[b] = a
	}
	members := map[int][]int{}
	for i := range parent {
		r := root(i)
		members[r] = append(members[r], i)
	}
	components := []int{}
	for r := range members {
		components = append(components, r)
	}
	sort.Ints(components)
	leaves := 1 << uint(x.cfg.Levels)
	if len(components) < leaves {
		return fmt.Errorf("T-REX capability: %d nonempty leaves require at least that many indivisible walking components; have %d", leaves, len(components))
	}
	adj := make([]map[int]int, n)
	for i := range adj {
		adj[i] = map[int]int{}
	}
	for _, c := range x.base.p.Data.Connections {
		if e := x.tick(ctx); e != nil {
			return e
		}
		a, b := root(x.ids[c.From]), root(x.ids[c.To])
		if a != b {
			adj[a][b]++
			adj[b][a]++
		}
	}
	p := TREXPartition{Method: "recursive weighted graph growth; deterministic affinity/degree/stop-ID ties; observed balance, not native partitioner parity", Cells: map[string]int{}, Components: len(components)}
	degree := make([]int, n)
	for _, r := range components {
		p.ComponentWeights = append(p.ComponentWeights, len(members[r]))
		for _, w := range adj[r] {
			degree[r] += w
		}
		p.LayoutEdges += len(adj[r])
		p.TotalWeight += degree[r]
	}
	p.LayoutEdges /= 2
	p.TotalWeight /= 2
	x.cells = make([]int, n)
	if len(x.cfg.Partition) > 0 {
		p.Method = "explicit nested binary leaf IDs; validated complete coverage and unsplit walking components"
		if len(x.cfg.Partition) != n {
			return fmt.Errorf("T-REX capability: partition must cover exactly all stops")
		}
		used := make([]bool, leaves)
		for i, name := range x.names {
			c, ok := x.cfg.Partition[name]
			if !ok || c < 0 || c >= leaves {
				return fmt.Errorf("T-REX capability: invalid partition leaf")
			}
			x.cells[i] = c
			used[c] = true
		}
		for _, ok := range used {
			if !ok {
				return fmt.Errorf("T-REX capability: partition contains an empty leaf")
			}
		}
		for _, r := range components {
			for _, s := range members[r] {
				if x.cells[s] != x.cells[r] {
					return fmt.Errorf("T-REX capability: partition cuts a walking component")
				}
			}
		}
	} else {
		var split func([]int, int, int) error
		split = func(vs []int, level, prefix int) error {
			if level == 0 {
				for _, v := range vs {
					for _, s := range members[v] {
						x.cells[s] = prefix
					}
				}
				return nil
			}
			total := 0
			for _, v := range vs {
				total += len(members[v])
			}
			minCount := 1 << uint(level-1)
			chosen := map[int]bool{}
			affinity := make([]int, n)
			left := []int{}
			weight := 0
			for len(left) < minCount || (weight < total/2 && len(vs)-len(left) > minCount) {
				best := -1
				for _, v := range vs {
					if e := x.tick(ctx); e != nil {
						return e
					}
					if chosen[v] {
						continue
					}
					if best < 0 || affinity[v] > affinity[best] || affinity[v] == affinity[best] && degree[v] > degree[best] {
						best = v
					}
				}
				chosen[best] = true
				left = append(left, best)
				weight += len(members[best])
				for u, w := range adj[best] {
					affinity[u] += w
				}
			}
			sort.Ints(left)
			right := []int{}
			for _, v := range vs {
				if !chosen[v] {
					right = append(right, v)
				}
			}
			if e := split(left, level-1, prefix*2); e != nil {
				return e
			}
			return split(right, level-1, prefix*2+1)
		}
		if e := split(components, x.cfg.Levels, 0); e != nil {
			return e
		}
	}
	for i, s := range x.names {
		p.Cells[s] = x.cells[i]
	}
	for level := 0; level <= x.cfg.Levels; level++ {
		sizes := make([]int, leaves>>uint(level))
		for _, c := range x.cells {
			sizes[c>>uint(level)]++
		}
		p.LevelSizes = append(p.LevelSizes, sizes)
		cut := 0
		for _, a := range components {
			for b, w := range adj[a] {
				if e := x.tick(ctx); e != nil {
					return e
				}
				if a < b && x.cells[a]>>uint(level) != x.cells[b]>>uint(level) {
					cut += w
				}
			}
		}
		p.CutWeights = append(p.CutWeights, cut)
	}
	x.snap.Partition = p
	return nil
}

func (x *TREXIndex) buildTREXOverlays(ctx context.Context) error {
	n := x.offsets[len(x.offsets)-1]
	for level := 0; level <= x.cfg.Levels; level++ {
		o := trexOverlay{starts: make([]int, n+1)}
		for ti, t := range x.base.trips {
			for i := range t.cs {
				event := x.offsets[ti] + i
				o.starts[event] = len(o.edges)
				for _, id := range x.byEvent[ti][i] {
					if e := x.tick(ctx); e != nil {
						return e
					}
					if x.snap.Transfers[id].Rank >= level {
						if x.snap.Stats.OverlayEntries == x.cfg.Limits.MaxOverlayEntries {
							return fmt.Errorf("T-REX incomplete: overlay entry budget exhausted")
						}
						o.edges = append(o.edges, id)
						x.snap.Stats.OverlayEntries++
					}
				}
			}
		}
		o.starts[n] = len(o.edges)
		x.overlays = append(x.overlays, o)
		succ := make([]int, n)
		for ti, t := range x.base.trips {
			end := len(t.cs)
			for i := end - 1; i >= 0; i-- {
				if e := x.tick(ctx); e != nil {
					return e
				}
				if i+1 == end || x.cells[x.ids[t.cs[i].To]]>>uint(level) != x.cells[x.ids[t.cs[i+1].To]]>>uint(level) {
					succ[x.offsets[ti]+i] = i + 1
				} else {
					succ[x.offsets[ti]+i] = succ[x.offsets[ti]+i+1]
				}
			}
		}
		x.successors = append(x.successors, succ)
	}
	return nil
}
