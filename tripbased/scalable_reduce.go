package tripbased

import (
	"context"
	"sort"
)

// ALENEX2016 §3.1.5 leaves the merge heuristic open. This deterministic policy
// merges a pair only if the fraction of its borders removed meets the declared
// threshold and combined size fits a separate postprocessing cap.
func (x *ScalableIndex) reduceBorders(ctx context.Context) error {
	trips := [][]int{}
	base := x.local.base
	for _, line := range base.cp.lines {
		t := base.cp.trips[line.trips[0]]
		seen := map[int]bool{}
		for _, c := range t.cs {
			if e := x.tick(ctx); e != nil {
				return e
			}
			seen[base.ids[c.From]] = true
			seen[base.ids[c.To]] = true
		}
		a := []int{}
		for u := range seen {
			a = append(a, u)
		}
		sort.Ints(a)
		trips = append(trips, a)
	}
	first := true
	for {
		sizes := map[int]int{}
		for _, c := range x.cells {
			sizes[c]++
		}
		crossCount := make([]int, len(x.cells))
		type pair struct{ a, b int }
		removals := map[pair]map[int]int{}
		for _, stops := range trips {
			cells := map[int]bool{}
			for _, u := range stops {
				if e := x.tick(ctx); e != nil {
					return e
				}
				cells[x.cells[u]] = true
			}
			if len(cells) < 2 {
				continue
			}
			for _, u := range stops {
				crossCount[u]++
			}
			if len(cells) != 2 {
				continue
			}
			ids := []int{}
			for c := range cells {
				ids = append(ids, c)
			}
			sort.Ints(ids)
			key := pair{ids[0], ids[1]}
			if sizes[key.a]+sizes[key.b] > x.cfg.BorderMergeMaxSize {
				continue
			}
			if removals[key] == nil {
				removals[key] = map[int]int{}
			}
			for _, u := range stops {
				removals[key][u]++
			}
		}
		count := 0
		byCell := map[int]int{}
		for u, n := range crossCount {
			if n > 0 {
				count++
				byCell[x.cells[u]]++
			}
		}
		if first {
			x.snap.Stats.BorderBeforeReduction = count
			first = false
		}
		x.snap.Stats.BorderAfterReduction = count
		if x.cfg.BorderMergeMaxSize == 0 {
			break
		}
		best := pair{-1, -1}
		bestReduction, bestBefore := 0, 1
		for key, removed := range removals {
			reduction := 0
			for u, n := range removed {
				if e := x.tick(ctx); e != nil {
					return e
				}
				if n == crossCount[u] {
					reduction++
				}
			}
			before := byCell[key.a] + byCell[key.b]
			if reduction == 0 || reduction*100 < before*x.cfg.BorderReductionPercent {
				continue
			}
			better := best.a < 0 || reduction*bestBefore > bestReduction*before || reduction*bestBefore == bestReduction*before && (reduction > bestReduction || reduction == bestReduction && (key.a < best.a || key.a == best.a && key.b < best.b))
			if better {
				best = key
				bestReduction = reduction
				bestBefore = before
			}
		}
		if best.a < 0 {
			break
		}
		for u, c := range x.cells {
			if e := x.tick(ctx); e != nil {
				return e
			}
			if c == best.b {
				x.cells[u] = best.a
			}
		}
		x.snap.Stats.BorderClusterMerges++
	}
	ids := map[int]int{}
	for u, c := range x.cells {
		id, ok := ids[c]
		if !ok {
			id = len(ids)
			ids[c] = id
		}
		x.cells[u] = id
		x.snap.Cells[base.names[u]] = id
	}
	x.snap.Stats.Clusters = len(ids)
	return nil
}
