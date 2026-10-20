package tripbased

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
	"time"
)

const fixedCapPatternMethod = "experimental_tp_destination_fixed_role_caps_native_profiles_v1"

// DestinationCappedPatternConfig scopes the entire pattern union. It is not a
// query-time relaxation of ordinary TP's nonbinding-walking capability guard.
// Profiles use native capped TB; queries use only the resulting TP graph.
type DestinationCappedPatternConfig struct {
	Patterns                 PatternConfig       `json:"patterns"`
	Destination              string              `json:"destination"`
	Walking                  model.WalkingLimits `json:"walking_limits"`
	ProfileMaxLegs           int                 `json:"profile_max_legs"`
	ProfileMaxWalkingSeconds int64               `json:"profile_max_walking_seconds"`
	MaxProfileRecords        int                 `json:"max_profile_records,omitempty"`
	MaxProfileSources        int                 `json:"max_profile_sources"`
	MaxProfileQueries        int                 `json:"max_profile_queries"`
	MaxProfileSegments       int                 `json:"max_profile_queued_segments"`
}

// DestinationCappedProfileWork describes one native profile call, including a
// partial call that returned an error. It is telemetry, never a pruning input.
type DestinationCappedProfileWork struct {
	Source    string `json:"source"`
	Departure int64  `json:"departure"`
	Complete  bool   `json:"complete"`
	Error     string `json:"error,omitempty"`
	Stats     Stats  `json:"stats"`
	ElapsedNS int64  `json:"elapsed_ns"`
}
type DestinationCappedPatternStats struct {
	NativeCompleted       int                          `json:"native_profiles_completed"`
	NativeFailed          int                          `json:"native_profiles_failed"`
	NativeWork            Stats                        `json:"native_work"`
	NativeElapsedNS       int64                        `json:"native_elapsed_ns"`
	ProfileRecordsOmitted int                          `json:"profile_records_omitted"`
	LastProfile           DestinationCappedProfileWork `json:"last_profile"`
	MaxTransferProfile    DestinationCappedProfileWork `json:"max_transfer_profile"`

	SourcesStarted       int `json:"sources_started"`
	SourcesCompleted     int `json:"sources_completed"`
	OfferedReadiness     int `json:"offered_readiness_so_far"`
	NativeProfileQueries int `json:"native_profile_queries"`
	NativeQueuedSegments int `json:"native_profile_queued_segments"`
	ProfileJourneys      int `json:"profile_journeys"`
}
type DestinationCappedPatternSnapshot struct {
	ProfileWork          []DestinationCappedProfileWork `json:"profile_work,omitempty"`
	Method               string                         `json:"method"`
	SourceDatasetID      string                         `json:"source_dataset_id"`
	ConditionedDatasetID string                         `json:"conditioned_dataset_id"`
	RemovedOutgoingWalks int                            `json:"removed_outgoing_destination_walks"`
	Complete             bool                           `json:"complete"`
	Error                string                         `json:"error,omitempty"`
	Config               DestinationCappedPatternConfig `json:"config"`
	Stats                DestinationCappedPatternStats  `json:"stats"`
	Patterns             PatternSnapshot                `json:"patterns"`
}
type DestinationCappedPatternIndex struct {
	p                       *PatternIndex
	cfg                     DestinationCappedPatternConfig
	sourceID, conditionedID string
	removed                 int
	complete                bool
	err                     string
	stats                   DestinationCappedPatternStats
	profileWork             []DestinationCappedProfileWork
}

func (x *DestinationCappedPatternIndex) Complete() bool { return x != nil && x.complete }
func (x *DestinationCappedPatternIndex) Snapshot() DestinationCappedPatternSnapshot {
	if x == nil {
		return DestinationCappedPatternSnapshot{Method: fixedCapPatternMethod}
	}
	s := DestinationCappedPatternSnapshot{Method: fixedCapPatternMethod, SourceDatasetID: x.sourceID, ConditionedDatasetID: x.conditionedID, RemovedOutgoingWalks: x.removed, Complete: x.complete, Error: x.err, Config: x.cfg, Stats: x.stats, ProfileWork: append([]DestinationCappedProfileWork(nil), x.profileWork...)}
	if x.p != nil {
		s.Patterns = x.p.Snapshot()
	}
	return s
}
func (x *DestinationCappedPatternIndex) CostSummary() json.RawMessage {
	if x == nil {
		return json.RawMessage("null")
	}
	v := map[string]any{"method": fixedCapPatternMethod, "source_dataset_id": x.sourceID, "conditioned_dataset_id": x.conditionedID, "removed_outgoing_destination_walks": x.removed, "complete": x.complete, "error": x.err, "config": x.cfg, "stats": x.stats}
	if x.p != nil {
		v["patterns"] = x.p.CostSummary()
	}
	b, _ := json.Marshal(v)
	return b
}

