package tripbased

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/paper"
)

// Count work is charged to the same construction-work budget as fill work.
// ExactStorage is opt-in: incomplete count passes retain no transfer prefix.
// Complete constructions retain the same ordered IDs and witnesses.
type TREXStorageWork struct {
	TransferCountWork      int  `json:"transfer_count_work"`
	CountedTransfers       int  `json:"counted_transfers"`
	CountSameLineDiscarded int  `json:"count_same_line_discarded"`
	TransferCountComplete  bool `json:"transfer_count_complete"`
	OverlayCountWork       int  `json:"overlay_count_work"`
	EdgeCapacity           int  `json:"edge_capacity"`
	RankCapacity           int  `json:"rank_capacity"`
	AdjacencyCapacity      int  `json:"adjacency_capacity"`
	OverlayCapacity        int  `json:"overlay_capacity"`
}

func (x *TREXIndex) buildTREXTransfers(ctx context.Context) error {
	x.byEvent = make([][][]int, len(x.base.trips))
	x.offsets = make([]int, len(x.base.trips)+1)
	for ti, t := range x.base.trips {
		x.offsets[ti+1] = x.offsets[ti] + len(t.cs)
		x.byEvent[ti] = make([][]int, len(t.cs))
	}
	if x.cfg.ExactStorage {
		counts := make([]int, x.offsets[len(x.offsets)-1])
		// The copy borrows immutable input only; its stats are a value copy and the
		// scanner does not mutate slices/maps. It cannot publish edges or ranks.
		counter := *x
		before := counter.snap.Stats.Work
		count := 0
		e := counter.scanTREXTransfers(ctx, func(ti, i, to, board int, p *paper.Path) error {
			if count >= x.cfg.Limits.MaxTransfers {
				return fmt.Errorf("T-REX incomplete: transfer budget exhausted")
			}
			counts[x.offsets[ti]+i]++
			count++
			return nil
		})
		x.snap.Stats.Storage.TransferCountWork = counter.snap.Stats.Work - before
		x.snap.Stats.Work = counter.snap.Stats.Work
		x.snap.Stats.Storage.CountedTransfers = count
		x.snap.Stats.Storage.CountSameLineDiscarded = counter.snap.Stats.SameLineDiscarded
		if e != nil {
			return e
		}
		x.snap.Stats.Storage.TransferCountComplete = true
		if e = ctx.Err(); e != nil {
			return e
		}
		x.edges = make([]trexEdge, 0, count)
		x.ranks = make([]uint8, 0, count)
		adjacency := make([]int, count)
		offset := 0
		for ti, t := range x.base.trips {
			for i := range t.cs {
				if e = ctx.Err(); e != nil {
					return e
				}
				n := counts[x.offsets[ti]+i]
				if n > 0 {
					x.byEvent[ti][i] = adjacency[offset : offset : offset+n]
				}
				offset += n
			}
		}
	}
	e := x.scanTREXTransfers(ctx, func(ti, i, to, board int, p *paper.Path) error {
		if len(x.edges) >= x.cfg.Limits.MaxTransfers {
			return fmt.Errorf("T-REX incomplete: transfer budget exhausted")
		}
		if x.cfg.ExactStorage && (len(x.edges) == cap(x.edges) || len(x.byEvent[ti][i]) == cap(x.byEvent[ti][i])) {
			return fmt.Errorf("T-REX internal: exact transfer count/fill mismatch")
		}
		id := len(x.edges)
		x.edges = append(x.edges, trexEdge{ti, i, to, board, p})
		x.byEvent[ti][i] = append(x.byEvent[ti][i], id)
		x.ranks = append(x.ranks, 0)
		x.snap.Stats.Transfers++
		return nil
	})
	x.snap.Stats.Storage.EdgeCapacity = cap(x.edges)
	x.snap.Stats.Storage.RankCapacity = cap(x.ranks)
	for _, rows := range x.byEvent {
		for _, row := range rows {
			x.snap.Stats.Storage.AdjacencyCapacity += cap(row)
		}
	}
	if e == nil && x.cfg.ExactStorage && len(x.edges) != x.snap.Stats.Storage.CountedTransfers {
		return fmt.Errorf("T-REX internal: exact transfer count/fill mismatch")
	}
	return e
}

func (x *TREXIndex) exactOverlayCapacity(ctx context.Context, level int) (int, error) {
	count := 0
	for _, rank := range x.ranks {
		if e := x.tick(ctx); e != nil {
			return 0, e
		}
		x.snap.Stats.Storage.OverlayCountWork++
		if int(rank) >= level {
			if count >= x.cfg.Limits.MaxOverlayEntries-x.snap.Stats.OverlayEntries {
				return 0, fmt.Errorf("T-REX incomplete: overlay entry budget exhausted")
			}
			count++
		}
	}
	return count, nil
}
