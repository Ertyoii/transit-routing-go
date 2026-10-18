// Package tripbased implements the trip-segment queues and precomputed earliest
// line transfers of Witt (2015), Algorithms 1 and 4. Optional transfer reductions
// and profile queries are deliberately not claimed by this initial port.
package tripbased

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type Config struct {
	StrictLineOrder      bool  `json:"strict_line_order,omitempty"`
	TransferWalkingLimit int64 `json:"transfer_walking_limit"`
	TransferBuffer       int64 `json:"transfer_buffer"`
	AccessibleOnly       bool  `json:"accessible_only"`
	MaxTransfers         int   `json:"max_precomputed_transfers"`
	MaxQueuedSegments    int   `json:"max_queued_segments"`
}
type Preparation struct {
	Trips             int `json:"trips"`
	Patterns          int `json:"patterns"`
	Lines             int `json:"non_overtaking_lines"`
	Transfers         int `json:"precomputed_transfers"`
	SameLineDiscarded int `json:"same_line_transfers_discarded"`
	WalkingClosures   int `json:"walking_closures"`
	WalkingPaths      int `json:"walking_paths"`
}
type Capability struct {
	paper.Capability
	Contract                      string `json:"output_contract"`
	RequiresEgressAtLeastTransfer bool   `json:"requires_egress_at_least_transfer"`
	Config                        Config `json:"preparation_config"`
}
type trip struct {
	id         string
	cs         []model.Connection
	line, rank int
}
type line struct{ trips []int }
type occurrence struct{ line, position int }
type link struct {
	trip, board int
	walk        *paper.Path
}
type Index struct {
	p     *paper.Profile
	cfg   Config
	trips []trip
	lines []line
	at    map[string][]occurrence
	links [][][]link
	prep  Preparation
}

func (x *Index) Preparation() Preparation { return x.prep }
func (x *Index) Capabilities() Capability {
	return Capability{x.p.Capability, "arrival_boardings_pareto_representatives", true, x.cfg}
}
func New(ctx context.Context, d model.Dataset, cfg Config) (*Index, error) {
	return newIndex(ctx, d, cfg, true)
}

