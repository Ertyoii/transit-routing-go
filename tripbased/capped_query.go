package tripbased

import (
	"context"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// SearchCappedWalking preserves arrival/boarding Pareto representatives while
// allowing independently binding walking-role caps. It reuses one ordinary TB
// index across destinations and repairs only destination-crossing transfers.
// Pickup/dropoff and static stop-pair rules retain the ordinary TB contract.
// Binding aggregate walking/leg resources, trip/route-specific rules, 3D walking
// Pareto completeness, every identity and ranked T-REX are not supported here.
func (x *Index) SearchCappedWalking(ctx context.Context, q model.Query) (Result, error) {
	return x.searchWalking(ctx, q, nil, false, true)
}

// CappedWalkingCapabilities describes SearchCappedWalking, not legacy Search.
func (x *Index) CappedWalkingCapabilities() Capability {
	c := x.Capabilities()
	c.RequiresEgressAtLeastTransfer = false
	return c
}

// Paths from paper.Walker are sorted lexically by To, including after absorbing
// destination filtering. The returned pointer borrows query-local immutable data.
func endpointPath(paths []paper.Path, target string) *paper.Path {
	i := sort.Search(len(paths), func(i int) bool { return paths[i].To >= target })
	if i < len(paths) && paths[i].To == target {
		return &paths[i]
	}
	return nil
}

type destinationWalk struct {
	path    *paper.Path
	changed bool
}

func (x *Index) repairDestinationTransfer(q model.Query, c model.Connection, tr link, avoiding []paper.Path, cache map[*paper.Path]destinationWalk, stats *Stats) (link, bool, error) {
	choice, exists := cache[tr.walk]
	if !exists {
		stats.DestinationWalkChecks++
		choice.path = tr.walk
		for _, edge := range tr.walk.Edges {
			if edge.To == q.Destination {
				choice.changed = true
				choice.path = nil
				// A transfer cannot board at destination. Otherwise the query's
				// absorbing closure may contain a longer valid avoiding detour.
				if tr.walk.To != q.Destination {
					choice.path = endpointPath(avoiding, tr.walk.To)
					if choice.path != nil && choice.path.Duration > q.Walking.Transfer {
						choice.path = nil
					}
				}
				break
			}
		}
		cache[tr.walk] = choice
	}
	if choice.path == nil {
		return link{}, false, nil
	}
	if !choice.changed {
		return tr, true, nil
	}
	stats.DestinationWalkRepairs++
	oldDuration := tr.walk.Duration
	tr.walk = choice.path
	if oldDuration == tr.walk.Duration {
		return tr, true, nil
	}
	ready, allowed, err := x.p.ReadyTime(q, c.To, tr.walk.To, c.Arrival, tr.walk.Duration)
	if err != nil || !allowed || ready > q.LatestArrival {
		return link{}, false, err
	}
	target := x.trips[tr.trip]
	line := x.lines[target.line]
	// Avoiding destination cannot shorten an unrestricted shortest walk, so
	// the newly earliest feasible trip is at or after the precomputed trip.
	stats.RepairTripSearches++
	rank := target.rank + sort.Search(len(line.trips)-target.rank, func(i int) bool {
		return x.trips[line.trips[target.rank+i]].cs[tr.board].Departure >= ready
	})
	if rank == len(line.trips) {
		return link{}, false, nil
	}
	tr.trip = line.trips[rank]
	if x.trips[tr.trip].cs[tr.board].Arrival > q.LatestArrival {
		return link{}, false, nil
	}
	return tr, true, nil
}
