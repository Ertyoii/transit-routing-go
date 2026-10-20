package tripbased

import (
	"context"
	"github.com/ertyoii/transit-routing-go/model"
)

// searchRoleCapped composes only conservative suffix-reachability flags with
// the accepted goal-absorbing repair kernel. Canonical optimal-profile flags
// have no such cap-preservation certificate and cannot select this policy.
func (x *FlashIndex) searchRoleCapped(ctx context.Context, q model.Query, mode int) (Result, error) {
	index := x.cp
	if mode == 2 {
		index = x.base
	}
	// Preparation enumerated the COMPLETE unconstrained shortest walking metric;
	// ClosureDiameter was not a radius truncation. A goal-avoiding detour may be
	// longer than that diameter. This query-local view permits the original role
	// cap to reach the repair kernel; it changes neither query nor prepared data.
	view := *index
	view.cfg.TransferWalkingLimit = model.MaxServiceSecond
	var filter func(int, int, int) bool
	if mode == 0 {
		cell := x.snap.Partition.Cells[q.Destination]
		filter = func(t, i, e int) bool { return x.flagged(x.transferID(t, i, e), cell) }
	}
	// Filtering refers to the original prepared edge BEFORE goal-avoiding repair.
	// If a later repaired receiver has a feasible suffix, its earlier same-line
	// receiver has an unbounded feasible suffix as well (strict FIFO, identical
	// permissions, static stop-pair rules). Therefore its conservative cell flag
	// cannot be false. The kernel independently repairs, retargets and validates
	// the original access/egress/transfer/direct walking-role feasibility.
	return view.searchWalking(ctx, q, filter, false, true)
}