// Private direct-timetable infrastructure is never exposed as a TB router.
func newIndex(ctx context.Context, d model.Dataset, cfg Config, transfers bool) (*Index, error) {
	if cfg.TransferWalkingLimit < 0 || cfg.TransferWalkingLimit > model.MaxServiceSecond || cfg.TransferBuffer < 0 || cfg.TransferBuffer > model.MaxServiceSecond || cfg.MaxTransfers <= 0 || cfg.MaxQueuedSegments <= 0 {
		return nil, fmt.Errorf("Trip-Based: invalid preparation/budget configuration")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := paper.New(d)
	if err != nil {
		return nil, err
	}
	x := &Index{p: p, cfg: cfg, at: map[string][]occurrence{}}
	chains := map[string][]model.Connection{}
	for _, c := range p.Data.Connections {
		chains[c.TripID] = append(chains[c.TripID], c)
	}
	ids := []string{}
	for id := range chains {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	patterns := map[string][]int{}
	for _, id := range ids {
		cs := chains[id]
		sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
		type segment struct {
			From, To        string
			Pickup, Dropoff bool
		}
		signature := struct {
			Segments   []segment
			Accessible model.Access
		}{Accessible: p.Trips[id].Accessible}
		for _, c := range cs {
			signature.Segments = append(signature.Segments, segment{c.From, c.To, c.Pickup, c.Dropoff})
		}
		key, _ := json.Marshal(signature)
		patterns[string(key)] = append(patterns[string(key)], len(x.trips))
		x.trips = append(x.trips, trip{id: id, cs: cs})
	}
	keys := []string{}
	for key := range patterns {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ts := patterns[key]
		sort.Slice(ts, func(i, j int) bool {
			a, b := x.trips[ts[i]], x.trips[ts[j]]
			if a.cs[0].Departure != b.cs[0].Departure {
				return a.cs[0].Departure < b.cs[0].Departure
			}
			return a.id < b.id
		})
		seenStops := map[string]bool{}
		repeated := false
		if len(ts) > 0 {
			cs := x.trips[ts[0]].cs
			seenStops[cs[0].From] = true
			for _, c := range cs {
				if seenStops[c.To] {
					repeated = true
				}
				seenStops[c.To] = true
			}
		}
		groups := []line{}
		for _, ti := range ts {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			placed := false
			for g := range groups {
				if cfg.StrictLineOrder && repeated {
					break
				}
				prior := x.trips[groups[g].trips[len(groups[g].trips)-1]]
				ordered := true
				for i, c := range x.trips[ti].cs {
					if c.Departure < prior.cs[i].Departure || c.Arrival < prior.cs[i].Arrival || cfg.StrictLineOrder && (c.Departure == prior.cs[i].Departure || c.Arrival == prior.cs[i].Arrival) {
						ordered = false
						break
					}
				}
				if ordered {
					groups[g].trips = append(groups[g].trips, ti)
					placed = true
					break
				}
			}
			if !placed {
				groups = append(groups, line{trips: []int{ti}})
			}
		}
		x.lines = append(x.lines, groups...)
	}
	for li, l := range x.lines {
		for rank, ti := range l.trips {
			x.trips[ti].line = li
			x.trips[ti].rank = rank
		}
		for i, c := range x.trips[l.trips[0]].cs {
			x.at[c.From] = append(x.at[c.From], occurrence{li, i})
		}
	}
	x.prep = Preparation{Trips: len(x.trips), Patterns: len(patterns), Lines: len(x.lines)}
	if !transfers {
		return x, nil
	}
	x.links = make([][][]link, len(x.trips))
	// Query-independent closure: no destination is absorbing during preparation.
	// Search requires egress >= intermediate walk so a path passing its destination
	// is dominated by terminating there. The destination is never used as transit.
	prepQuery := model.Query{AccessibleOnly: cfg.AccessibleOnly, TransferBuffer: cfg.TransferBuffer, Walking: &model.WalkingLimits{Transfer: cfg.TransferWalkingLimit}}
	walker := p.Walker(prepQuery)
	for ti, t := range x.trips {
		x.links[ti] = make([][]link, len(t.cs))
		if !p.TripAllowed(prepQuery, t.id) {
			continue
		}
		for i, c := range t.cs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !c.Dropoff || !p.Accessible(prepQuery, c.To) {
				continue
			}
			paths, err := walker.Paths(ctx, c.To)
			if err != nil {
				return nil, err
			}
			for wi := range paths {
				w := &paths[wi]
				ready, allowed, err := p.ReadyTime(prepQuery, c.To, w.To, c.Arrival, w.Duration)
				if err != nil {
					return nil, err
				}
				if !allowed {
					continue
				}
				for _, o := range x.at[w.To] {
					l := x.lines[o.line]
					prototype := x.trips[l.trips[0]]
					if !prototype.cs[o.position].Pickup || !p.TripAllowed(prepQuery, prototype.id) {
						continue
					}
					rank := sort.Search(len(l.trips), func(r int) bool { return x.trips[l.trips[r]].cs[o.position].Departure >= ready })
					if rank == len(l.trips) {
						continue
					}
					u := l.trips[rank]
					// i+1 is the alighting stop index; o.position is the boarding stop index.
					// Equal-time earlier identities may retain extra transfers, never remove more.
					if u == ti || o.line == t.line && rank >= t.rank && o.position >= i+1 {
						x.prep.SameLineDiscarded++
						continue
					}
					if x.prep.Transfers == cfg.MaxTransfers {
						return nil, fmt.Errorf("Trip-Based preprocessing incomplete: transfer budget %d exhausted", cfg.MaxTransfers)
					}
					x.links[ti][i] = append(x.links[ti][i], link{u, o.position, w})
					x.prep.Transfers++
				}
			}
		}
	}
	x.prep.WalkingClosures = walker.Count
	// Counting cached path endpoints does not expose or share the search cache.
	for _, s := range p.Data.Stops {
		if !p.Accessible(prepQuery, s.ID) {
			continue
		}
		paths, err := walker.Paths(ctx, s.ID)
		if err != nil {
			return nil, err
		}
		x.prep.WalkingPaths += len(paths)
	}
	x.prep.WalkingClosures = walker.Count
	return x, nil
}
