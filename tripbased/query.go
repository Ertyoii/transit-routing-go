package tripbased

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type Stats struct {
	Rounds                      int `json:"rounds"`
	QueuedSegments              int `json:"queued_trip_segments"`
	TripSegments                int `json:"processed_trip_segments"`
	Connections                 int `json:"reconstruction_connection_visits"`
	Transfers                   int `json:"transfer_visits"`
	LaterTripLabels             int `json:"later_trip_label_updates"`
	ArrivalPruned               int `json:"arrival_pruned_segments"`
	WalkClosures                int `json:"query_walk_closures"`
	FlagRejected                int `json:"flag_rejected_transfers,omitempty"`
	DestinationWalkChecks       int `json:"destination_walk_checks,omitempty"`
	DestinationWalkRepairs      int `json:"destination_walk_repairs,omitempty"`
	RepairTripSearches          int `json:"repair_trip_searches,omitempty"`
	DominatedWalkParentsAvoided int `json:"dominated_walk_parents_avoided,omitempty"`
}
type Result struct {
	Journeys []model.Journey `json:"journeys"`
	Complete bool            `json:"complete"`
	Error    string          `json:"error,omitempty"`
	Stats    Stats           `json:"stats"`
}
type segment struct {
	trip, begin, end int
	parent           *paper.Node
}

func (x *Index) Search(ctx context.Context, q model.Query) (Result, error) {
	return x.search(ctx, q, nil, false)
}

// filter restricts an immutable accelerator transfer graph; nil preserves TB.
// targetEgressNonbinding is used only after FLASH certifies that every shortest
// walk into this destination fits Egress. Thus a transfer passing the target
// can be replaced by its feasible shorter egress prefix. Ordinary TB has no
// such destination certificate and retains its stronger global requirement.
func (x *Index) search(ctx context.Context, q model.Query, filter func(int, int, int) bool, targetEgressNonbinding bool) (Result, error) {
	return x.searchWalking(ctx, q, filter, targetEgressNonbinding, false)
}

func (x *Index) searchWalking(ctx context.Context, q model.Query, filter func(int, int, int) bool, targetEgressNonbinding, capped bool) (out Result, err error) {
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
	if !targetEgressNonbinding && !capped && q.Walking.Egress < q.Walking.Transfer {
		err = fmt.Errorf("Trip-Based capability: egress must cover intermediate walking for destination-independent transfer closure")
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
	var repairs map[*paper.Path]destinationWalk
	if capped {
		repairs = make(map[*paper.Path]destinationWalk)
	}
	defer func() { out.Stats.WalkClosures = walker.Count }()
	reached := make([]int, len(x.trips))
	for ti, t := range x.trips {
		reached[ti] = len(t.cs)
	}
	enqueue := func(queue *[]segment, ti, board int, parent *paper.Node) error {
		if board >= reached[ti] {
			return nil
		}
		if out.Stats.QueuedSegments == x.cfg.MaxQueuedSegments {
			return fmt.Errorf("Trip-Based incomplete: queued segment budget exhausted")
		}
		*queue = append(*queue, segment{ti, board, reached[ti], parent})
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
	queue := []segment{}
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
				if err = enqueue(&queue, l.trips[rank], o.position, w.Append(nil, q.Departure)); err != nil {
					return
				}
			}
		}
	}
	for boards := 1; boards <= q.MaxBoardings && len(queue) > 0; boards++ {
		out.Stats.Rounds++
		next := []segment{}
		best := paper.Label{Time: bestArrival}
		for _, s := range queue {
			if err = ctx.Err(); err != nil {
				return
			}
			out.Stats.TripSegments++
			t := x.trips[s.trip]
			if t.cs[s.begin].Arrival >= best.Time {
				out.Stats.ArrivalPruned++
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
					if c.Arrival < best.Time {
						best = paper.Label{Time: c.Arrival, Parent: parent}
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
				if w := endpointPath(ws, q.Destination); w != nil && w.Duration <= q.Walking.Egress && c.Arrival+w.Duration <= q.LatestArrival && c.Arrival+w.Duration < best.Time {
					best = paper.Label{Time: c.Arrival + w.Duration, Parent: w.Append(parent, c.Arrival)}
				}
				if boards == q.MaxBoardings || i >= s.end || c.Arrival >= best.Time {
					continue
				}
				for ei, tr := range x.links[s.trip][i] {
					out.Stats.Transfers++
					if filter != nil && !filter(s.trip, i, ei) {
						out.Stats.FlagRejected++
						continue
					}
					if out.Stats.Transfers%256 == 0 {
						if e := ctx.Err(); e != nil {
							err = e
							return
						}
					}
					if tr.walk.Duration > q.Walking.Transfer || c.Arrival+tr.walk.Duration > q.LatestArrival || x.trips[tr.trip].cs[tr.board].Arrival > q.LatestArrival {
						continue
					}
					if capped {
						var ok bool
						tr, ok, err = x.repairDestinationTransfer(q, c, tr, ws, repairs, &out.Stats)
						if err != nil {
							return
						}
						if !ok {
							continue
						}
					} else {
						touchesTarget := false
						for _, edge := range tr.walk.Edges {
							if edge.To == q.Destination {
								touchesTarget = true
								break
							}
						}
						if touchesTarget {
							continue
						}
					}
					if tr.board >= reached[tr.trip] {
						out.Stats.DominatedWalkParentsAvoided++
						continue
					}

					if err = enqueue(&next, tr.trip, tr.board, tr.walk.Append(parent, c.Arrival)); err != nil {
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
