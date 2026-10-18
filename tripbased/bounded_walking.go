package tripbased

import (
	"context"
	"fmt"

	"github.com/ertyoii/transit-routing-go/model"
)

// BoundedWalkingTBIndex is a destination-conditioned, unranked Trip-Based
// accelerator. It does not apply T-REX ranks to a nontransitive capped metric.
// Reuse it across origins, departures and role caps for the same destination.
type BoundedWalkingTBIndex struct {
	x                    *TREXIndex
	destination          string
	removed              int
	transferWalkingLimit int64
}

type BoundedWalkingTBSnapshot struct {
	Profile              string       `json:"profile"`
	Destination          string       `json:"destination"`
	RemovedOutgoingWalks int          `json:"removed_outgoing_walks"`
	TransferWalkingLimit int64        `json:"transfer_walking_limit"`
	Index                TREXSnapshot `json:"index"`
}

func (x *BoundedWalkingTBIndex) Complete() bool { return x != nil && x.x.Complete() }
func (x *BoundedWalkingTBIndex) Snapshot() BoundedWalkingTBSnapshot {
	s := x.x.Snapshot()
	s.Profile = "destination_conditioned_capped_walking_tb_v1"
	return BoundedWalkingTBSnapshot{s.Profile, x.destination, x.removed, x.transferWalkingLimit, s}
}

// BuildBoundedWalkingTB retains the complete positive walking metric, but makes
// destination absorbing before computing it. This preserves detours that avoid
// destination when the unrestricted shortest path passes through destination.
// cfg must explicitly select level zero with no supplied partition. All existing
// preparation limits and fail-closed resource checks remain in force.
func BuildBoundedWalkingTB(ctx context.Context, d model.Dataset, destination string, transferWalkingLimit int64, cfg TREXConfig) (*BoundedWalkingTBIndex, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if transferWalkingLimit < 0 || transferWalkingLimit > model.MaxServiceSecond {
		return nil, fmt.Errorf("bounded walking TB: invalid prepared transfer walking limit")
	}
	if cfg.Levels != 0 || len(cfg.Partition) != 0 {
		return nil, fmt.Errorf("bounded walking TB requires level zero and no partition; ranked T-REX cap preservation is unproved")
	}
	if e := d.Validate(); e != nil {
		return nil, e
	}
	found := false
	for _, s := range d.Stops {
		if s.ID == destination {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("bounded walking TB: unknown destination %q", destination)
	}
	// Validate raw input before removing absorbing edges, including positivity.
	if len(d.Stops) > cfg.Limits.MaxStops || len(d.Trips) > cfg.Limits.MaxTrips || len(d.Connections)+len(d.Footpaths) > cfg.Limits.MaxInputEdges {
		return nil, fmt.Errorf("bounded walking TB incomplete: raw input size budget exhausted")
	}
	d = d.Clone()
	kept := d.Footpaths[:0]
	removed := 0
	for _, w := range d.Footpaths {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		if w.Duration <= 0 {
			return nil, fmt.Errorf("bounded walking TB requires positive walking edges")
		}
		if w.From == destination {
			removed++
			continue
		}
		kept = append(kept, w)
	}
	d.Footpaths = kept
	x, e := buildTREX(ctx, d, cfg, &transferWalkingLimit)
	if x == nil {
		return nil, e
	}
	return &BoundedWalkingTBIndex{x, destination, removed, transferWalkingLimit}, e
}

func (x *BoundedWalkingTBIndex) Search(ctx context.Context, q model.Query) (TREXResult, error) {
	if e := ctx.Err(); e != nil {
		return TREXResult{Error: e.Error()}, e
	}
	if x == nil || x.x == nil {
		e := fmt.Errorf("bounded walking TB: nil index")
		return TREXResult{Error: e.Error()}, e
	}
	if q.Destination != x.destination {
		e := fmt.Errorf("bounded walking TB: destination differs from prepared index")
		return TREXResult{Error: e.Error()}, e
	}
	if q.Walking != nil && q.Walking.Transfer > x.transferWalkingLimit {
		e := fmt.Errorf("bounded walking TB: query transfer cap exceeds prepared limit")
		return TREXResult{Error: e.Error()}, e
	}
	return x.x.searchTREX(ctx, q, "bounded-walking-tb")
}
