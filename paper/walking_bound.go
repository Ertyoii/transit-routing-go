package paper

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

// ProofLimits bounds certification work separately from timetable search.
// Exhaustion supplies no certificate and must fail the query closed.
type ProofLimits struct {
	MaxSettled int `json:"max_settled"`
	MaxEdges   int `json:"max_edges"`
}

func DefaultProofLimits() ProofLimits { return ProofLimits{MaxSettled: 10000000, MaxEdges: 50000000} }

type LegProof struct {
	SufficientLegs int    `json:"sufficient_legs"`
	Method         string `json:"method"`
	Sources        int    `json:"walking_sources"`
	Settled        int    `json:"settled_vertices"`
	Edges          int    `json:"walking_edge_visits"`
}

// CertifyLegBound bounds the size of every shortest-walking representative the
// supported paper kernels may return. It does not change journey-leg semantics.
// It is a capability proof, not a timetable optimum or a journey oracle.
func (p *Profile) CertifyLegBound(ctx context.Context, q model.Query, limits ProofLimits) (proof LegProof, err error) {
	if err = q.Validate(p.Data); err != nil {
		return
	}
	if q.Walking == nil || limits.MaxSettled <= 0 || limits.MaxEdges <= 0 {
		err = fmt.Errorf("walking leg proof requires explicit walking and positive work limits")
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if q.Origin == q.Destination {
		proof.Method = "empty_journey"
		return
	}
	w := q.Walking
	durationBound := func(cap int64) int {
		if p.minimumWalkDuration == Infinity {
			return 0
		}
		return min(p.Capability.MaximumSimpleWalkingEdges, int(cap/p.minimumWalkDuration))
	}
	combine := func(access, transfer, egress, direct int) int {
		n := direct
		if q.MaxBoardings > 0 {
			n = max(n, q.MaxBoardings*p.Capability.MaximumTripSegments+access+egress+(q.MaxBoardings-1)*transfer)
		}
		if p.minimumEventDuration != Infinity {
			n = min(n, int((q.LatestArrival-q.Departure)/p.minimumEventDuration))
		}
		return n
	}
	proof.SufficientLegs = combine(durationBound(w.Access), durationBound(w.Transfer), durationBound(w.Egress), durationBound(w.Direct))
	proof.Method = "positive_duration_and_component_bound"
	if proof.SufficientLegs <= q.MaxLegs {
		return
	}
	board := map[string]bool{}
	alight := map[string]bool{}
	for i, c := range p.Data.Connections {
		if i%1024 == 0 {
			if err = ctx.Err(); err != nil {
				return
			}
		}
		if !p.TripAllowed(q, c.TripID) {
			continue
		}
		if c.Pickup && c.Departure >= q.Departure && c.Departure <= q.LatestArrival && p.Accessible(q, c.From) {
			board[c.From] = true
		}
		if c.Dropoff && c.Arrival >= q.Departure && c.Arrival <= q.LatestArrival && p.Accessible(q, c.To) {
			alight[c.To] = true
		}
	}
	// Equal-distance alternatives may have different edge counts. Since every
	// edge is positive, all shortest predecessors settle first; keep the maximum
	// hop count among ALL shortest paths, not only a convenient chosen witness.
	walks := func(from string, cap int64) (map[string]int64, map[string]int, error) {
		proof.Sources++
		dist := map[string]int64{from: 0}
		hops := map[string]int{from: 0}
		h := &queue{{from, 0}}
		for h.Len() > 0 {
			if e := ctx.Err(); e != nil {
				return nil, nil, e
			}
			u := heap.Pop(h).(entry)
			if dist[u.stop] != u.distance {
				continue
			}
			if proof.Settled == limits.MaxSettled {
				return nil, nil, fmt.Errorf("walking leg proof exhausted settled-vertex budget")
			}
			proof.Settled++
			if u.stop == q.Destination || !p.Accessible(q, u.stop) {
				continue
			}
			for _, edge := range p.Outgoing[u.stop] {
				if proof.Edges == limits.MaxEdges {
					return nil, nil, fmt.Errorf("walking leg proof exhausted edge budget")
				}
				proof.Edges++
				if !p.Accessible(q, edge.To) || q.AccessibleOnly && edge.Accessible != model.Yes {
					continue
				}
				next := u.distance + edge.Duration
				if next > cap {
					continue
				}
				old, ok := dist[edge.To]
				if !ok || next < old {
					dist[edge.To] = next
					hops[edge.To] = hops[u.stop] + 1
					heap.Push(h, entry{edge.To, next})
				} else if next == old {
					hops[edge.To] = max(hops[edge.To], hops[u.stop]+1)
				}
			}
		}
		return dist, hops, nil
	}
	access, transfer, egress, direct := 0, 0, 0, 0
	ds, hs, e := walks(q.Origin, max(w.Access, w.Direct))
	if e != nil {
		err = e
		return
	}
	for s, d := range ds {
		if board[s] && d <= w.Access {
			access = max(access, hs[s])
		}
		if s == q.Destination && d <= w.Direct {
			direct = hs[s]
		}
	}
	sources := []string{}
	if q.MaxBoardings > 0 {
		for s := range alight {
			if s != q.Destination && len(p.Outgoing[s]) > 0 {
				sources = append(sources, s)
			}
		}
	}
	sort.Strings(sources)
	for _, s := range sources {
		ds, hs, e = walks(s, max(w.Transfer, w.Egress))
		if e != nil {
			err = e
			return
		}
		for t, d := range ds {
			if board[t] && d <= w.Transfer {
				transfer = max(transfer, hs[t])
			}
			if t == q.Destination && d <= w.Egress {
				egress = max(egress, hs[t])
			}
		}
	}
	proof.SufficientLegs = combine(access, transfer, egress, direct)
	proof.Method = "relevant_endpoint_shortest_walk_hops"
	return
}
