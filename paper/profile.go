// Package paper provides the declared input transformation shared by the paper
// kernels. It contains no timetable search. Oracle and validation do not import it.
package paper

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/transfer"
	"github.com/ertyoii/transit-routing-go/walking"
	"github.com/ertyoii/transit-routing-go/walking/graph"
	"github.com/ertyoii/transit-routing-go/walking/transit"
	"sort"
)

const Infinity int64 = 1 << 62

type Node struct {
	Previous *Node
	Leg      model.Leg
}

func Ride(parent *Node, c model.Connection) *Node {
	return &Node{parent, model.Leg{Kind: "ride", Ref: c.ID, Departure: c.Departure, Arrival: c.Arrival}}
}
func Journey(n *Node) model.Journey {
	legs := []model.Leg{}
	for ; n != nil; n = n.Previous {
		legs = append(legs, n.Leg)
	}
	for i, j := 0, len(legs)-1; i < j; i, j = i+1, j-1 {
		legs[i], legs[j] = legs[j], legs[i]
	}
	return model.Journey{Legs: legs}
}

type Label struct {
	Time   int64
	Parent *Node
}
type Result struct {
	Journeys []model.Journey `json:"journeys"`
	Complete bool            `json:"complete"`
	Error    string          `json:"error,omitempty"`
	Stats    Stats           `json:"stats"`
}
type Stats struct {
	Connections  int `json:"connection_visits"`
	Routes       int `json:"route_scans"`
	Rounds       int `json:"rounds"`
	WalkClosures int `json:"walk_closures"`
}
type Capability struct {
	Profile                   string `json:"profile"`
	MaximumTripSegments       int    `json:"maximum_trip_segments"`
	MaximumSimpleWalkingEdges int    `json:"maximum_simple_walking_edges"`
}
type Profile struct {
	walkingTargets       *graph.TargetSet
	walkingGraph         *graph.Graph
	walkingEdges         map[string]model.Footpath
	transitStops         map[string]bool
	minimumWalkDuration  int64
	minimumEventDuration int64
	Data                 model.Dataset
	Stops                map[string]model.Stop
	Trips                map[string]model.Trip
	Outgoing             map[string][]model.Footpath
	Rules                *transfer.Resolver
	Capability           Capability
}

func New(d model.Dataset) (*Profile, error) {
	if e := d.Validate(); e != nil {
		return nil, e
	}
	d = d.Clone()
	p := &Profile{transitStops: map[string]bool{}, minimumWalkDuration: Infinity, minimumEventDuration: Infinity, Data: d, Stops: map[string]model.Stop{}, Trips: map[string]model.Trip{}, Outgoing: map[string][]model.Footpath{}, Rules: transfer.New(d), Capability: Capability{Profile: "positive_static_stop_pair_v1"}}
	for _, s := range d.Stops {
		p.Stops[s.ID] = s
	}
	for _, t := range d.Trips {
		p.Trips[t.ID] = t
	}
	for _, r := range d.Transfers {
		if r.FromTrip != "" || r.ToTrip != "" || r.FromRoute != "" || r.ToRoute != "" {
			return nil, fmt.Errorf("paper capability: trip/route-specific transfer %q requires state expansion", r.ID)
		}
	}
	counts := map[string]int{}
	for _, c := range d.Connections {
		if c.Arrival <= c.Departure || c.From == c.To {
			return nil, fmt.Errorf("paper capability: positive non-self connection required: %q", c.ID)
		}
		p.minimumEventDuration = min(p.minimumEventDuration, c.Arrival-c.Departure)
		p.transitStops[c.From] = true
		p.transitStops[c.To] = true
		counts[c.TripID]++
		p.Capability.MaximumTripSegments = max(p.Capability.MaximumTripSegments, counts[c.TripID])
	}
	// Every shortest positive walk is simple. An undirected component size is a
	// conservative bound even for directed, inaccessible, or capped walking.
	neighbors := map[string][]string{}
	for _, w := range d.Footpaths {
		p.minimumWalkDuration = min(p.minimumWalkDuration, w.Duration)
		p.minimumEventDuration = min(p.minimumEventDuration, w.Duration)
		p.Outgoing[w.From] = append(p.Outgoing[w.From], w)
		neighbors[w.From] = append(neighbors[w.From], w.To)
		neighbors[w.To] = append(neighbors[w.To], w.From)
	}
	for s := range p.Outgoing {
		sort.Slice(p.Outgoing[s], func(i, j int) bool { return p.Outgoing[s][i].ID < p.Outgoing[s][j].ID })
	}
	seen := map[string]bool{}
	for s := range neighbors {
		if seen[s] {
			continue
		}
		stack := []string{s}
		seen[s] = true
		n := 0
		for len(stack) > 0 {
			u := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			n++
			for _, v := range neighbors[u] {
				if !seen[v] {
					seen[v] = true
					stack = append(stack, v)
				}
			}
		}
		p.Capability.MaximumSimpleWalkingEdges = max(p.Capability.MaximumSimpleWalkingEdges, n-1)
	}
	// Ambiguous stop-pair rules are rejected before any query, including station
	// parent matches. Only endpoint pairs reachable by walking (or staying) matter.
	for _, r := range d.Transfers {
		for _, s := range d.Stops {
			if s.ID == r.FromStop || s.ParentStation == r.FromStop {
				for _, t := range d.Stops {
					if t.ID == r.ToStop || t.ParentStation == r.ToStop {
						if _, e := p.Rules.Resolve(model.Connection{To: s.ID}, model.Connection{From: t.ID}, 0); e != nil {
							return nil, e
						}
					}
				}
			}
		}
	}

	var err error
	p.walkingGraph, err = transit.FromDataset(d, graph.DefaultLimits())
	if err != nil {
		return nil, err
	}
	targetEndpoints := make([]walking.Endpoint, 0, len(p.transitStops))
	for id := range p.transitStops {
		targetEndpoints = append(targetEndpoints, walking.Endpoint{NodeID: id})
	}
	p.walkingTargets, err = p.walkingGraph.PrepareTargets(context.Background(), targetEndpoints)
	if err != nil {
		return nil, err
	}
	p.walkingEdges = make(map[string]model.Footpath, len(d.Footpaths))
	for _, e := range d.Footpaths {
		p.walkingEdges[e.ID] = e
	}
	return p, nil
}
func (p *Profile) Check(q model.Query) error { return p.CheckContext(context.Background(), q) }
func (p *Profile) CheckContext(ctx context.Context, q model.Query) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if e := q.Validate(p.Data); e != nil {
		return e
	}
	if q.Walking == nil {
		return fmt.Errorf("paper capability: explicit walking role limits required")
	}
	if q.Origin == q.Destination {
		return nil
	}
	w := q.Walking
	bound := w.Direct
	if q.MaxBoardings > 0 {
		bound = max(bound, w.Access+w.Egress+int64(q.MaxBoardings-1)*w.Transfer)
	}
	if q.MaxWalkSeconds < bound {
		return fmt.Errorf("paper capability: total walking %d may bind; require >=%d", q.MaxWalkSeconds, bound)
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	proof, e := p.CertifyLegBound(ctx, q, DefaultProofLimits())
	if e != nil {
		return e
	}
	if q.MaxLegs < proof.SufficientLegs {
		return fmt.Errorf("paper capability: leg budget %d may bind; certified sufficient bound %d (%s)", q.MaxLegs, proof.SufficientLegs, proof.Method)
	}
	return nil
}
func (p *Profile) Accessible(q model.Query, s string) bool {
	return !q.AccessibleOnly || p.Stops[s].Accessible == model.Yes
}
func (p *Profile) TripAllowed(q model.Query, t string) bool {
	return !q.AccessibleOnly || p.Trips[t].Accessible == model.Yes
}

