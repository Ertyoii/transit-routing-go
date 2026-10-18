package ultra

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"time"
)

type trace struct {
	previous           *trace
	kind               string
	ref                string
	arc                int
	departure, arrival int64
	relative           bool
}
type label struct {
	time  int64
	trips int
	path  *trace
}

func emptyLabels(n int) []label {
	a := make([]label, n)
	for i := range a {
		a[i].time = paper.Infinity
	}
	return a
}

type entry struct {
	v int
	t int64
}
type queue []entry

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	if q[i].t == q[j].t {
		return q[i].v < q[j].v
	}
	return q[i].t < q[j].t
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(v any)   { *q = append(*q, v.(entry)) }
func (q *queue) Pop() any     { a := *q; v := a[len(a)-1]; *q = a[:len(a)-1]; return v }

type work struct {
	ctx context.Context
	l   Limits
	s   *Stats
}

func (w *work) check() error { return w.ctx.Err() }
func (w *work) settle() error {
	if e := w.check(); e != nil {
		return e
	}
	if w.s.WalkSettled == w.l.MaxWalkSettled {
		return fmt.Errorf("ULTRA incomplete: walking settled budget exhausted")
	}
	w.s.WalkSettled++
	return nil
}
func (w *work) edge() error {
	if w.s.WalkEdges == w.l.MaxWalkEdges {
		return fmt.Errorf("ULTRA incomplete: walking edge budget exhausted")
	}
	w.s.WalkEdges++
	return w.check()
}
func (w *work) connection() error {
	if w.s.ConnectionVisits == w.l.MaxConnectionVisits {
		return fmt.Errorf("ULTRA incomplete: connection budget exhausted")
	}
	w.s.ConnectionVisits++
	return w.check()
}
func (w *work) leg() error {
	if w.s.OutputLegs == w.l.MaxOutputLegs {
		return fmt.Errorf("ULTRA incomplete: output budget exhausted")
	}
	w.s.OutputLegs++
	return w.check()
}

// Sorted route scanning precedes all walking relaxation. Strict arrival updates
// retain the earliest route/index representative and favor rides over tied walks.
func (x *Index) scan(prev, curr []label, maximum int64, destination int, w *work) error {
	if x.s.Config.RouteMode == NonOvertakingGroups {
		return x.scanGrouped(prev, curr, maximum, destination, w)
	}
	selected, e := x.collectRoutes(prev, maximum, destination, nil, w)
	if e != nil {
		return e
	}
	for _, selectedRoute := range selected {
		group := x.routes[selectedRoute.route]
		r := group.trips[0]
		if e := w.check(); e != nil {
			return e
		}
		w.s.RouteScans++
		boarded := false
		var parent *trace
		trips := 0
		for _, c := range r.cs[selectedRoute.position:] {
			if e := w.connection(); e != nil {
				return e
			}
			from, to := x.ids[c.From], x.ids[c.To]
			if !boarded && from != destination && prev[from].time <= c.Departure {
				boarded = true
				parent = prev[from].path
				trips = prev[from].trips + 1
			}
			if !boarded {
				continue
			}
			if c.Arrival > maximum {
				break
			}
			parent = &trace{previous: parent, kind: "ride", ref: c.ID, departure: c.Departure, arrival: c.Arrival}
			if c.Arrival < curr[to].time {
				curr[to] = label{c.Arrival, trips, parent}
			}
			if to == destination {
				break
			}
		}
	}
	return nil
}
func (x *Index) relaxCore(labels []label, maximum int64, relative bool, destination int, w *work) error {
	q := &queue{}
	for v, l := range labels {
		if l.time <= maximum {
			heap.Push(q, entry{v, l.time})
		}
	}
	for q.Len() > 0 {
		u := heap.Pop(q).(entry)
		if labels[u.v].time != u.t {
			continue
		}
		if e := w.settle(); e != nil {
			return e
		}
		if u.v == destination {
			continue
		}
		for _, a := range x.graph[u.v] {
			if e := w.edge(); e != nil {
				return e
			}
			nt := u.t + a.duration
			if nt > maximum || nt >= labels[a.to].time {
				continue
			}
			p := &trace{previous: labels[u.v].path, kind: "core", arc: a.id, departure: u.t, arrival: nt, relative: relative}
			labels[a.to] = label{nt, labels[u.v].trips, p}
			heap.Push(q, entry{a.to, nt})
		}
	}
	return nil
}
func (x *Index) materialize(p *trace, offset int64, w *work) (model.Journey, error) {
	nodes := []*trace{}
	for ; p != nil; p = p.previous {
		nodes = append(nodes, p)
	}
	j := model.Journey{Legs: []model.Leg{}}
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		if e := w.check(); e != nil {
			return j, e
		}
		start, end := n.departure, n.arrival
		if n.relative {
			start += offset
			end += offset
		}
		if n.kind == "ride" || n.kind == "walk" {
			if e := w.leg(); e != nil {
				return j, e
			}
			j.Legs = append(j.Legs, model.Leg{Kind: n.kind, Ref: n.ref, Departure: start, Arrival: end})
			continue
		}
		refs := []string{}
		if n.kind == "shortcut" {
			refs = x.s.Shortcuts[n.arc].RawEdges
		} else {
			stack := []int{n.arc}
			for len(stack) > 0 {
				if e := w.check(); e != nil {
					return j, e
				}
				id := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				a := x.core.Arcs[id]
				if a.Left >= 0 {
					if a.Left >= id || a.Right < 0 || a.Right >= id {
						return j, fmt.Errorf("invalid Core-CH child DAG")
					}
					stack = append(stack, a.Right, a.Left)
				} else {
					refs = append(refs, a.RawRef)
				}
			}
		}
		for _, id := range refs {
			if e := w.leg(); e != nil {
				return j, e
			}
			edge := x.raw[id]
			j.Legs = append(j.Legs, model.Leg{Kind: "walk", Ref: id, Departure: start, Arrival: start + edge.Duration})
			start += edge.Duration
		}
		if start != end {
			return j, fmt.Errorf("expanded transfer duration mismatch")
		}
	}
	return j, nil
}

