package tripbased

import (
	"context"
	"fmt"
	"sort"
	"unsafe"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

const fixedCapRetainedPatternMethod = "fixed_cap_destination_retained_exact_events_v1"

// DestinationRetainedCappedConfig names a separate construction policy. Scope
// keeps the fresh builder's destination, four role caps and fail-closed guards.
// SourceOrder, if supplied, must be an exact permutation of all non-goal stops.
type DestinationRetainedCappedConfig struct {
	Scope              DestinationCappedPatternConfig `json:"scope"`
	MaxPendingSegments int                            `json:"max_pending_segments"`
	MaxSourceSeeds     int                            `json:"max_source_seed_events"`
	SourceOrder        []string                       `json:"source_order,omitempty"`
}

type DestinationRetainedCappedStats struct {
	SeedOffers            int `json:"seed_offers"`
	QueuedSegments        int `json:"queued_segments"`
	ProcessedSegments     int `json:"processed_segments"`
	PeakPendingSegments   int `json:"peak_pending_segments"`
	TripInitializations   int `json:"trip_initializations"`
	EventVisits           int `json:"event_visits"`
	TransferVisits        int `json:"transfer_visits"`
	TargetChecks          int `json:"target_checks"`
	ReconstructionLegs    int `json:"reconstruction_legs"`
	ProfileRecordsOmitted int `json:"profile_records_omitted"`
	PeakSeedPayloadBytes  int `json:"peak_seed_payload_bytes"`
	PeakScratchBytes      int `json:"peak_scratch_payload_bytes"`
}

// Frontier is the minimum arrival with at most b boardings, indexed by b.
// paper.Infinity means unreachable. Records are bounded output-only telemetry.
type DestinationRetainedCappedProfile struct {
	Source    string  `json:"source"`
	Departure int64   `json:"departure"`
	Frontier  []int64 `json:"frontier"`
}
type DestinationRetainedCappedSnapshot struct {
	Config   DestinationRetainedCappedConfig    `json:"config"`
	Stats    DestinationRetainedCappedStats     `json:"stats"`
	Profiles []DestinationRetainedCappedProfile `json:"profiles,omitempty"`
}

func (s *DestinationRetainedCappedSnapshot) clone() *DestinationRetainedCappedSnapshot {
	c := *s
	c.Config.SourceOrder = append([]string(nil), s.Config.SourceOrder...)
	c.Profiles = append([]DestinationRetainedCappedProfile(nil), s.Profiles...)
	for i := range c.Profiles {
		c.Profiles[i].Frontier = append([]int64(nil), s.Profiles[i].Frontier...)
	}
	return &c
}

// BuildDestinationRetainedCappedPatterns retains exact-event state only within
// one source as readiness descends. It promises arrival/boarding representative
// vectors, deterministic ties, and never strict identity top-k. It does not call
// or enable the existing unbounded retained profile policy.
func BuildDestinationRetainedCappedPatterns(ctx context.Context, d model.Dataset, c DestinationRetainedCappedConfig) (*DestinationCappedPatternIndex, error) {
	return buildDestinationCappedPatterns(ctx, d, c.Scope, &c)
}

type cappedRetainedSeed struct {
	trip, board int
	ready       int64
	walk        *paper.Path
}
type cappedRetainedPath struct {
	previous          *cappedRetainedPath
	walk              *paper.Path
	trip, board, exit int
}
type cappedRetainedSegment struct {
	previous         *cappedRetainedPath
	walk             *paper.Path
	trip, begin, end int
}
type cappedRetainedArrival struct {
	at   int64
	path *cappedRetainedPath
	walk *paper.Path
}
type cappedRetainedSweep struct {
	x       *DestinationCappedPatternIndex
	ctx     context.Context
	source  int
	reached [][]int
	queues  [][]cappedRetainedSegment
	targets []cappedRetainedArrival
	pending int
}

func (x *DestinationCappedPatternIndex) buildRetainedCappedProfiles(ctx context.Context, domain *sourceDomainBuild) error {
	b, p, c := x.p.base, x.p, x.retained.Config
	lower, upper := b.ruleWindow()
	lists := make([][]retainedSeed, len(b.names))
	for ti, t := range b.cp.trips {
		for pos, v := range t.cs {
			if e := p.tick(ctx); e != nil {
				return e
			}
			if v.Pickup && v.Arrival <= upper {
				lists[b.ids[v.From]] = append(lists[b.ids[v.From]], retainedSeed{ti, pos, v.Departure})
			}
		}
	}
	order := make([]int, 0, len(b.names)-1)
	if domain != nil {
		for _, name := range domain.origins {
			i, ok := b.ids[name]
			if !ok || name == x.cfg.Destination {
				return fmt.Errorf("source-domain TP unknown or destination source")
			}
			order = append(order, i)
		}
	} else if len(c.SourceOrder) == 0 {
		for i, name := range b.names {
			if name != x.cfg.Destination {
				order = append(order, i)
			}
		}
	} else {
		seen := map[string]bool{}
		for _, name := range c.SourceOrder {
			i, ok := b.ids[name]
			if !ok || name == x.cfg.Destination || seen[name] {
				return fmt.Errorf("fixed-cap retained TP source order must be an exact non-goal permutation")
			}
			seen[name] = true
			order = append(order, i)
		}
		if len(order) != len(b.names)-1 {
			return fmt.Errorf("fixed-cap retained TP source order omitted a source")
		}
	}
	for _, source := range order {
		if x.stats.SourcesStarted >= x.cfg.MaxProfileSources {
			return fmt.Errorf("fixed-cap retained TP incomplete: source guard")
		}
		x.stats.SourcesStarted++
		if domain != nil {
			if err := domain.start(b.names[source]); err != nil {
				return err
			}
		}
		// At the earliest readiness, certify nonbinding total walking and legs.
		// Every later feasible event sequence is also feasible then (with waiting),
		// with the same leg count and initial walking geometry.
		wl := x.cfg.Walking
		q := model.Query{ID: "fixed-cap-retained-profile-scope", Origin: b.names[source], Destination: x.cfg.Destination, Departure: lower, LatestArrival: upper, MaxBoardings: x.cfg.Patterns.Profile.MaxBoardings, MaxLegs: x.cfg.ProfileMaxLegs, MaxWalkSeconds: x.cfg.ProfileMaxWalkingSeconds, Walking: &wl}
		if e := b.cp.p.CheckContext(ctx, q); e != nil {
			return e
		}
		seeds := []cappedRetainedSeed{}
		for wi := range b.walks[source] {
			fp := &b.walks[source][wi]
			if e := p.tick(ctx); e != nil {
				return e
			}
			if fp.To == x.cfg.Destination || fp.Duration > x.cfg.Walking.Access {
				continue
			}
			for _, event := range lists[b.ids[fp.To]] {
				if e := p.tick(ctx); e != nil {
					return e
				}
				ready := event.departure - fp.Duration
				if ready < lower || ready > upper {
					continue
				}
				if len(seeds) >= c.MaxSourceSeeds {
					return fmt.Errorf("fixed-cap retained TP incomplete: source seed budget")
				}
				seeds = append(seeds, cappedRetainedSeed{event.trip, event.pos, ready, fp})
			}
		}
		sort.Slice(seeds, func(i, j int) bool {
			a, b := seeds[i], seeds[j]
			if a.ready != b.ready {
				return a.ready > b.ready
			}
			if a.trip != b.trip {
				return a.trip < b.trip
			}
			return a.board < b.board
		})
		x.retained.Stats.PeakSeedPayloadBytes = max(x.retained.Stats.PeakSeedPayloadBytes, cap(seeds)*int(unsafe.Sizeof(cappedRetainedSeed{})))
		s, e := x.newCappedRetainedSweep(ctx, source)
		if e != nil {
			return e
		}
		last := int64(-1)
		for i := 0; i < len(seeds); {
			ready := seeds[i].ready
			if e := x.offerRetainedReadiness(ctx); e != nil {
				return e
			}
			for i < len(seeds) && seeds[i].ready == ready {
				seed := seeds[i]
				x.retained.Stats.SeedOffers++
				if e := s.enqueue(1, seed.trip, seed.board, nil, seed.walk); e != nil {
					return e
				}
				i++
			}
			if e := s.propagate(); e != nil {
				return e
			}
			if e := s.emit(ready); e != nil {
				return e
			}
			if domain != nil {
				if e := domain.emitted(b.names[source], ready, lower); e != nil {
					return e
				}
			}
			last = ready
		}
		// Every source, including unreachable/no-board sources, gets the sentinel.
		if last != lower {
			if e := x.offerRetainedReadiness(ctx); e != nil {
				return e
			}
			if e := s.emit(lower); e != nil {
				return e
			}
			if domain != nil {
				if e := domain.emitted(b.names[source], lower, lower); e != nil {
					return e
				}
			}
		}
		if domain != nil {
			if e := domain.finish(b.names[source], s.pending); e != nil {
				return e
			}
		}
		x.stats.SourcesCompleted++
	}
	return nil
}

func (x *DestinationCappedPatternIndex) offerRetainedReadiness(ctx context.Context) error {
	if e := x.p.tick(ctx); e != nil {
		return e
	}
	if x.stats.OfferedReadiness >= x.cfg.MaxProfileQueries {
		return fmt.Errorf("fixed-cap retained TP incomplete: offered-readiness guard")
	}
	x.stats.OfferedReadiness++
	return nil
}

func (x *DestinationCappedPatternIndex) newCappedRetainedSweep(ctx context.Context, source int) (*cappedRetainedSweep, error) {
	k := x.cfg.Patterns.Profile.MaxBoardings
	s := &cappedRetainedSweep{x: x, ctx: ctx, source: source, reached: make([][]int, k+1), queues: make([][]cappedRetainedSegment, k+1), targets: make([]cappedRetainedArrival, k+1)}
	for n := 1; n <= k; n++ {
		s.targets[n].at = paper.Infinity
		s.reached[n] = make([]int, len(x.p.base.cp.trips))
		for ti, t := range x.p.base.cp.trips {
			if e := x.p.tick(ctx); e != nil {
				return nil, e
			}
			s.reached[n][ti] = len(t.cs)
			x.retained.Stats.TripInitializations++
		}
	}
	x.retained.Stats.PeakScratchBytes = max(x.retained.Stats.PeakScratchBytes, k*len(x.p.base.cp.trips)*int(unsafe.Sizeof(int(0)))+(k+1)*int(unsafe.Sizeof(cappedRetainedArrival{})))
	return s, nil
}

func (s *cappedRetainedSweep) enqueue(n, ti, board int, previous *cappedRetainedPath, walk *paper.Path) error {
	x := s.x
	if e := x.p.tick(s.ctx); e != nil {
		return e
	}
	old := s.reached[n][ti]
	if board >= old {
		return nil
	}
	if s.pending >= x.retained.Config.MaxPendingSegments {
		return fmt.Errorf("fixed-cap retained TP incomplete: pending segment budget")
	}
	if x.retained.Stats.QueuedSegments >= x.cfg.MaxProfileSegments {
		return fmt.Errorf("fixed-cap retained TP incomplete: cumulative segment guard")
	}
	// Only this exact trip is changed. No later-trip labels or parents propagate.
	s.reached[n][ti] = board
	s.queues[n] = append(s.queues[n], cappedRetainedSegment{previous, walk, ti, board, old})
	s.pending++
	x.retained.Stats.QueuedSegments++
	x.retained.Stats.PeakPendingSegments = max(x.retained.Stats.PeakPendingSegments, s.pending)
	return nil
}

func (s *cappedRetainedSweep) propagate() error {
	x, b := s.x, s.x.p.base
	_, upper := b.ruleWindow()
	k := x.cfg.Patterns.Profile.MaxBoardings
	for n := 1; n <= k; n++ {
		for head := 0; head < len(s.queues[n]); head++ {
			seg := s.queues[n][head]
			s.queues[n][head] = cappedRetainedSegment{}
			s.pending--
			x.retained.Stats.ProcessedSegments++
			t := b.cp.trips[seg.trip]
			for i := seg.begin; i < seg.end; i++ {
				if e := x.p.tick(s.ctx); e != nil {
					return e
				}
				x.retained.Stats.EventVisits++
				c := t.cs[i]
				if c.Arrival > upper {
					break
				}
				if !c.Dropoff {
					continue
				}
				var path *cappedRetainedPath
				parent := func() *cappedRetainedPath {
					if path == nil {
						path = &cappedRetainedPath{seg.previous, seg.walk, seg.trip, seg.begin, i + 1}
					}
					return path
				}
				if c.To == x.cfg.Destination {
					if c.Arrival < s.targets[n].at {
						s.targets[n] = cappedRetainedArrival{c.Arrival, parent(), nil}
					}
					break // Legal physical destination arrival is terminal.
				}
				if fp := b.walkMap[b.ids[c.To]][b.ids[x.cfg.Destination]]; fp != nil && fp.Duration <= x.cfg.Walking.Egress && c.Arrival+fp.Duration <= upper && c.Arrival+fp.Duration < s.targets[n].at {
					s.targets[n] = cappedRetainedArrival{c.Arrival + fp.Duration, parent(), fp}
				}
				if n == k {
					continue
				}
				for _, tr := range b.cp.links[seg.trip][i] {
					if e := x.p.tick(s.ctx); e != nil {
						return e
					}
					x.retained.Stats.TransferVisits++
					if tr.walk.To == x.cfg.Destination || tr.walk.Duration > x.cfg.Walking.Transfer || b.cp.trips[tr.trip].cs[tr.board].Arrival > upper {
						continue
					}
					if tr.board >= s.reached[n+1][tr.trip] {
						continue
					}
					if e := s.enqueue(n+1, tr.trip, tr.board, parent(), tr.walk); e != nil {
						return e
					}
				}
			}
		}
		s.queues[n] = s.queues[n][:0]
	}
	return nil
}

func (s *cappedRetainedSweep) unpack(path *cappedRetainedPath, final *paper.Path, departure int64) (model.Journey, error) {
	chain := []*cappedRetainedPath{}
	for p := path; p != nil; p = p.previous {
		if e := s.x.p.tick(s.ctx); e != nil {
			return model.Journey{}, e
		}
		chain = append(chain, p)
	}
	legs := []model.Leg{}
	at := departure
	walk := func(fp *paper.Path) error {
		if fp != nil {
			for _, w := range fp.Edges {
				if e := s.x.p.tick(s.ctx); e != nil {
					return e
				}
				legs = append(legs, model.Leg{Kind: "walk", Ref: w.ID, Departure: at, Arrival: at + w.Duration})
				at += w.Duration
				s.x.retained.Stats.ReconstructionLegs++
			}
		}
		return nil
	}
	for i := len(chain) - 1; i >= 0; i-- {
		p := chain[i]
		if e := walk(p.walk); e != nil {
			return model.Journey{}, e
		}
		for j := p.board; j < p.exit; j++ {
			if e := s.x.p.tick(s.ctx); e != nil {
				return model.Journey{}, e
			}
			c := s.x.p.base.cp.trips[p.trip].cs[j]
			legs = append(legs, model.Leg{Kind: "ride", Ref: c.ID, Departure: c.Departure, Arrival: c.Arrival})
			at = c.Arrival
			s.x.retained.Stats.ReconstructionLegs++
		}
	}
	if e := walk(final); e != nil {
		return model.Journey{}, e
	}
	return model.Journey{Legs: legs}, nil
}

func (s *cappedRetainedSweep) emit(departure int64) error {
	x, b := s.x, s.x.p.base
	_, upper := b.ruleWindow()
	best := paper.Infinity
	if direct := b.walkMap[s.source][b.ids[x.cfg.Destination]]; direct != nil && direct.Duration <= x.cfg.Walking.Direct && departure+direct.Duration <= upper {
		best = departure + direct.Duration
		j, e := s.unpack(nil, direct, departure)
		if e != nil {
			return e
		}
		x.stats.ProfileJourneys++
		x.p.snap.Stats.ProfileJourneys++
		if e = x.p.addPattern(s.ctx, b.names[s.source], x.cfg.Destination, departure, j); e != nil {
			return e
		}
	}
	frontier := make([]int64, len(s.targets))
	frontier[0] = best
	for n := 1; n < len(s.targets); n++ {
		if e := x.p.tick(s.ctx); e != nil {
			return e
		}
		x.retained.Stats.TargetChecks++
		a := s.targets[n]
		if a.at < best {
			best = a.at
			j, e := s.unpack(a.path, a.walk, departure)
			if e != nil {
				return e
			}
			x.stats.ProfileJourneys++
			x.p.snap.Stats.ProfileJourneys++
			if e = x.p.addPattern(s.ctx, b.names[s.source], x.cfg.Destination, departure, j); e != nil {
				return e
			}
		}
		frontier[n] = best
	}
	if len(x.retained.Profiles) < x.cfg.MaxProfileRecords {
		x.retained.Profiles = append(x.retained.Profiles, DestinationRetainedCappedProfile{b.names[s.source], departure, frontier})
	} else {
		x.retained.Stats.ProfileRecordsOmitted++
	}
	return nil
}
