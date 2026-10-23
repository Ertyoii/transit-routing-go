package tripbased

import (
	"container/heap"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type flashHeapItem struct {
	v        int
	distance int64
}
type flashHeap []flashHeapItem

func (h flashHeap) Len() int { return len(h) }
func (h flashHeap) Less(i, j int) bool {
	if h[i].distance == h[j].distance {
		return h[i].v < h[j].v
	}
	return h[i].distance < h[j].distance
}
func (h flashHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *flashHeap) Push(v any)   { *h = append(*h, v.(flashHeapItem)) }
func (h *flashHeap) Pop() any     { a := *h; v := a[len(a)-1]; *h = a[:len(a)-1]; return v }
func (x *FlashIndex) closeWalks(d model.Dataset, w *flashWork) error {
	return x.closeWalksWithin(d, w, nil)
}

// closeWalksWithin retains exact directed shortest paths of length <= limit.
// A positive path within the radius has every prefix within that radius, so
// pruning only over-radius relaxations cannot discard a feasible endpoint or
// intermediate vertex. Nil preserves the complete legacy metric and guards.
func (x *FlashIndex) closeWalksWithin(d model.Dataset, w *flashWork, limit *int64) error {
	n := len(x.names)
	adj := make([][]model.Footpath, n)
	for _, e := range d.Footpaths {
		adj[x.ids[e.From]] = append(adj[x.ids[e.From]], e)
	}
	for i := range adj {
		sort.Slice(adj[i], func(a, b int) bool { return adj[i][a].ID < adj[i][b].ID })
	}
	x.walks = make([][]paper.Path, n)
	x.walkMap = make([]map[int]*paper.Path, n)
	for source := 0; source < n; source++ {
		dist := map[int]int64{source: 0}
		prev := map[int]model.Footpath{}
		// Strictly positive validated edges make every final predecessor point
		// to a vertex settled earlier. Depth is final when a vertex settles.
		depth := map[int]int{source: 0}
		settled := []int{}
		h := &flashHeap{{source, 0}}
		for h.Len() > 0 {
			if e := w.tick(); e != nil {
				return e
			}
			u := heap.Pop(h).(flashHeapItem)
			if dist[u.v] != u.distance {
				continue
			}
			if x.snap.Stats.WalkSettled >= x.cfg.Limits.MaxWalkSettled {
				return fmt.Errorf("FLASH-TB incomplete: walking settlement budget exhausted")
			}
			x.snap.Stats.WalkSettled++
			settled = append(settled, u.v)
			for _, e := range adj[u.v] {
				if e2 := w.tick(); e2 != nil {
					return e2
				}
				v := x.ids[e.To]
				value := u.distance + e.Duration
				if limit != nil && value > *limit {
					continue
				}
				old, ok := dist[v]
				if !ok || value < old {
					dist[v] = value
					prev[v] = e
					depth[v] = depth[u.v] + 1
					heap.Push(h, flashHeapItem{v, value})
				}
			}
		}
		stops := []int{}
		for v := range dist {
			stops = append(stops, v)
		}
		sort.Ints(stops)
		// Pick one descendant leaf for each vertex. Reverse settlement order
		// visits children before their parents, without walking any raw word
		// ahead of its existing charged reconstruction. All metadata is local
		// to this source and never changes routing or logical work accounting.
		leaf := make(map[int]int, len(stops))
		for _, v := range stops {
			leaf[v] = v
		}
		for i := len(settled) - 1; i >= 0; i-- {
			v := settled[i]
			if v != source {
				leaf[x.ids[prev[v].From]] = leaf[v]
			}
		}
		words := map[int][]model.Footpath{}
		published := map[int]int{}
		for _, v := range stops {
			if e := w.tick(); e != nil {
				return e
			}
			if x.snap.Stats.ClosurePaths >= x.cfg.Limits.MaxClosurePaths {
				return fmt.Errorf("FLASH-TB incomplete: closure path budget exhausted")
			}
			x.snap.Stats.ClosurePaths++
			p := paper.Path{To: x.names[v], Duration: dist[v], Edges: []model.Footpath{}}
			owner := leaf[v]
			word := words[owner]
			for u := v; u != source; {
				if e := w.tick(); e != nil {
					return e
				}
				edge := prev[u]
				if word == nil {
					// Allocate only after the original first successful hop tick.
					// The unseen suffix may remain uninitialized until a later
					// target, but no published prefix is ever written again.
					word = make([]model.Footpath, depth[owner])
					words[owner] = word
				}
				position := depth[u] - 1
				if position >= published[owner] {
					word[position] = edge
				}
				u = x.ids[edge.From]
			}
			if length := depth[v]; length > 0 {
				published[owner] = max(published[owner], length)
				// Capacity limiting prevents append from touching another path's
				// suffix. Consumers must also keep visible elements immutable.
				p.Edges = word[:length:length]
			}
			x.walks[source] = append(x.walks[source], p)
			x.snap.ClosureDiameter = max(x.snap.ClosureDiameter, p.Duration)
		}
		x.walkMap[source] = map[int]*paper.Path{}
		for i := range x.walks[source] {
			p := &x.walks[source][i]
			x.walkMap[source][x.ids[p.To]] = p
		}
	}
	if x.snap.ClosureDiameter > model.MaxServiceSecond {
		return fmt.Errorf("FLASH-TB capability: complete closure exceeds service-time domain")
	}
	return nil
}

// flashAffinityHeap contains only unassigned vertices with positive affinity
// to the current cell. positions permits O(log V) updates without stale entries.
// Sorted stop indices provide exactly the original stop-ID tie break.
type flashAffinityHeap struct {
	vertices                    []int
	positions, affinity, degree []int
}

func (h flashAffinityHeap) Len() int { return len(h.vertices) }
func (h flashAffinityHeap) Less(i, j int) bool {
	a, b := h.vertices[i], h.vertices[j]
	if h.affinity[a] != h.affinity[b] {
		return h.affinity[a] > h.affinity[b]
	}
	if h.degree[a] != h.degree[b] {
		return h.degree[a] > h.degree[b]
	}
	return a < b
}
func (h flashAffinityHeap) Swap(i, j int) {
	h.vertices[i], h.vertices[j] = h.vertices[j], h.vertices[i]
	h.positions[h.vertices[i]] = i
	h.positions[h.vertices[j]] = j
}
func (h *flashAffinityHeap) Push(value any) {
	v := value.(int)
	h.positions[v] = len(h.vertices)
	h.vertices = append(h.vertices, v)
}
func (h *flashAffinityHeap) Pop() any {
	i := len(h.vertices) - 1
	v := h.vertices[i]
	h.vertices = h.vertices[:i]
	h.positions[v] = -1
	return v
}

// A deterministic balanced weighted graph-growth partitioner. This is an exact
// partition, not a shortest-path approximation; partition quality affects speed.
func (x *FlashIndex) partition(w *flashWork) error {
	n := len(x.names)
	adj := make([]map[int]int, n)
	for i := range adj {
		adj[i] = map[int]int{}
	}
	add := func(a, b int) {
		if a != b {
			adj[a][b]++
			adj[b][a]++
		}
	}
	for _, c := range x.base.p.Data.Connections {
		add(x.ids[c.From], x.ids[c.To])
	}
	for u, paths := range x.walks {
		for _, p := range paths {
			add(u, x.ids[p.To])
		}
	}
	p := FlashPartition{Method: "deterministic balanced weighted graph growth; max accumulated affinity, degree, stop ID", Cells: map[string]int{}, Sizes: make([]int, x.cfg.Cells)}
	degree := make([]int, n)
	for i := range adj {
		for _, v := range adj[i] {
			degree[i] += v
		}
		p.LayoutEdges += len(adj[i])
		p.TotalWeight += degree[i]
	}
	p.LayoutEdges /= 2
	p.TotalWeight /= 2
	assigned := make([]bool, n)
	// A fixed degree/ID order supplies the best zero-affinity seed whenever
	// the current cell has no frontier. Assigned entries are skipped once.
	seeds := make([]int, n)
	for v := range seeds {
		seeds[v] = v
	}
	sort.Slice(seeds, func(i, j int) bool {
		a, b := seeds[i], seeds[j]
		if degree[a] != degree[b] {
			return degree[a] > degree[b]
		}
		return a < b
	})
	seed := 0
	affinity, epoch, positions := make([]int, n), make([]int, n), make([]int, n)
	frontier := &flashAffinityHeap{positions: positions, affinity: affinity, degree: degree}
	remaining := n
	for cell := 0; cell < x.cfg.Cells; cell++ {
		capacity := (remaining + x.cfg.Cells - cell - 1) / (x.cfg.Cells - cell)
		// Reset only the frontier. Epochs avoid a V-element reset per cell.
		frontier.vertices = frontier.vertices[:0]
		for j := 0; j < capacity; j++ {
			if e := w.tick(); e != nil {
				return e
			}
			best := -1
			if frontier.Len() > 0 {
				best = heap.Pop(frontier).(int)
			} else {
				for assigned[seeds[seed]] {
					if e := w.tick(); e != nil {
						return e
					}
					seed++
				}
				best = seeds[seed]
				seed++
			}
			assigned[best] = true
			remaining--
			p.Cells[x.names[best]] = cell
			p.Sizes[cell]++
			for v, weight := range adj[best] {
				if e := w.tick(); e != nil {
					return e
				}
				if assigned[v] {
					continue
				}
				if epoch[v] != cell+1 {
					epoch[v] = cell + 1
					affinity[v] = weight
					heap.Push(frontier, v)
				} else {
					affinity[v] += weight
					heap.Fix(frontier, positions[v])
				}
			}
		}
	}
	for a := range adj {
		for b, weight := range adj[a] {
			if a < b && p.Cells[x.names[a]] != p.Cells[x.names[b]] {
				p.CutWeight += weight
			}
		}
	}
	x.snap.Partition = p
	return nil
}
