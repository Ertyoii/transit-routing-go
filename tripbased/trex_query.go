package tripbased

import (
	"context"
	"fmt"
	"math/bits"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

type TREXQueryStats struct {
	Mode           string `json:"mode"`
	Work           int    `json:"work"`
	Rounds         int    `json:"rounds"`
	QueuedSegments int    `json:"queued_segments"`
	SplitSegments  int    `json:"split_segments"`
	TargetEvents   int    `json:"target_events"`
	TargetPruned   int    `json:"target_pruned"`
	TransferVisits int    `json:"transfer_visits"`
	RankTests      int    `json:"rank_tests"`
	RankRejected   int    `json:"rank_rejected"`
	ReachedUpdates int    `json:"reached_updates"`
	UnpackedLegs   int    `json:"unpacked_legs"`
}
type TREXResult struct {
	Journeys []model.Journey `json:"journeys"`
	Complete bool            `json:"complete"`
	Error    string          `json:"error,omitempty"`
	Stats    TREXQueryStats  `json:"stats"`
}

func (x *TREXIndex) Search(ctx context.Context, q model.Query) (TREXResult, error) {
	return x.searchTREX(ctx, q, "overlay")
}
func (x *TREXIndex) SearchRanked(ctx context.Context, q model.Query) (TREXResult, error) {
	return x.searchTREX(ctx, q, "rank-test")
}
func (x *TREXIndex) SearchUnranked(ctx context.Context, q model.Query) (TREXResult, error) {
	return x.searchTREX(ctx, q, "unranked")
}

func (x *TREXIndex) searchTREX(ctx context.Context, q model.Query, mode string) (out TREXResult, err error) {
	out.Journeys = []model.Journey{}
	out.Stats.Mode = mode
	defer func() {
		out.Complete = err == nil
		if err != nil {
			out.Error = err.Error()
		}
	}()
	bounded := mode == "bounded-walking-tb"
	if bounded {
		err = x.checkTREXResources(ctx, q)
	} else {
		err = x.checkTREXQuery(ctx, q)
	}
	if err != nil {
		return
	}
	charge := func() error {
		if e := ctx.Err(); e != nil {
			return e
		}
		if out.Stats.Work == x.cfg.Limits.MaxQueryWork {
			return fmt.Errorf("T-REX incomplete: query work budget exhausted")
		}
		out.Stats.Work++
		return nil
	}
	if q.Origin == q.Destination {
		out.Journeys = append(out.Journeys, model.Journey{Legs: []model.Leg{}})
		return
	}
	source, target := x.ids[q.Origin], x.ids[q.Destination]
	sourceCell, targetCell := x.cells[source], x.cells[target]
	relevant := func(stop string) int {
		c := x.cells[x.ids[stop]]
		return min(bits.Len(uint(c^sourceCell)), bits.Len(uint(c^targetCell)))
	}
	reached := make([]int, len(x.base.trips))
	for ti, t := range x.base.trips {
		if err = charge(); err != nil {
			return
		}
		reached[ti] = len(t.cs)
	}
	queue := []trexSegment{}
	enqueue := func(q *[]trexSegment, ti, board, edge int, parent *trexNode, access *paper.Path) error {
		if e := charge(); e != nil {
			return e
		}
		if board >= reached[ti] {
			return nil
		}
		if out.Stats.QueuedSegments+out.Stats.SplitSegments == x.cfg.Limits.MaxQueuedSegments {
			return fmt.Errorf("T-REX incomplete: query segment budget exhausted")
		}
		out.Stats.QueuedSegments++
		node := &trexNode{ti, board, edge, parent, access}
		*q = append(*q, trexSegment{trip: ti, begin: board, end: reached[ti], node: node})
		t := x.base.trips[ti]
		for _, later := range x.base.lines[t.line].trips[t.rank:] {
			if e := charge(); e != nil {
				return e
			}
			if reached[later] <= board {
				break
			}
			reached[later] = board
			out.Stats.ReachedUpdates++
		}
		return nil
	}
	bestArrival := q.LatestArrival + 1
	for wi := range x.walks[source] {
		w := &x.walks[source][wi]
		if err = charge(); err != nil {
			return
		}
		at := q.Departure + w.Duration
		if w.To == q.Destination {
			if at <= q.LatestArrival && (!bounded || w.Duration <= q.Walking.Direct) {
				j, _, e := x.unpackTREX(nil, 0, w, q.Departure, charge)
				if e != nil {
					err = e
					return
				}
				out.Journeys = append(out.Journeys, j)
				out.Stats.UnpackedLegs += len(j.Legs)
				bestArrival = at
			}
			continue
		}
		if q.MaxBoardings == 0 || at > q.LatestArrival || bounded && w.Duration > q.Walking.Access {
			continue
		}
		for _, o := range x.base.at[w.To] {
			if err = charge(); err != nil {
				return
			}
			line := x.base.lines[o.line]
			lo, hi := 0, len(line.trips)
			for lo < hi {
				if err = charge(); err != nil {
					return
				}
				m := (lo + hi) / 2
				if x.base.trips[line.trips[m]].cs[o.position].Departure < at {
					lo = m + 1
				} else {
					hi = m
				}
			}
			if lo < len(line.trips) {
				if err = enqueue(&queue, line.trips[lo], o.position, -1, nil, w); err != nil {
					return
				}
			}
		}
	}
	for round := 1; round <= q.MaxBoardings && len(queue) > 0; round++ {
		out.Stats.Rounds++
		scan := queue
		if mode == "overlay" {
			scan = []trexSegment{}
			for _, s := range queue {
				for pos := s.begin; pos < s.end; {
					if err = charge(); err != nil {
						return
					}
					level := relevant(x.base.trips[s.trip].cs[pos].To)
					lower := max(level-1, 0)
					end := min(s.end, x.successors[lower][x.offsets[s.trip]+pos])
					if out.Stats.QueuedSegments+out.Stats.SplitSegments == x.cfg.Limits.MaxQueuedSegments {
						err = fmt.Errorf("T-REX incomplete: query segment budget exhausted")
						return
					}
					out.Stats.SplitSegments++
					scan = append(scan, trexSegment{s.trip, pos, end, level, s.node})
					pos = end
				}
			}
		}
		arrival := bestArrival
		var found *trexNode
		exit := -1
		var egress *paper.Path
		// All target relaxations precede all transfer relaxations in a round.
		for _, s := range scan {
			if mode == "overlay" && x.cells[x.ids[x.base.trips[s.trip].cs[s.begin].To]] != targetCell {
				continue
			}
			for i := s.begin; i < s.end; i++ {
				if err = charge(); err != nil {
					return
				}
				out.Stats.TargetEvents++
				c := x.base.trips[s.trip].cs[i]
				if c.Arrival >= arrival {
					break
				}
				p := x.walkMap[x.ids[c.To]][target]
				if p != nil && (!bounded || p.Duration <= q.Walking.Egress) && c.Arrival+p.Duration < arrival {
					arrival = c.Arrival + p.Duration
					found = s.node
					exit = i
					egress = p
				}
			}
		}
		if arrival < bestArrival {
			j, _, e := x.unpackTREX(found, exit, egress, q.Departure, charge)
			if e != nil {
				err = e
				return
			}
			out.Journeys = append(out.Journeys, j)
			out.Stats.UnpackedLegs += len(j.Legs)
			bestArrival = arrival
		}
		if round == q.MaxBoardings {
			break
		}
		next := []trexSegment{}
		for _, s := range scan {
			if err = charge(); err != nil {
				return
			}
			if x.base.trips[s.trip].cs[s.begin].Arrival >= bestArrival {
				out.Stats.TargetPruned++
				continue
			}
			level := 0
			if mode == "overlay" {
				level = s.level
			} else {
				for pos := s.begin; pos < s.end; pos++ {
					if err = charge(); err != nil {
						return
					}
					if x.base.trips[s.trip].cs[pos].Arrival >= bestArrival {
						s.end = pos
						break
					}
				}
			}
			o := x.overlays[level]
			begin, end := o.starts[x.offsets[s.trip]+s.begin], o.starts[x.offsets[s.trip]+s.end]
			for _, id := range o.edges[begin:end] {
				if err = charge(); err != nil {
					return
				}
				out.Stats.TransferVisits++
				edge := x.edges[id]
				if bounded && (edge.walk.Duration > q.Walking.Transfer || edge.walk.To == q.Destination) {
					continue
				}
				if mode == "rank-test" {
					out.Stats.RankTests++
					if x.snap.Transfers[id].Rank < relevant(x.base.trips[edge.from].cs[edge.exit].To) {
						out.Stats.RankRejected++
						continue
					}
				}
				if err = enqueue(&next, edge.to, edge.board, id, s.node, nil); err != nil {
					return
				}
			}
		}
		queue = next
	}
	return
}