type Path struct {
	To       string
	Duration int64
	Edges    []model.Footpath
}

func (p Path) Append(n *Node, start int64) *Node {
	for _, w := range p.Edges {
		n = &Node{n, model.Leg{Kind: "walk", Ref: w.ID, Departure: start, Arrival: start + w.Duration}}
		start += w.Duration
	}
	return n
}

type Walker struct {
	p     *Profile
	q     model.Query
	cache map[string][]Path
	Count int
}

func (p *Profile) Walker(q model.Query) *Walker {
	w := *q.Walking
	q.Walking = &w
	return &Walker{p: p, q: q, cache: map[string][]Path{}}
}

type entry struct {
	stop     string
	distance int64
}
type queue []entry

func (q queue) Len() int { return len(q) }
func (q queue) Less(i, j int) bool {
	if q[i].distance == q[j].distance {
		return q[i].stop < q[j].stop
	}
	return q[i].distance < q[j].distance
}
func (q queue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *queue) Push(v any)   { *q = append(*q, v.(entry)) }
func (q *queue) Pop() any     { old := *q; v := old[len(old)-1]; *q = old[:len(old)-1]; return v }

// Paths is a query-local metric closure. The destination is absorbing, matching
// the itinerary contract; the cap is checked for the actual role by the caller.
func (w *Walker) Paths(ctx context.Context, from string) ([]Path, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if paths, ok := w.cache[from]; ok {
		return paths, nil
	}
	w.Count++
	limit := max(w.q.Walking.Access, w.q.Walking.Transfer, w.q.Walking.Egress, w.q.Walking.Direct)
	targets := []walking.Endpoint{{NodeID: from}}
	if w.q.Destination != "" {
		targets = append(targets, walking.Endpoint{NodeID: w.q.Destination})
	}
	closure, err := w.p.walkingGraph.ReachWithTargets(ctx, walking.ReachRequest{Targets: targets,
		SnapshotID: w.p.walkingGraph.Descriptor().SnapshotID, From: walking.Endpoint{NodeID: from},
		Options: walking.Options{Role: walking.Closure, MaxSeconds: limit, AccessibleOnly: w.q.AccessibleOnly, StopAt: w.q.Destination},
	}, w.p.walkingTargets)
	if err != nil {
		return nil, err
	}
	paths := make([]Path, 0, len(closure.Paths))
	for _, p := range closure.Paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if p.To != from && p.To != w.q.Destination && !w.p.transitStops[p.To] {
			continue
		}
		edges := make([]model.Footpath, 0, len(p.Segments))
		for _, s := range p.Segments {
			edges = append(edges, w.p.walkingEdges[s.ID])
		}
		paths = append(paths, Path{To: p.To, Duration: p.Seconds, Edges: edges})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	w.cache[from] = paths
	return paths, nil
}

// ReadyTime applies stop-pair transfer rules AFTER the complete walking block.
// Waiting for a minimum does not change walking timestamps or add a walking edge.
func (p *Profile) ReadyTime(q model.Query, from, to string, arrival, duration int64) (int64, bool, error) {
	r, e := p.Rules.Resolve(model.Connection{To: from}, model.Connection{From: to}, q.TransferBuffer)
	return arrival + max(duration, r.Minimum), r.Allowed, e
}