type rawEntry struct {
	v string
	t int64
}
type rawQueue []rawEntry

func (q rawQueue) Len() int { return len(q) }
func (q rawQueue) Less(i, j int) bool {
	if q[i].t == q[j].t {
		return q[i].v < q[j].v
	}
	return q[i].t < q[j].t
}
func (q rawQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *rawQueue) Push(v any)   { *q = append(*q, v.(rawEntry)) }
func (q *rawQueue) Pop() any     { a := *q; v := a[len(a)-1]; *q = a[:len(a)-1]; return v }

type rawSearch struct {
	distance map[string]int64
	parent   map[string]model.Footpath
	source   string
	reverse  bool
}

func (x *Index) rawDijkstra(source string, maximum int64, reverse bool, w *work) (rawSearch, error) {
	r := rawSearch{map[string]int64{source: 0}, map[string]model.Footpath{}, source, reverse}
	q := &rawQueue{{source, 0}}
	for q.Len() > 0 {
		u := heap.Pop(q).(rawEntry)
		if r.distance[u.v] != u.t {
			continue
		}
		if e := w.settle(); e != nil {
			return r, e
		}
		edges := x.rawOut[u.v]
		if reverse {
			edges = x.rawIn[u.v]
		}
		for _, edge := range edges {
			if e := w.edge(); e != nil {
				return r, e
			}
			to := edge.To
			if reverse {
				to = edge.From
			}
			nt := u.t + edge.Duration
			if nt > maximum {
				continue
			}
			if old, ok := r.distance[to]; ok && old <= nt {
				continue
			}
			r.distance[to] = nt
			r.parent[to] = edge
			heap.Push(q, rawEntry{to, nt})
		}
	}
	return r, nil
}
func (r rawSearch) path(endpoint string) []model.Footpath {
	edges := []model.Footpath{}
	for at := endpoint; at != r.source; {
		e := r.parent[at]
		edges = append(edges, e)
		if r.reverse {
			at = e.To
		} else {
			at = e.From
		}
	}
	if !r.reverse {
		for i, j := 0, len(edges)-1; i < j; i, j = i+1, j-1 {
			edges[i], edges[j] = edges[j], edges[i]
		}
	}
	return edges
}
func appendWalk(parent *trace, edges []model.Footpath, start int64) *trace {
	for _, e := range edges {
		parent = &trace{previous: parent, kind: "walk", ref: e.ID, departure: start, arrival: start + e.Duration}
		start += e.Duration
	}
	return parent
}

type Result struct {
	Journeys     []model.Journey `json:"journeys"`
	Complete     bool            `json:"complete"`
	Error        string          `json:"error,omitempty"`
	Stats        Stats           `json:"stats"`
	AccessNS     int64           `json:"access_search_ns"`
	EgressNS     int64           `json:"egress_search_ns"`
	CapabilityNS int64           `json:"capability_check_ns"`
}

