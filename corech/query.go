package corech

import (
	"container/heap"
	"context"
	"fmt"
)

type QueryLimits struct {
	MaxSettled       int `json:"max_settled"`
	MaxEdges         int `json:"max_edge_visits"`
	MaxUnpackedEdges int `json:"max_unpacked_edges"`
}
type QueryStats struct {
	Settled       int `json:"settled_vertices"`
	Edges         int `json:"edge_visits"`
	UnpackedEdges int `json:"unpacked_edges"`
}
type Path struct {
	Target   string   `json:"target"`
	Found    bool     `json:"found"`
	Duration int64    `json:"duration"`
	CoreArcs []int    `json:"core_arcs"`
	RawEdges []string `json:"raw_edges"`
}
type Row struct {
	Source          string     `json:"source"`
	MaximumDuration int64      `json:"maximum_duration"`
	Paths           []Path     `json:"paths"`
	Complete        bool       `json:"complete"`
	Error           string     `json:"error,omitempty"`
	Stats           QueryStats `json:"stats"`
}

// SearchMany is one actual shared Dijkstra row on the retained core, including
// full raw-edge unpacking. Latency belongs to the whole row, not individual cells.
func (x *Index) SearchMany(ctx context.Context, source string, targets []string, maximumDuration int64, limits QueryLimits) (out Row, err error) {
	out = Row{Source: source, MaximumDuration: maximumDuration, Paths: []Path{}}
	for _, t := range targets {
		out.Paths = append(out.Paths, Path{Target: t, CoreArcs: []int{}, RawEdges: []string{}})
	}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if !x.s.Complete {
		err = fmt.Errorf("Core-CH query requires a complete preparation")
		return
	}
	if maximumDuration < 0 || maximumDuration >= Infinity || limits.MaxSettled <= 0 || limits.MaxEdges <= 0 || limits.MaxUnpackedEdges <= 0 {
		err = fmt.Errorf("Core-CH query requires finite nonnegative duration and positive work/output limits")
		return
	}
	s, ok := x.ids[source]
	if !ok || !x.s.Nodes[s].Core {
		err = fmt.Errorf("unsupported source: retained core vertex required")
		return
	}
	targetIDs := map[int]int{}
	for i, t := range targets {
		v, ok := x.ids[t]
		if !ok || !x.s.Nodes[v].Core {
			err = fmt.Errorf("unsupported target: retained core vertex required: %q", t)
			return
		}
		if _, ok := targetIDs[v]; ok {
			err = fmt.Errorf("duplicate target %q", t)
			return
		}
		targetIDs[v] = i
	}
	if err = ctx.Err(); err != nil {
		return
	}
	remaining := len(targets)
	dist := map[int]int64{s: 0}
	parent := map[int]int{}
	settledTargets := map[int]bool{}
	h := &queue{{v: s}}
	for h.Len() > 0 && remaining > 0 {
		if err = ctx.Err(); err != nil {
			return
		}
		u := heap.Pop(h).(item)
		if dist[u.v] != u.distance {
			continue
		}
		if u.distance > maximumDuration {
			break
		}
		if out.Stats.Settled == limits.MaxSettled {
			err = fmt.Errorf("Core-CH query incomplete: settled budget exhausted")
			return
		}
		out.Stats.Settled++
		if _, ok := targetIDs[u.v]; ok {
			settledTargets[u.v] = true
			remaining--
			if remaining == 0 {
				break
			}
		}
		for _, id := range x.out[u.v] {
			if out.Stats.Edges == limits.MaxEdges {
				err = fmt.Errorf("Core-CH query incomplete: edge budget exhausted")
				return
			}
			out.Stats.Edges++
			a := x.s.Arcs[id]
			next := u.distance + a.Duration
			if next > maximumDuration {
				continue
			}
			if old, ok := dist[a.To]; !ok || next < old {
				dist[a.To] = next
				parent[a.To] = id
				heap.Push(h, item{v: a.To, distance: next})
			}
		}
	}
	for i, target := range targets {
		v := x.ids[target]
		if !settledTargets[v] {
			continue
		}
		p := &out.Paths[i]
		p.Found = true
		p.Duration = dist[v]
		reversed := []int{}
		for u := v; u != s; {
			id, ok := parent[u]
			if !ok {
				err = fmt.Errorf("broken core parent chain")
				return
			}
			reversed = append(reversed, id)
			u = x.s.Arcs[id].From
		}
		for j := len(reversed) - 1; j >= 0; j-- {
			p.CoreArcs = append(p.CoreArcs, reversed[j])
		}
		// Iterative expansion avoids stack depth depending on contraction order.
		stack := append([]int{}, reversed...)
		sum := int64(0)
		at := s
		for len(stack) > 0 {
			if err = ctx.Err(); err != nil {
				return
			}
			id := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			a := x.s.Arcs[id]
			if a.Left >= 0 {
				if a.Left >= id || a.Right < 0 || a.Right >= id {
					err = fmt.Errorf("invalid shortcut child ordering")
					return
				}
				stack = append(stack, a.Right, a.Left)
				continue
			}
			if a.RawRef == "" || a.From != at {
				err = fmt.Errorf("broken raw edge witness")
				return
			}
			if out.Stats.UnpackedEdges == limits.MaxUnpackedEdges {
				err = fmt.Errorf("Core-CH query incomplete: unpacking budget exhausted")
				return
			}
			out.Stats.UnpackedEdges++
			p.RawEdges = append(p.RawEdges, a.RawRef)
			sum += a.Duration
			at = a.To
		}
		if at != v || sum != p.Duration {
			err = fmt.Errorf("unpacked witness duration or endpoint mismatch")
			return
		}
	}
	return
}