// BuildDestinationCappedPatterns is an explicit correctness-first experimental
// construction: one immutable index per destination and exact role-cap class.
// It deliberately does not reuse unbounded optimal patterns or claim retained
// v5 preprocessing. Every source and exact feasible access breakpoint is
// profiled using the native role-capped TB kernel, with fail-closed budgets.
func BuildDestinationCappedPatterns(ctx context.Context, d model.Dataset, c DestinationCappedPatternConfig) (x *DestinationCappedPatternIndex, err error) {
	x = &DestinationCappedPatternIndex{cfg: c, sourceID: d.ID}
	p := &PatternIndex{cfg: c.Patterns, dags: map[string]*PatternDAG{}, prefix: map[string]map[patternPrefix]int{}, seen: map[string]bool{}, connections: map[string]model.Connection{}, footpaths: map[string]model.Footpath{}, snap: PatternSnapshot{Profile: fixedCapPatternMethod, Config: c.Patterns, Witnesses: []PatternWitness{}, PhasesNS: map[string]int64{}}}
	x.p = p
	defer func() {
		x.complete = err == nil
		p.snap.Complete = x.complete
		if err != nil {
			x.err = err.Error()
			p.snap.Error = x.err
		}
		p.prefix = nil
		p.seen = nil
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	pc, fc := c.Patterns, c.Patterns.Profile
	if fc.Semantics != "static_stop_pair_v1" || fc.ProfilePolicy != "" || fc.WalkingPolicy != "" || fc.RetainedSourceStart != "" || fc.Cells != 1 {
		err = fmt.Errorf("fixed-cap TP requires its own static-stop-pair query-profile construction, one cell and no other profile/walking policy")
		return
	}
	if c.MaxProfileRecords < 0 || c.MaxProfileSources < 1 || c.MaxProfileQueries < 1 || c.MaxProfileSegments < 1 || c.ProfileMaxLegs < fc.MaxBoardings || c.ProfileMaxLegs > 1024 || pc.MaxPatterns < 1 || pc.MaxNodes < 1 || pc.MaxEdges < 1 || pc.MaxDirectEntries < 1 || pc.MaxBuildWork < 1 || pc.MaxQueryWork < 1 || pc.MaxQueryLabels < 1 {
		err = fmt.Errorf("fixed-cap TP requires positive explicit profile/pattern budgets and legal profile leg limit")
		return
	}
	for _, v := range []int64{c.Walking.Access, c.Walking.Egress, c.Walking.Transfer, c.Walking.Direct} {
		if v < 0 || v > model.MaxServiceSecond {
			err = fmt.Errorf("fixed-cap TP invalid walking class")
			return
		}
	}
	bound := c.Walking.Direct
	if fc.MaxBoardings > 0 {
		bound = max(bound, c.Walking.Access+c.Walking.Egress+int64(fc.MaxBoardings-1)*c.Walking.Transfer)
	}
	if c.ProfileMaxWalkingSeconds < bound || c.ProfileMaxWalkingSeconds > model.MaxServiceSecond {
		err = fmt.Errorf("fixed-cap TP profile total walking must be nonbinding")
		return
	}
	if err = d.Validate(); err != nil {
		return
	}
	found := false
	for _, s := range d.Stops {
		if s.ID == c.Destination {
			found = true
		}
	}
	if !found {
		err = fmt.Errorf("fixed-cap TP unknown destination")
		return
	}
	if len(d.Stops) > fc.Limits.MaxStops || len(d.Trips) > fc.Limits.MaxTrips {
		err = fmt.Errorf("fixed-cap TP incomplete: raw network size guard")
		return
	}
	// Physical arrival at destination is terminal under the itinerary contract.
	// Remove its outgoing walking edges BEFORE computing the metric. This keeps
	// longer valid avoiding detours; truncating an unconditioned path is unsafe.
	d = d.Clone()
	kept := d.Footpaths[:0]
	for _, w := range d.Footpaths {
		if err = ctx.Err(); err != nil {
			return
		}
		if w.From == c.Destination {
			x.removed++
			continue
		}
		kept = append(kept, w)
	}
	d.Footpaths = kept
	d.ID = fmt.Sprintf("%s::tp-absorbing-%x", x.sourceID, sha256.Sum256([]byte(c.Destination)))
	x.conditionedID = d.ID
	start := time.Now()
	p.base, err = BuildFlash(ctx, d, fc) // complete base links; no all-source profiles
	p.snap.PhasesNS["conditioned_base_graph"] = time.Since(start).Nanoseconds()
	if err != nil {
		return
	}
	for _, v := range d.Connections {
		if err = p.tick(ctx); err != nil {
			return
		}
		p.connections[v.ID] = v
	}
	for _, v := range d.Footpaths {
		if err = p.tick(ctx); err != nil {
			return
		}
		p.footpaths[v.ID] = v
	}
	start = time.Now()
	err = x.buildProfiles(ctx)
	p.snap.PhasesNS["fixed_cap_native_query_profiles"] = time.Since(start).Nanoseconds()
	if err != nil {
		return
	}
	start = time.Now()
	p.incident = make([][]patternOccurrence, len(p.base.names))
	for li, line := range p.base.cp.lines {
		t := p.base.cp.trips[line.trips[0]]
		for pos := 0; pos <= len(t.cs); pos++ {
			if err = p.tick(ctx); err != nil {
				return
			}
			stop := t.cs[0].From
			if pos > 0 {
				stop = t.cs[pos-1].To
			}
			p.incident[p.base.ids[stop]] = append(p.incident[p.base.ids[stop]], patternOccurrence{li, pos})
			if p.snap.Stats.DirectEntries > pc.MaxDirectEntries-len(line.trips) {
				err = fmt.Errorf("fixed-cap TP incomplete: direct incidence budget")
				return
			}
			p.snap.Stats.DirectEntries += len(line.trips)
		}
	}
	p.snap.PhasesNS["direct_incidence"] = time.Since(start).Nanoseconds()
	return
}

func (x *DestinationCappedPatternIndex) buildProfiles(ctx context.Context) error {
	p := x.p
	b := p.base
	lower, upper := b.ruleWindow()
	departures := make([][]int64, len(b.names))
	for _, t := range b.cp.trips {
		for _, c := range t.cs {
			if e := p.tick(ctx); e != nil {
				return e
			}
			if c.Pickup && c.Arrival <= upper {
				v := b.ids[c.From]
				departures[v] = append(departures[v], c.Departure)
			}
		}
	}
	for source, name := range b.names {
		if name == x.cfg.Destination {
			continue
		}
		if x.stats.SourcesStarted >= x.cfg.MaxProfileSources {
			return fmt.Errorf("fixed-cap TP incomplete: source guard")
		}
		x.stats.SourcesStarted++
		points := map[int64]bool{}
		offer := func(at int64) error {
			if at < lower || at > upper || points[at] {
				return nil
			}
			if x.stats.OfferedReadiness >= x.cfg.MaxProfileQueries {
				return fmt.Errorf("fixed-cap TP incomplete: offered-readiness guard")
			}
			points[at] = true
			x.stats.OfferedReadiness++
			return nil
		}
		if e := offer(lower); e != nil {
			return e
		} // covers walking-only/no-board sources
		for _, fp := range b.walks[source] {
			if e := p.tick(ctx); e != nil {
				return e
			}
			if fp.To == x.cfg.Destination || fp.Duration > x.cfg.Walking.Access {
				continue
			}
			for _, at := range departures[b.ids[fp.To]] {
				if e := p.tick(ctx); e != nil {
					return e
				}
				if e := offer(at - fp.Duration); e != nil {
					return e
				}
			}
		}
		times := make([]int64, 0, len(points))
		for at := range points {
			times = append(times, at)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] > times[j] })
		for _, at := range times {
			if e := p.tick(ctx); e != nil {
				return e
			}
			remaining := x.cfg.MaxProfileSegments - x.stats.NativeQueuedSegments
			if remaining <= 0 {
				return fmt.Errorf("fixed-cap TP incomplete: native profile segment guard")
			}
			view := *b.base
			view.cfg.TransferWalkingLimit = model.MaxServiceSecond // COMPLETE metric, not a radius truncation
			view.cfg.MaxQueuedSegments = min(view.cfg.MaxQueuedSegments, remaining)
			wl := x.cfg.Walking
			q := model.Query{ID: "fixed-cap-tp-profile", Origin: name, Destination: x.cfg.Destination, Departure: at, LatestArrival: upper, MaxBoardings: p.cfg.Profile.MaxBoardings, MaxLegs: x.cfg.ProfileMaxLegs, MaxWalkSeconds: x.cfg.ProfileMaxWalkingSeconds, Walking: &wl}
			x.stats.NativeProfileQueries++
			profileStart := time.Now()
			r, e := view.SearchCappedWalking(ctx, q)
			x.recordProfile(name, at, r, e, time.Since(profileStart).Nanoseconds())
			x.stats.NativeQueuedSegments += r.Stats.QueuedSegments
			if e != nil {
				return fmt.Errorf("fixed-cap TP profile %s at %d: %w", name, at, e)
			}
			for _, j := range r.Journeys {
				x.stats.ProfileJourneys++
				p.snap.Stats.ProfileJourneys++
				if e := p.addPattern(ctx, name, x.cfg.Destination, at, j); e != nil {
					return e
				}
			}
		}
		x.stats.SourcesCompleted++
	}
	return nil
}