func (x *Index) Search(ctx context.Context, q model.Query, l Limits) (out Result, err error) {
	out.Journeys = []model.Journey{}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if !x.s.Complete {
		err = fmt.Errorf("ULTRA query requires complete preprocessing")
		return
	}
	if !validLimits(l) {
		err = fmt.Errorf("ULTRA requires explicit positive query limits")
		return
	}
	t := time.Now()
	if q.TransferBuffer != 0 || q.AccessibleOnly {
		err = fmt.Errorf("ULTRA capability: nonzero transfer buffers and accessibility-filtered queries require separate preprocessing")
		return
	}
	if err = x.p.CheckContext(ctx, q); err != nil {
		return
	}
	horizon := q.LatestArrival - q.Departure
	if q.MaxBoardings > 1 && q.Walking.Transfer < horizon {
		err = fmt.Errorf("ULTRA capability: intermediate-walk cap may bind; require at least query horizon %d", horizon)
		return
	}
	if q.Origin == q.Destination {
		out.Journeys = append(out.Journeys, model.Journey{Legs: []model.Leg{}})
		return
	}
	out.CapabilityNS = time.Since(t).Nanoseconds()
	w := work{ctx, l, &out.Stats}
	t = time.Now()
	var forward, backward rawSearch
	forward, err = x.rawDijkstra(q.Origin, horizon, false, &w)
	out.AccessNS = time.Since(t).Nanoseconds()
	if err != nil {
		return
	}
	t = time.Now()
	backward, err = x.rawDijkstra(q.Destination, horizon, true, &w)
	out.EgressNS = time.Since(t).Nanoseconds()
	if err != nil {
		return
	}
	t = time.Now()
	for _, s := range x.names {
		if !x.transit[s] {
			continue
		}
		if d, ok := forward.distance[s]; ok && d > q.Walking.Access {
			err = fmt.Errorf("ULTRA capability: access cap %d may bind at %q (distance %d)", q.Walking.Access, s, d)
			return
		}
		if d, ok := backward.distance[s]; ok && d > q.Walking.Egress {
			err = fmt.Errorf("ULTRA capability: egress cap %d may bind at %q (distance %d)", q.Walking.Egress, s, d)
			return
		}
	}
	if d, ok := forward.distance[q.Destination]; ok && d > q.Walking.Direct {
		err = fmt.Errorf("ULTRA capability: direct walking cap may bind")
		return
	}
	out.CapabilityNS += time.Since(t).Nanoseconds()
	previous := emptyLabels(len(x.names))
	best := paper.Infinity
	for s := range x.transit {
		d, ok := forward.distance[s]
		if !ok || s == q.Destination {
			continue
		}
		edges := forward.path(s)
		passes := false
		for _, e := range edges {
			if e.To == q.Destination {
				passes = true
				break
			}
		}
		if passes {
			continue
		}
		previous[x.ids[s]] = label{q.Departure + d, 0, appendWalk(nil, edges, q.Departure)}
	}
	if d, ok := forward.distance[q.Destination]; ok {
		best = q.Departure + d
		var j model.Journey
		j, err = x.materialize(appendWalk(nil, forward.path(q.Destination), q.Departure), q.Departure, &w)
		if err != nil {
			return
		}
		out.Journeys = append(out.Journeys, j)
	}
	destination := -1
	if id, ok := x.ids[q.Destination]; ok {
		destination = id
	}
	for round := 1; round <= q.MaxBoardings; round++ {
		if err = w.check(); err != nil {
			return
		}
		curr := append([]label{}, previous...)
		if err = x.scan(previous, curr, q.LatestArrival, destination, &w); err != nil {
			return
		}
		byRoute := append([]label{}, curr...)
		targetTime := best
		var targetParent *trace
		for u, lab := range byRoute {
			if lab.trips != round || lab.path == nil || lab.path.kind != "ride" {
				continue
			}
			name := x.names[u]
			if d, ok := backward.distance[name]; ok && lab.time+d <= q.LatestArrival && lab.time+d < targetTime {
				targetTime = lab.time + d
				targetParent = appendWalk(lab.path, backward.path(name), lab.time)
			}
			if u == destination {
				continue
			}
			for _, id := range x.outgoing[name] {
				if err = w.edge(); err != nil {
					return
				}
				s := x.s.Shortcuts[id]
				to := x.ids[s.To]
				at := lab.time + s.Duration
				if at > q.LatestArrival || at >= curr[to].time {
					continue
				}
				passes := false
				for _, ref := range s.RawEdges {
					if x.raw[ref].To == q.Destination && s.To != q.Destination {
						passes = true
						break
					}
				}
				if passes {
					continue
				}
				curr[to] = label{at, lab.trips, &trace{previous: lab.path, kind: "shortcut", arc: id, departure: lab.time, arrival: at}}
			}
		}
		if targetTime < best {
			var j model.Journey
			j, err = x.materialize(targetParent, q.Departure, &w)
			if err != nil {
				return
			}
			best = targetTime
			out.Journeys = append(out.Journeys, j)
		}
		previous = curr
	}
	return
}
