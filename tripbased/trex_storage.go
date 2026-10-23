package tripbased

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/paper"
	"unsafe"
)

// Count work is charged to the same construction-work budget as fill work.
// ExactStorage is opt-in: incomplete count passes retain no transfer prefix.
// Complete constructions retain the same ordered IDs and witnesses.
type TREXStorageWork struct {
	TransferCountWork      int    `json:"transfer_count_work"`
	CountedTransfers       int    `json:"counted_transfers"`
	CountSameLineDiscarded int    `json:"count_same_line_discarded"`
	TransferCountComplete  bool   `json:"transfer_count_complete"`
	OverlayCountWork       int    `json:"overlay_count_work"`
	EdgeCapacity           int    `json:"edge_capacity"`
	RankCapacity           int    `json:"rank_capacity"`
	AdjacencyCapacity      int    `json:"adjacency_capacity"`
	OverlayCapacity        int    `json:"overlay_capacity"`
	ImplicitOverlayEntries int    `json:"implicit_overlay_entries"`
	EdgeBytes              uint64 `json:"edge_bytes"`
	PathPoolBytes          uint64 `json:"path_pool_bytes"`
	NativeEscapeBytes      uint64 `json:"native_escape_bytes"`
	EventTripBytes         uint64 `json:"event_trip_bytes"`
	EventRangeBytes        uint64 `json:"event_range_bytes"`
	OverlayBytes           uint64 `json:"overlay_bytes"`
	ChunkHeaderBytes       uint64 `json:"chunk_header_bytes"`
	RankBytes              uint64 `json:"rank_bytes"`
}

func (x *TREXIndex) buildTREXTransfers(ctx context.Context) error {
	x.byEvent = make([][]trexEventRange, len(x.base.trips))
	x.offsets = make([]int, len(x.base.trips)+1)
	for ti, t := range x.base.trips {
		x.offsets[ti+1] = x.offsets[ti] + len(t.cs)
		x.byEvent[ti] = make([]trexEventRange, len(t.cs))
	}
	x.eventTrips = make([]int, x.offsets[len(x.offsets)-1])
	for ti, t := range x.base.trips {
		for i := range t.cs {
			if e := ctx.Err(); e != nil {
				return e
			}
			x.eventTrips[x.offsets[ti]+i] = ti
		}
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
		x.edges.expected = count
		x.ranks = make([]uint8, 0, count)
		// IDs are emitted in event-major order. Retain only event ranges,
		// never a second adjacency ID for every transfer.
		offset := 0
		for ti, t := range x.base.trips {
			for i := range t.cs {
				if e = ctx.Err(); e != nil {
					return e
				}
				n := counts[x.offsets[ti]+i]
				x.byEvent[ti][i] = trexEventRange{offset, offset}
				offset += n
			}
		}
	}
	builder := trexEdgeBuilder{store: &x.edges}
	e := x.scanTREXTransfers(ctx, func(ti, i, to, board int, p *paper.Path) error {
		if x.edgeCount() >= x.cfg.Limits.MaxTransfers {
			return fmt.Errorf("T-REX incomplete: transfer budget exhausted")
		}
		id := x.edgeCount()
		row := &x.byEvent[ti][i]
		if x.cfg.ExactStorage {
			if id >= x.edges.expected || row.end != id {
				return fmt.Errorf("T-REX internal: exact transfer count/fill mismatch")
			}
		} else if row.begin == row.end {
			row.begin, row.end = id, id
		}
		if row.end != id {
			return fmt.Errorf("T-REX internal: transfer event order mismatch")
		}
		x.edges.append(builder.pack(trexEdge{ti, i, to, board, p}, x.offsets[ti]+i, x.offsets[to]+board))
		row.end++
		x.ranks = append(x.ranks, 0)
		x.snap.Stats.Transfers++
		return nil
	})
	if !x.cfg.ExactStorage {
		// Empty events emit no callback. Normalize their zero-length ranges to
		// the same event-major boundary used by counted storage and overlays.
		at := 0
		for _, rows := range x.byEvent {
			for i := range rows {
				if rows[i].begin == rows[i].end {
					rows[i] = trexEventRange{at, at}
				} else {
					at = rows[i].end
				}
			}
		}
	}
	x.snap.Stats.Storage.EdgeCapacity = x.edges.capacity()
	x.snap.Stats.Storage.RankCapacity = cap(x.ranks)
	// AdjacencyCapacity is zero: event-major ranges make adjacency IDs implicit.
	x.snap.Stats.Storage.EdgeBytes = uint64(x.edges.capacity()) * uint64(unsafe.Sizeof(trexPackedEdge{}))
	x.snap.Stats.Storage.PathPoolBytes = uint64(cap(x.edges.paths)) * uint64(unsafe.Sizeof((*paper.Path)(nil)))
	x.snap.Stats.Storage.NativeEscapeBytes = uint64(cap(x.edges.wide)) * uint64(unsafe.Sizeof(trexEdge{}))
	x.snap.Stats.Storage.EventTripBytes = uint64(cap(x.eventTrips)) * uint64(unsafe.Sizeof(int(0)))
	for _, rows := range x.byEvent {
		x.snap.Stats.Storage.EventRangeBytes += uint64(cap(rows)) * uint64(unsafe.Sizeof(trexEventRange{}))
	}
	x.snap.Stats.Storage.ChunkHeaderBytes = uint64(cap(x.edges.chunks)) * uint64(unsafe.Sizeof([]trexPackedEdge(nil)))
	x.snap.Stats.Storage.RankBytes = uint64(cap(x.ranks))
	if e == nil && x.cfg.ExactStorage && x.edgeCount() != x.snap.Stats.Storage.CountedTransfers {
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
