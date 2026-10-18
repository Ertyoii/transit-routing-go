package tripbased

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"math"
	"sort"
)

type scalableMerge struct {
	a, b, va, vb int
	score        float64
}
type scalableMerges []scalableMerge

func (h scalableMerges) Len() int { return len(h) }
func (h scalableMerges) Less(i, j int) bool {
	a, b := h[i], h[j]
	if a.score != b.score {
		return a.score > b.score
	}
	if a.a != b.a {
		return a.a < b.a
	}
	return a.b < b.b
}
func (h scalableMerges) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *scalableMerges) Push(v any)   { *h = append(*h, v.(scalableMerge)) }
func (h *scalableMerges) Pop() any     { a := *h; v := a[len(a)-1]; *h = a[:len(a)-1]; return v }
func (x *ScalableIndex) partition(ctx context.Context, d model.Dataset) error {
	base := x.local.base
	n := len(base.names)
	x.cells = make([]int, n)
	if len(x.cfg.Partition) > 0 {
		if len(x.cfg.Partition) != n {
			return fmt.Errorf("Scalable TP capability: partition must cover exactly every stop")
		}
		used := map[int]bool{}
		for _, s := range base.names {
			v, ok := x.cfg.Partition[s]
			if !ok || v < 0 {
				return fmt.Errorf("Scalable TP capability: missing/negative partition cell")
			}
			used[v] = true
		}
		keys := []int{}
		for k := range used {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		ids := map[int]int{}
		for i, k := range keys {
			ids[k] = i
		}
		for i, s := range base.names {
			x.cells[i] = ids[x.cfg.Partition[s]]
		}
	} else {
		parent, size, version := make([]int, n), make([]int, n), make([]int, n)
		for i := range parent {
			parent[i] = i
			size[i] = 1
		}
		var root func(int) int
		root = func(a int) int {
			for parent[a] != a {
				parent[a] = parent[parent[a]]
				a = parent[a]
			}
			return a
		}
		// All directed walking edges induce an undirected, indivisible component.
		for _, w := range d.Footpaths {
			if e := x.tick(ctx); e != nil {
				return e
			}
			a, b := root(base.ids[w.From]), root(base.ids[w.To])
			if a != b {
				if a > b {
					a, b = b, a
				}
				parent[b] = a
				size[a] += size[b]
			}
		}
		adj := make([]map[int]int, n)
		for i := range adj {
			adj[i] = map[int]int{}
		}
		for _, c := range d.Connections {
			if x.longTrips[c.TripID] {
				continue
			}
			if e := x.tick(ctx); e != nil {
				return e
			}
			a, b := root(base.ids[c.From]), root(base.ids[c.To])
			if a != b {
				adj[a][b]++
				adj[b][a]++
			}
		}
		h := &scalableMerges{}
		push := func(a, b int) {
			if a > b {
				a, b = b, a
			}
			if a == b || size[a]+size[b] > x.cfg.MaxClusterSize {
				return
			}
			w := float64(adj[a][b])
			sa, sb := float64(size[a]), float64(size[b])
			score := w * (1/math.Sqrt(sa) + 1/math.Sqrt(sb)) / (sa * sb)
			heap.Push(h, scalableMerge{a, b, version[a], version[b], score})
		}
		for a := range adj {
			for b := range adj[a] {
				if a < b {
					push(a, b)
				}
			}
		}
		for h.Len() > 0 {
			if e := x.tick(ctx); e != nil {
				return e
			}
			v := heap.Pop(h).(scalableMerge)
			a, b := v.a, v.b
			if root(a) != a || root(b) != b || version[a] != v.va || version[b] != v.vb {
				continue
			}
			if size[a]+size[b] > x.cfg.MaxClusterSize {
				continue
			}
			parent[b] = a
			size[a] += size[b]
			version[a]++
			version[b]++
			delete(adj[a], b)
			keys := []int{}
			for u := range adj[b] {
				keys = append(keys, u)
			}
			sort.Ints(keys)
			for _, u := range keys {
				if e := x.tick(ctx); e != nil {
					return e
				}
				if u == a {
					continue
				}
				weight := adj[b][u]
				delete(adj[u], b)
				adj[u][a] += weight
				adj[a][u] += weight
			}
			adj[b] = nil
			keys = keys[:0]
			for u := range adj[a] {
				keys = append(keys, u)
			}
			sort.Ints(keys)
			for _, u := range keys {
				push(a, u)
			}
		}
		ids := map[int]int{}
		for i := range x.cells {
			r := root(i)
			id, ok := ids[r]
			if !ok {
				id = len(ids)
				ids[r] = id
			}
			x.cells[i] = id
		}
	}
	counts := map[int]int{}
	for i, c := range x.cells {
		counts[c]++
		x.snap.Cells[base.names[i]] = c
	}
	for _, count := range counts {
		if count > x.cfg.MaxClusterSize {
			return fmt.Errorf("Scalable TP incomplete: indivisible/supplied cluster exceeds size budget")
		}
	}
	for _, w := range d.Footpaths {
		if x.cells[base.ids[w.From]] != x.cells[base.ids[w.To]] {
			return fmt.Errorf("Scalable TP capability: cross-cluster walking unsupported")
		}
	}
	x.snap.Stats.Clusters = len(counts)
	return nil
}
