// Package graph implements the immutable local directed walking provider.
package graph

import (
	"container/heap"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/walking"
	"reflect"
	"sort"
)

type Node struct {
	ID         string                `json:"id"`
	Coordinate *walking.Coordinate   `json:"coordinate,omitempty"`
	Accessible walking.Accessibility `json:"accessible"`
}
type Data struct {
	Descriptor walking.Descriptor `json:"descriptor"`
	Nodes      []Node             `json:"nodes"`
	Edges      []walking.Segment  `json:"edges"`
}
type Limits struct {
	MaxSettled        int
	MaxEdgeVisits     int
	MaxMatrixCells    int
	MaxResultSegments int // total across an entire route/reach/batch/matrix response
}

func DefaultLimits() Limits { return Limits{2000000, 10000000, 1000000, 2000000} }

type Graph struct {
	descriptor walking.Descriptor
	nodes      map[string]Node
	edges      map[string]walking.Segment
	out        map[string][]walking.Segment
	limits     Limits
}

func New(d Data, limits Limits) (*Graph, error) {
	if limits.MaxSettled <= 0 || limits.MaxEdgeVisits <= 0 || limits.MaxMatrixCells <= 0 || limits.MaxResultSegments <= 0 {
		return nil, fmt.Errorf("%w: limits", walking.ErrInvalid)
	}
	if d.Descriptor.Schema != "" && d.Descriptor.Schema != walking.SchemaVersion {
		return nil, fmt.Errorf("%w: schema", walking.ErrUnsupported)
	}
	if d.Descriptor.ProviderID == "" || d.Descriptor.GraphID == "" || d.Descriptor.Version == "" || d.Descriptor.CostModel == "" || !d.Descriptor.RoleIndependent {
		return nil, fmt.Errorf("%w: descriptor", walking.ErrInvalid)
	}
	// Deep-copy ownership before sorting or indexing caller input.
	b, e := json.Marshal(d)
	if e != nil {
		return nil, e
	}
	var owned Data
	if e = json.Unmarshal(b, &owned); e != nil {
		return nil, e
	}
	d = owned
	expected := d.Descriptor.SnapshotID
	d.Descriptor.SnapshotID = ""
	d.Descriptor.Schema = walking.SchemaVersion
	sort.Strings(d.Descriptor.Sources)
	sort.Slice(d.Nodes, func(i, j int) bool { return d.Nodes[i].ID < d.Nodes[j].ID })
	sort.Slice(d.Edges, func(i, j int) bool { return d.Edges[i].ID < d.Edges[j].ID })
	g := &Graph{nodes: map[string]Node{}, edges: map[string]walking.Segment{}, out: map[string][]walking.Segment{}, limits: limits}
	for _, n := range d.Nodes {
		if n.ID == "" || !n.Accessible.Valid() || n.Coordinate != nil && !n.Coordinate.Valid() {
			return nil, fmt.Errorf("%w: node", walking.ErrInvalid)
		}
		if _, ok := g.nodes[n.ID]; ok {
			return nil, fmt.Errorf("%w: duplicate node", walking.ErrInvalid)
		}
		g.nodes[n.ID] = n
	}
	if len(g.nodes) == 0 {
		return nil, fmt.Errorf("%w: no nodes", walking.ErrInvalid)
	}
	for _, s := range d.Edges {
		_, a := g.nodes[s.From]
		_, z := g.nodes[s.To]
		_, duplicate := g.edges[s.ID]
		if s.ID == "" || !a || !z || duplicate || s.Seconds <= 0 || s.Seconds > walking.MaxSeconds || !s.Accessible.Valid() || s.Witness.Source == "" || s.Witness.Revision == "" || len(s.Witness.EdgeIDs) == 0 {
			return nil, fmt.Errorf("%w: edge %q", walking.ErrInvalid, s.ID)
		}
		for _, id := range s.Witness.EdgeIDs {
			if id == "" {
				return nil, fmt.Errorf("%w: witness ID", walking.ErrInvalid)
			}
		}
		g.edges[s.ID] = s
		g.out[s.From] = append(g.out[s.From], s)
	}
	b, _ = json.Marshal(d)
	h := sha256.Sum256(b)
	d.Descriptor.SnapshotID = hex.EncodeToString(h[:])
	if expected != "" && expected != d.Descriptor.SnapshotID {
		return nil, walking.ErrStale
	}
	g.descriptor = d.Descriptor
	return g, nil
}
func (g *Graph) Descriptor() walking.Descriptor {
	d := g.descriptor
	d.Sources = append([]string{}, d.Sources...)
	return d
}
func (g *Graph) endpoint(e walking.Endpoint) error {
	n, ok := g.nodes[e.NodeID]
	if !ok {
		return fmt.Errorf("%w: unknown node %q", walking.ErrInvalid, e.NodeID)
	}
	if e.Coordinate != nil {
		if !e.Coordinate.Valid() {
			return fmt.Errorf("%w: coordinate", walking.ErrInvalid)
		}
		if n.Coordinate == nil {
			return fmt.Errorf("%w: graph has no coordinate for node", walking.ErrUnsupported)
		}
		if *n.Coordinate != *e.Coordinate {
			return fmt.Errorf("%w: coordinate/node mismatch", walking.ErrInvalid)
		}
	}
	return nil
}
func (g *Graph) check(ctx context.Context, snapshot string, from walking.Endpoint, o walking.Options) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if snapshot != g.descriptor.SnapshotID {
		return walking.ErrStale
	}
	if e := o.Validate(); e != nil {
		return e
	}
	if e := g.endpoint(from); e != nil {
		return e
	}
	if o.StopAt != "" {
		if _, ok := g.nodes[o.StopAt]; !ok {
			return fmt.Errorf("%w: absorbing node", walking.ErrInvalid)
		}
	}
	return nil
}