func (x *DestinationCappedPatternIndex) checkQuery(ctx context.Context, q model.Query) (model.Query, error) {
	if e := ctx.Err(); e != nil {
		return q, e
	}
	if q.Destination != x.cfg.Destination || q.Walking == nil || *q.Walking != x.cfg.Walking {
		return q, fmt.Errorf("fixed-cap TP capability: destination/walking class differs from prepared scope")
	}
	if q.TransferBuffer != 0 || q.AccessibleOnly || q.MaxBoardings > x.cfg.Patterns.Profile.MaxBoardings {
		return q, fmt.Errorf("fixed-cap TP capability: buffer/accessibility/boarding scope mismatch")
	}
	lower, upper := x.p.base.ruleWindow()
	if q.Departure < lower || q.LatestArrival > upper {
		return q, fmt.Errorf("fixed-cap TP capability: query outside prepared profile window")
	}
	if e := x.p.base.cp.p.CheckContext(ctx, q); e != nil {
		return q, e
	}
	return q, nil
}
func (x *DestinationCappedPatternIndex) Search(ctx context.Context, q model.Query) (PatternResult, error) {
	if !x.Complete() {
		e := fmt.Errorf("fixed-cap TP incomplete index cannot answer queries")
		return PatternResult{Journeys: []model.Journey{}, Graph: []PatternEdge{}, Error: e.Error()}, e
	}
	return x.p.searchGraphChecked(ctx, q, nil, x.checkQuery)
}

