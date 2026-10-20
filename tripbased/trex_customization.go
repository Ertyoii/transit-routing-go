package tripbased

import (
	"context"
	"fmt"
	"runtime"
	"time"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// Parents describe complete boarded segments. Split/trimmed scan intervals never
// replace their original boarding position or incoming-transfer identity.
type trexNode struct {
	trip, board, edge int
	parent            *trexNode
	access            *paper.Path
}
type trexSegment struct {
	trip, begin, end, level int
	node                    *trexNode
}

func (x *TREXIndex) unpackTREX(node *trexNode, exit int, egress *paper.Path, departure int64, charge func() error) (model.Journey, []int, error) {
	j := model.Journey{Legs: []model.Leg{}}
	nodes := []*trexNode{}
	ids := []int{}
	for n := node; n != nil; n = n.parent {
		if e := charge(); e != nil {
			return j, ids, e
		}
		nodes = append(nodes, n)
	}
	for i, k := 0, len(nodes)-1; i < k; i, k = i+1, k-1 {
		nodes[i], nodes[k] = nodes[k], nodes[i]
	}
	appendWalk := func(p *paper.Path, at int64) error {
		if p == nil {
			return nil
		}
		for _, w := range p.Edges {
			if e := charge(); e != nil {
				return e
			}
			j.Legs = append(j.Legs, model.Leg{Kind: "walk", Ref: w.ID, Departure: at, Arrival: at + w.Duration})
			at += w.Duration
		}
		return nil
	}
	if len(nodes) == 0 {
		return j, ids, appendWalk(egress, departure)
	}
	if e := appendWalk(nodes[0].access, departure); e != nil {
		return j, ids, e
	}
	for i, n := range nodes {
		last := exit
		if i+1 < len(nodes) {
			last = x.edges[nodes[i+1].edge].exit
		}
		if last < n.board || last >= len(x.base.trips[n.trip].cs) {
			return j, ids, fmt.Errorf("T-REX internal: invalid witness segment")
		}
		for pos := n.board; pos <= last; pos++ {
			if e := charge(); e != nil {
				return j, ids, e
			}
			c := x.base.trips[n.trip].cs[pos]
			j.Legs = append(j.Legs, model.Leg{Kind: "ride", Ref: c.ID, Departure: c.Departure, Arrival: c.Arrival})
		}
		at := x.base.trips[n.trip].cs[last].Arrival
		if i+1 < len(nodes) {
			id := nodes[i+1].edge
			ids = append(ids, id)
			if e := appendWalk(x.edges[id].walk, at); e != nil {
				return j, ids, e
			}
		} else if e := appendWalk(egress, at); e != nil {
			return j, ids, e
		}
	}
	return j, ids, nil
}

func (x *TREXIndex) customizeTREX(ctx context.Context) error {
	// Initialize once; between event runs restore only labels actually changed.
	// This keeps independent Event-TB runs while avoiding O(runs * trips) resets.
	reached := make([]int, len(x.base.trips))
	for ti, t := range x.base.trips {
		if e := x.tick(ctx); e != nil {
			return e
		}
		reached[ti] = len(t.cs)
	}
	touched := []int{}
	// Each level reads ranks >= level. Promotions within the level cannot change
	// eligibility, so processing order does not affect the retained transfer set.
	for level := 0; level < x.cfg.Levels; level++ {
		progress := TREXLevelWork{Level: level}
		before := x.snap.Stats
		var m0, m1 runtime.MemStats
		runtime.ReadMemStats(&m0)
		start := time.Now()
		e := func() error {
			for ti, t := range x.base.trips {
				for board, c := range t.cs {
					if e := x.tick(ctx); e != nil {
						return e
					}
					a, b := x.cells[x.ids[c.From]]>>uint(level), x.cells[x.ids[c.To]]>>uint(level)
					if a == b {
						continue
					}
					if x.snap.Stats.EventRuns == x.cfg.Limits.MaxRuns {
						return fmt.Errorf("T-REX incomplete: event-run budget exhausted")
					}
					x.snap.Stats.EventRuns++
					progress.Started++
					eventBefore := x.snap.Stats
					e := x.eventTREX(ctx, ti, board, level, b, reached, &touched)
					ew := TREXEventWork{Trip: t.id, Board: board, Work: x.snap.Stats.Work - eventBefore.Work, Queued: x.snap.Stats.QueuedSegments - eventBefore.QueuedSegments, Promotions: x.snap.Stats.RankPromotions - eventBefore.RankPromotions, Complete: e == nil}
					progress.Last = ew
					if progress.Started == 1 || ew.Work > progress.MaxWork.Work {
						progress.MaxWork = ew
					}
					if e != nil {
						return e
					}
					progress.Completed++
				}
			}
			return nil
		}()
		runtime.ReadMemStats(&m1)
		progress.ElapsedNS = time.Since(start).Nanoseconds()
		progress.AllocatedBytes = m1.TotalAlloc - m0.TotalAlloc
		progress.Work = x.snap.Stats.Work - before.Work
		progress.Queued = x.snap.Stats.QueuedSegments - before.QueuedSegments
		progress.Promotions = x.snap.Stats.RankPromotions - before.RankPromotions
		progress.Traversals = x.snap.Stats.Traversals - before.Traversals
		progress.Redundant = x.snap.Stats.RedundantWitnesses - before.RedundantWitnesses
		progress.Complete = e == nil
		x.snap.Stats.Levels = append(x.snap.Stats.Levels, progress)
		if e != nil {
			return e
		}
	}
	return nil
}

func (x *TREXIndex) eventTREX(ctx context.Context, source, board, level, cell int, reached []int, touched *[]int) error {
	for _, ti := range *touched {
		if e := x.tick(ctx); e != nil {
			return e
		}
		reached[ti] = len(x.base.trips[ti].cs)
	}
	*touched = (*touched)[:0]
	queue := []trexSegment{}
	queued := 0
	enqueue := func(q *[]trexSegment, ti, board, edge int, parent *trexNode) error {
		if e := x.tick(ctx); e != nil {
			return e
		}
		if board >= reached[ti] {
			return nil
		}
		if queued == x.cfg.Limits.MaxQueuedSegments {
			return fmt.Errorf("T-REX incomplete: event queue budget exhausted")
		}
		queued++
		x.snap.Stats.QueuedSegments++
		node := &trexNode{trip: ti, board: board, edge: edge, parent: parent}
		*q = append(*q, trexSegment{trip: ti, begin: board, end: reached[ti], node: node})
		t := x.base.trips[ti]
		for _, later := range x.base.lines[t.line].trips[t.rank:] {
			if e := x.tick(ctx); e != nil {
				return e
			}
			if reached[later] <= board {
				break
			}
			if reached[later] == len(x.base.trips[later].cs) {
				*touched = append(*touched, later)
			}
			reached[later] = board
		}
		return nil
	}
	if e := enqueue(&queue, source, board, -1, nil); e != nil {
		return e
	}
	for round := 1; round <= x.cfg.MaxBoardings && len(queue) > 0; round++ {
		// Collect traversal witnesses before trimming. An event journey may end at
		// the next outside event; its recorded ExitEvent is the preceding OBE.
		for i := range queue {
			s := &queue[i]
			t := x.base.trips[s.trip]
			for p := s.begin; p < s.end; p++ {
				if e := x.tick(ctx); e != nil {
					return e
				}
				if x.cells[x.ids[t.cs[p].To]]>>uint(level) == cell {
					continue
				}
				x.snap.Stats.Traversals++
				promotes := false
				for n := s.node; n.parent != nil; n = n.parent {
					if e := x.tick(ctx); e != nil {
						return e
					}
					if int(x.ranks[n.edge]) < level+1 {
						promotes = true
						break
					}
				}
				if s.node.parent != nil && !promotes {
					x.snap.Stats.RedundantWitnesses++
				}
				// Already-supported edges cannot change this level's rank>=level
				// eligibility. Preserve the traversal and trim point, but retain
				// a full raw proof only when at least one rank increases.
				if promotes {
					if x.proofs.count == x.cfg.Limits.MaxWitnesses {
						return fmt.Errorf("T-REX incomplete: traversal witness budget exhausted")
					}
					_, ids, e := x.unpackTREX(s.node, p, nil, x.base.trips[source].cs[board].Departure, func() error { return x.tick(ctx) })
					if e != nil {
						return e
					}
					for _, id := range ids {
						if e := x.tick(ctx); e != nil {
							return e
						}
						if int(x.ranks[id]) < level+1 {
							x.ranks[id] = uint8(level + 1)
							x.snap.Stats.RankPromotions++
						}
					}
					x.proofs.append(level, board, p, ids)
				}
				s.end = p
				break
			}
		}
		if round == x.cfg.MaxBoardings {
			break
		}
		next := []trexSegment{}
		for _, s := range queue {
			for p := s.begin; p < s.end; p++ {
				if e := x.tick(ctx); e != nil {
					return e
				}
				for _, id := range x.byEvent[s.trip][p] {
					if e := x.tick(ctx); e != nil {
						return e
					}
					if int(x.ranks[id]) < level {
						continue
					}
					edge := x.edges[id]
					if e := enqueue(&next, edge.to, edge.board, id, s.node); e != nil {
						return e
					}
				}
			}
		}
		queue = next
	}
	return nil
}