type entry struct {
	node    string
	seconds int64
}
type queue []entry

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	if q[i].seconds == q[j].seconds {
		return q[i].node < q[j].node
	}
	return q[i].seconds < q[j].seconds
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(x any)   { *q = append(*q, x.(entry)) }
func (q *queue) Pop() any     { a := *q; v := a[len(a)-1]; *q = a[:len(a)-1]; return v }

type search struct {
	dist map[string]int64
	prev map[string]walking.Segment
}

func (g *Graph) search(ctx context.Context, from string, o walking.Options) (search, error) {
	s := search{map[string]int64{from: 0}, map[string]walking.Segment{}}
	h := &queue{{from, 0}}
	settled, visits := 0, 0
	for h.Len() > 0 {
		if e := ctx.Err(); e != nil {
			return search{}, e
		}
		u := heap.Pop(h).(entry)
		if u.seconds != s.dist[u.node] {
			continue
		}
		settled++
		if settled > g.limits.MaxSettled {
			return search{}, walking.ErrBudget
		}
		if u.node == o.StopAt || o.AccessibleOnly && g.nodes[u.node].Accessible != walking.Yes {
			continue
		}
		for _, edge := range g.out[u.node] {
			if e := ctx.Err(); e != nil {
				return search{}, e
			}
			visits++
			if visits > g.limits.MaxEdgeVisits {
				return search{}, walking.ErrBudget
			}
			if o.AccessibleOnly && (edge.Accessible != walking.Yes || g.nodes[edge.To].Accessible != walking.Yes) {
				continue
			}
			n := u.seconds + edge.Seconds
			if n > o.MaxSeconds {
				continue
			}
			old, ok := s.dist[edge.To]
			if !ok || n < old {
				s.dist[edge.To] = n
				s.prev[edge.To] = edge
				heap.Push(h, entry{edge.To, n})
			}
		}
	}
	if e := ctx.Err(); e != nil {
		return search{}, e
	}
	return s, nil
}
func (g *Graph) result(ctx context.Context, from, to string, s search, remaining *int) (walking.Result, error) {
	r := walking.Result{SnapshotID: g.descriptor.SnapshotID, From: from, To: to, Status: walking.Unreachable}
	seconds, ok := s.dist[to]
	if !ok {
		return r, nil
	}
	p := walking.Path{SnapshotID: r.SnapshotID, From: from, To: to, Seconds: seconds, Segments: []walking.Segment{}}
	for at := to; at != from; {
		if e := ctx.Err(); e != nil {
			return walking.Result{}, e
		}
		if *remaining <= 0 {
			return walking.Result{}, walking.ErrBudget
		}
		*remaining--
		edge := s.prev[at]
		p.Segments = append(p.Segments, edge)
		at = edge.From
	}
	for i, j := 0, len(p.Segments)-1; i < j; i, j = i+1, j-1 {
		p.Segments[i], p.Segments[j] = p.Segments[j], p.Segments[i]
	}
	r.Status = walking.Reachable
	r.Path = &p
	return walking.CloneResult(r), nil
}
func (g *Graph) Route(ctx context.Context, r walking.Request) (walking.Result, error) {
	if e := g.check(ctx, r.SnapshotID, r.From, r.Options); e != nil {
		return walking.Result{}, e
	}
	if e := g.endpoint(r.To); e != nil {
		return walking.Result{}, e
	}
	s, e := g.search(ctx, r.From.NodeID, r.Options)
	if e != nil {
		return walking.Result{}, e
	}
	remaining := g.limits.MaxResultSegments
	out, e := g.result(ctx, r.From.NodeID, r.To.NodeID, s, &remaining)
	if e != nil {
		return walking.Result{}, e
	}
	if e = ctx.Err(); e != nil {
		return walking.Result{}, e
	}
	return out, nil
}
func (g *Graph) Duration(ctx context.Context, r walking.Request) (walking.DurationResult, error) {
	v, e := g.Route(ctx, r)
	if e != nil {
		return walking.DurationResult{}, e
	}
	d := walking.DurationResult{SnapshotID: v.SnapshotID, From: v.From, To: v.To, Status: v.Status}
	if v.Path != nil {
		n := v.Path.Seconds
		d.Seconds = &n
	}
	return d, nil
}