// recordProfile owns bounded output-only telemetry. Search and all construction
// guards continue using their existing counters; a record limit cannot truncate
// construction. Snapshot owns a copy so consumers cannot mutate index evidence.
func (x *DestinationCappedPatternIndex) recordProfile(source string, at int64, r Result, err error, elapsed int64) {
	w := DestinationCappedProfileWork{Source: source, Departure: at, Complete: r.Complete && err == nil, Stats: r.Stats, ElapsedNS: elapsed}
	if err != nil {
		w.Error = err.Error()
	}
	if w.Complete {
		x.stats.NativeCompleted++
	} else {
		x.stats.NativeFailed++
	}
	x.stats.NativeElapsedNS += elapsed
	a, b := &x.stats.NativeWork, r.Stats
	a.Rounds += b.Rounds
	a.QueuedSegments += b.QueuedSegments
	a.TripSegments += b.TripSegments
	a.Connections += b.Connections
	a.Transfers += b.Transfers
	a.LaterTripLabels += b.LaterTripLabels
	a.ArrivalPruned += b.ArrivalPruned
	a.WalkClosures += b.WalkClosures
	a.FlagRejected += b.FlagRejected
	a.DestinationWalkChecks += b.DestinationWalkChecks
	a.DestinationWalkRepairs += b.DestinationWalkRepairs
	a.RepairTripSearches += b.RepairTripSearches
	a.DominatedWalkParentsAvoided += b.DominatedWalkParentsAvoided
	x.stats.LastProfile = w
	if x.stats.NativeProfileQueries == 1 || w.Stats.Transfers > x.stats.MaxTransferProfile.Stats.Transfers {
		x.stats.MaxTransferProfile = w
	}
	if len(x.profileWork) < x.cfg.MaxProfileRecords {
		x.profileWork = append(x.profileWork, w)
	} else {
		x.stats.ProfileRecordsOmitted++
	}
}
