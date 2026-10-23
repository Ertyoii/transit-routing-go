package tripbased

import (
	"context"
	"fmt"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// answerTieSegment retains exactly the native reached interval and parent.
// walking is a scalar witness attribute, not a dominance resource or frontier.
type answerTieSegment struct {
	trip, begin, end int
	parent           *paper.Node
	walking          int64
}

// SearchCappedWalkingAnswerTies retains SearchCappedWalking's native two-
// dimensional arrival/boarding contract, index, reached pruning and work caps.
// Only equal-arrival answers discovered within one native boarding round prefer
// less aggregate walking. This is deliberately NOT minimum-walking representative
// completeness: native reached-position and earliest-trip pruning remain intact.
// It calls no other router and does not expose a three-dimensional frontier.
func (x *Index) SearchCappedWalkingAnswerTies(ctx context.Context, q model.Query) (out Result, err error) {
	out.Journeys = []model.Journey{}
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	if err = x.p.CheckContext(ctx, q); err != nil {
		return
	}
	if q.TransferBuffer != x.cfg.TransferBuffer || q.AccessibleOnly != x.cfg.AccessibleOnly {
		err = fmt.Errorf("Trip-Based capability: query buffer/accessibility differs from prepared index")
		return
	}
	if q.Walking.Transfer > x.cfg.TransferWalkingLimit {
		err = fmt.Errorf("Trip-Based capability: intermediate walking exceeds prepared limit")
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if !x.p.Accessible(q, q.Origin) || !x.p.Accessible(q, q.Destination) {
		return
	}
	if q.Origin == q.Destination {
		out.Journeys = append(out.Journeys, paper.Journey(nil))
		return
	}
	walker := x.p.Walker(q)
	repairs := make(map[*paper.Path]destinationWalk)
	defer func() { out.Stats.WalkClosures = walker.Count }()
	reached := make([]int, len(x.trips))
	for ti, t := range x.trips {
		reached[ti] = len(t.cs)
	}
	enqueue := func(queue *[]answerTieSegment, ti, board int, parent *paper.Node, walking int64) error {
		if board >= reached[ti] {
			return nil
		}
		if out.Stats.QueuedSegments == x.cfg.MaxQueuedSegments {
			return fmt.Errorf("Trip-Based incomplete: queued segment budget exhausted")
		}
		*queue = append(*queue, answerTieSegment{ti, board, reached[ti], parent, walking})
		out.Stats.QueuedSegments++
		t := x.trips[ti]
		for _, later := range x.lines[t.line].trips[t.rank:] {
			if board >= reached[later] {
				break
			}
			reached[later] = board
			out.Stats.LaterTripLabels++
		}
		return nil
	}
	queue := []answerTieSegment{}
	bestArrival := paper.Infinity
	paths, e := walker.Paths(ctx, q.Origin)
	if e != nil {
		err = e
		return
	}
	for _, w := range paths {
		at := q.Departure + w.Duration
		if at > q.LatestArrival {
			continue
		}
		if w.To == q.Destination {
			if w.Duration <= q.Walking.Direct {
				bestArrival = at
				out.Journeys = append(out.Journeys, paper.Journey(w.Append(nil, q.Departure)))
			}
			continue
		}
		if q.MaxBoardings == 0 || w.Duration > q.Walking.Access {
			continue
		}
		for _, o := range x.at[w.To] {
			l := x.lines[o.line]
			prototype := x.trips[l.trips[0]]
			if !prototype.cs[o.position].Pickup || !x.p.TripAllowed(q, prototype.id) {
				continue
			}
			rank := sort.Search(len(l.trips), func(r int) bool { return x.trips[l.trips[r]].cs[o.position].Departure >= at })
			if rank < len(l.trips) {
				if o.position >= reached[l.trips[rank]] {
					out.Stats.DominatedWalkParentsAvoided++
					continue
				}
				if err = enqueue(&queue, l.trips[rank], o.position, w.Append(nil, q.Departure), w.Duration); err != nil {
					return
				}
			}
		}
	}
	for boards := 1; boards <= q.MaxBoardings && len(queue) > 0; boards++ {
		out.Stats.Rounds++
		next := []answerTieSegment{}
		best := paper.Label{Time: bestArrival}
		bestWalking := paper.Infinity
		for _, s := range queue {
			if err = ctx.Err(); err != nil {
				return
			}
			out.Stats.TripSegments++
			t := x.trips[s.trip]
			if t.cs[s.begin].Arrival >= best.Time {
				out.Stats.ArrivalPruned++
				// The native arrival-pruned segment can only tie at its first
				// connection: all subsequent connections have positive duration.
				// Recover that literal destination answer without opening a new
				// suffix, walking closure, reached interval, or transfer scan.
				c := t.cs[s.begin]
				if best.Time < bestArrival && c.Arrival == best.Time && c.To == q.Destination && c.Dropoff && x.p.Accessible(q, c.To) && s.walking < bestWalking {
					best.Parent = paper.Ride(s.parent, c)
					bestWalking = s.walking
					out.Stats.Connections++
				}
				continue
			}
			parent := s.parent
			// Immutable reconstruction follows the whole suffix; transfers are scanned
			// only in the newly reached interval [begin,end), as in Algorithm 4.
			for i := s.begin; i < len(t.cs); i++ {
				if err = ctx.Err(); err != nil {
					return
				}
				c := t.cs[i]
				if c.Arrival > q.LatestArrival {
					break
				}
				parent = paper.Ride(parent, c)
				out.Stats.Connections++
				if !c.Dropoff || !x.p.Accessible(q, c.To) {
					continue
				}
				if c.To == q.Destination {
					if c.Arrival < best.Time || c.Arrival == best.Time && s.walking < bestWalking {
						best = paper.Label{Time: c.Arrival, Parent: parent}
						bestWalking = s.walking
					}
					break
				}
				ws, e := walker.Paths(ctx, c.To)
				if e != nil {
					err = e
					return
				}
				// Walker paths are lexically ordered by endpoint. One lookup replaces
				// a whole closure scan at every event of the same alighting stop.
				if w := endpointPath(ws, q.Destination); w != nil && w.Duration <= q.Walking.Egress && c.Arrival+w.Duration <= q.LatestArrival && (c.Arrival+w.Duration < best.Time || c.Arrival+w.Duration == best.Time && s.walking+w.Duration < bestWalking) {
					best = paper.Label{Time: c.Arrival + w.Duration, Parent: w.Append(parent, c.Arrival)}
					bestWalking = s.walking + w.Duration
				}
				if boards == q.MaxBoardings || i >= s.end || c.Arrival >= best.Time {
					continue
				}
				for _, packed := range x.links[s.trip][i] {
					out.Stats.Transfers++
					if out.Stats.Transfers%256 == 0 {
						if e := ctx.Err(); e != nil {
							err = e
							return
						}
					}
					tr := x.linkData.unpack(packed)
					if tr.walk.Duration > q.Walking.Transfer || c.Arrival+tr.walk.Duration > q.LatestArrival || x.trips[tr.trip].cs[tr.board].Arrival > q.LatestArrival {
						continue
					}
					var ok bool
					tr, ok, err = x.repairDestinationTransfer(q, c, tr, ws, repairs, &out.Stats)
					if err != nil {
						return
					}
					if !ok {
						continue
					}
					if tr.board >= reached[tr.trip] {
						out.Stats.DominatedWalkParentsAvoided++
						continue
					}

					if err = enqueue(&next, tr.trip, tr.board, tr.walk.Append(parent, c.Arrival), s.walking+tr.walk.Duration); err != nil {
						return
					}
				}
			}
		}
		if best.Time < bestArrival {
			bestArrival = best.Time
			out.Journeys = append(out.Journeys, paper.Journey(best.Parent))
		}
		queue = next
	}
	return
}

// CappedWalkingAnswerTiesCapabilities names the narrower local representative
// refinement while preserving every native preparation/capability restriction.
func (x *Index) CappedWalkingAnswerTiesCapabilities() Capability {
	c := x.CappedWalkingCapabilities()
	c.Contract = "arrival_boardings_pareto_representatives_local_equal_arrival_answer_walk_ties"
	return c
}