// TargetSet is a prepared immutable reconstruction filter bound to graph contents.
// Search still traverses intermediate nodes. It does not change reachability.
type TargetSet struct {
	snapshot string
	ids      map[string]bool
}

func (g *Graph) PrepareTargets(ctx context.Context, endpoints []walking.Endpoint) (*TargetSet, error) {
	if len(endpoints) > g.limits.MaxMatrixCells {
		return nil, walking.ErrBudget
	}
	out := &TargetSet{snapshot: g.descriptor.SnapshotID, ids: map[string]bool{}}
	for _, v := range endpoints {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if e := g.endpoint(v); e != nil {
			return nil, e
		}
		out.ids[v.NodeID] = true
	}
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return out, nil
}
func (g *Graph) Reach(ctx context.Context, r walking.ReachRequest) (walking.ReachResult, error) {
	return g.ReachWithTargets(ctx, r, nil)
}

// ReachWithTargets reconstructs the union of a prepared target set and request
// targets. A nil set follows ordinary Reach semantics. A nonnil empty set selects
// no nodes except explicit request targets; no hidden endpoint is added.
func (g *Graph) ReachWithTargets(ctx context.Context, r walking.ReachRequest, selected *TargetSet) (walking.ReachResult, error) {
	if e := ctx.Err(); e != nil {
		return walking.ReachResult{}, e
	}
	if selected != nil && selected.snapshot != g.descriptor.SnapshotID {
		return walking.ReachResult{}, walking.ErrStale
	}

	targets := map[string]bool{}
	if len(r.Targets) > g.limits.MaxMatrixCells {
		return walking.ReachResult{}, walking.ErrBudget
	}
	for _, target := range r.Targets {
		if e := ctx.Err(); e != nil {
			return walking.ReachResult{}, e
		}
		if e := g.endpoint(target); e != nil {
			return walking.ReachResult{}, e
		}
		targets[target.NodeID] = true
	}

	if e := g.check(ctx, r.SnapshotID, r.From, r.Options); e != nil {
		return walking.ReachResult{}, e
	}
	s, e := g.search(ctx, r.From.NodeID, r.Options)
	if e != nil {
		return walking.ReachResult{}, e
	}
	keys := make([]string, 0, len(s.dist))
	for k := range s.dist {
		if (selected != nil || len(targets) > 0) && !targets[k] && (selected == nil || !selected.ids[k]) {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := walking.ReachResult{SnapshotID: g.descriptor.SnapshotID, Paths: []walking.Path{}}
	remaining := g.limits.MaxResultSegments
	for _, k := range keys {
		if e := ctx.Err(); e != nil {
			return walking.ReachResult{}, e
		}
		value, e := g.result(ctx, r.From.NodeID, k, s, &remaining)
		if e != nil {
			return walking.ReachResult{}, e
		}
		out.Paths = append(out.Paths, *value.Path)
	}
	if e := ctx.Err(); e != nil {
		return walking.ReachResult{}, e
	}
	return out, nil
}
func (g *Graph) Batch(ctx context.Context, rs []walking.Request) ([]walking.Result, error) {
	if len(rs) > g.limits.MaxMatrixCells {
		return nil, walking.ErrBudget
	}
	for _, r := range rs {
		if e := g.check(ctx, r.SnapshotID, r.From, r.Options); e != nil {
			return nil, e
		}
		if e := g.endpoint(r.To); e != nil {
			return nil, e
		}
	}
	type key struct {
		from    string
		options walking.Options
	}
	groups := map[key][]int{}
	order := []key{}
	for i, r := range rs {
		k := key{r.From.NodeID, r.Options}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], i)
	}
	out := make([]walking.Result, len(rs))
	remaining := g.limits.MaxResultSegments
	// Hold only one origin search at a time, while sharing it across its ordered targets.
	for _, k := range order {
		s, e := g.search(ctx, k.from, k.options)
		if e != nil {
			return nil, e
		}
		for _, i := range groups[k] {
			value, e := g.result(ctx, rs[i].From.NodeID, rs[i].To.NodeID, s, &remaining)
			if e != nil {
				return nil, e
			}
			out[i] = value
		}
	}

	if e := ctx.Err(); e != nil {
		return nil, e
	}
	return out, nil
}
func (g *Graph) Matrix(ctx context.Context, r walking.MatrixRequest) (walking.MatrixResult, error) {
	if e := ctx.Err(); e != nil {
		return walking.MatrixResult{}, e
	}
	if len(r.Sources) == 0 || len(r.Targets) == 0 {
		return walking.MatrixResult{}, fmt.Errorf("%w: empty matrix axis", walking.ErrInvalid)
	}
	if len(r.Sources) > g.limits.MaxMatrixCells/len(r.Targets) {
		return walking.MatrixResult{}, walking.ErrBudget
	}
	rs := make([]walking.Request, 0, len(r.Sources)*len(r.Targets))
	for _, s := range r.Sources {
		for _, t := range r.Targets {
			rs = append(rs, walking.Request{SnapshotID: r.SnapshotID, From: s, To: t, Options: r.Options})
		}
	}
	values, e := g.Batch(ctx, rs)
	if e != nil {
		return walking.MatrixResult{}, e
	}
	out := walking.MatrixResult{SnapshotID: g.descriptor.SnapshotID, Rows: make([][]walking.Result, len(r.Sources))}
	for i := range out.Rows {
		out.Rows[i] = values[i*len(r.Targets) : (i+1)*len(r.Targets)]
	}
	return out, nil
}

// ValidatePath checks the entire witness against this immutable source graph, separately from search.
func (g *Graph) ValidatePath(r walking.Request, v walking.Result) error {
	if e := g.check(context.Background(), r.SnapshotID, r.From, r.Options); e != nil {
		return e
	}
	if e := g.endpoint(r.To); e != nil {
		return e
	}
	if e := walking.CheckResult(r, v); e != nil {
		return e
	}
	if v.Status == walking.Unreachable {
		return fmt.Errorf("%w: witness checker cannot prove no path", walking.ErrUnsupported)
	}
	for _, edge := range v.Path.Segments {
		source, ok := g.edges[edge.ID]
		if !ok || !reflect.DeepEqual(source, edge) {
			return fmt.Errorf("%w: source witness", walking.ErrInvalid)
		}
		if r.Options.AccessibleOnly && (g.nodes[edge.From].Accessible != walking.Yes || g.nodes[edge.To].Accessible != walking.Yes) {
			return fmt.Errorf("%w: node accessibility", walking.ErrInvalid)
		}
	}
	return nil
}
