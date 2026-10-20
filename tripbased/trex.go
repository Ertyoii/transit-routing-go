// T-REX follows Sauer, Steil and Witt (ESA 2026), sections 3-4,
// Algorithms 1-4 and the Event-TB customization. See docs/TREX_CONTRACT.md.
package tripbased

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"time"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

type TREXLimits struct {
	MaxStops          int `json:"max_stops"`
	MaxTrips          int `json:"max_trips"`
	MaxInputEdges     int `json:"max_input_edges"`
	MaxClosurePaths   int `json:"max_closure_paths"`
	MaxWalkSettled    int `json:"max_walk_settled"`
	MaxTransfers      int `json:"max_transfers"`
	MaxRuns           int `json:"max_runs"`
	MaxWork           int `json:"max_work"`
	MaxWitnesses      int `json:"max_witnesses"`
	MaxOverlayEntries int `json:"max_overlay_entries"`
	MaxQueuedSegments int `json:"max_queued_segments"`
	MaxQueryWork      int `json:"max_query_work"`
}
type TREXConfig struct {
	ExactStorage bool           `json:"exact_storage,omitempty"`
	Levels       int            `json:"levels"`
	MaxBoardings int            `json:"max_boardings"`
	Partition    map[string]int `json:"partition,omitempty"`
	Limits       TREXLimits     `json:"limits"`
}
type TREXStats struct {
	Storage            TREXStorageWork `json:"storage"`
	Levels             []TREXLevelWork `json:"levels,omitempty"`
	Work               int             `json:"work"`
	ClosurePaths       int             `json:"closure_paths"`
	WalkSettled        int             `json:"walk_settled"`
	Transfers          int             `json:"transfers"`
	SameLineDiscarded  int             `json:"same_line_discarded"`
	EventRuns          int             `json:"event_runs"`
	QueuedSegments     int             `json:"queued_segments"`
	RankPromotions     int             `json:"rank_promotions"`
	RedundantWitnesses int             `json:"redundant_witnesses_avoided"`
	Traversals         int             `json:"traversals"`
	OverlayEntries     int             `json:"overlay_entries"`
	Ranks              []int           `json:"rank_distribution"`
}
type TREXTransfer struct {
	FromTrip    string   `json:"from_trip"`
	Exit        int      `json:"exit_event_index"`
	ToTrip      string   `json:"to_trip"`
	Board       int      `json:"board_event_index"`
	WalkSeconds int64    `json:"walk_seconds"`
	RawEdges    []string `json:"raw_edges"`
	Rank        int      `json:"rank"`
}
type TREXWitness struct {
	Level       int           `json:"level"`
	Cell        int           `json:"cell"`
	SourceTrip  string        `json:"source_trip"`
	SourceBoard int           `json:"source_board"`
	ExitTrip    string        `json:"exit_trip"`
	ExitEvent   int           `json:"exit_event"`
	Transfers   []int         `json:"transfers"`
	Journey     model.Journey `json:"journey"`
}
type TREXPartition struct {
	Method           string         `json:"method"`
	Cells            map[string]int `json:"cells"`
	Components       int            `json:"walking_components"`
	ComponentWeights []int          `json:"component_weights"`
	LayoutEdges      int            `json:"layout_edges"`
	TotalWeight      int            `json:"total_connection_weight"`
	LevelSizes       [][]int        `json:"level_sizes"`
	CutWeights       []int          `json:"cut_weights"`
}
type TREXPhase struct {
	ElapsedNS      int64  `json:"elapsed_ns"`
	AllocatedBytes uint64 `json:"allocated_bytes"`
	Work           int    `json:"work"`
}
type TREXSnapshot struct {
	FixedTransferWalkingCap *int64               `json:"fixed_transfer_walking_cap,omitempty"`
	Profile                 string               `json:"profile"`
	Complete                bool                 `json:"complete"`
	Error                   string               `json:"error,omitempty"`
	Config                  TREXConfig           `json:"config"`
	Stats                   TREXStats            `json:"stats"`
	Partition               TREXPartition        `json:"partition"`
	Lines                   [][]string           `json:"lines"`
	ClosureDiameter         int64                `json:"closure_diameter"`
	Transfers               []TREXTransfer       `json:"transfers"`
	Witnesses               []TREXWitness        `json:"traversal_witnesses"`
	Phases                  map[string]TREXPhase `json:"phases"`
}
type trexEdge struct {
	from, exit, to, board int
	walk                  *paper.Path
}
type trexOverlay struct{ starts, edges []int }
type TREXIndex struct {
	staticRulesCandidate bool   // opt-in qualification profile; legacy BuildTREX stays strict
	transferWalkLimit    *int64 // fixed-cap ranked and separate unranked builders bind this policy
	base                 *Index
	cfg                  TREXConfig
	proofs               trexProofStore
	snap                 TREXSnapshot
	names                []string
	ids                  map[string]int
	walks                [][]paper.Path
	walkMap              []map[int]*paper.Path
	cells, offsets       []int
	edges                []trexEdge
	ranks                []uint8 // levels are bounded by 12; verbose metadata is reconstructed on demand
	byEvent              [][][]int
	overlays             []trexOverlay
	successors           [][]int
}

func (x *TREXIndex) Complete() bool { return x != nil && x.snap.Complete }
func (x *TREXIndex) Snapshot() TREXSnapshot {
	// Diagnostic serialization deliberately owns every nested collection.
	view := x.snap
	if x.staticRulesCandidate && x.transferWalkLimit != nil {
		cap := *x.transferWalkLimit
		view.FixedTransferWalkingCap = &cap
	}
	view.Witnesses = x.expandTREXProofs()
	view.Transfers = make([]TREXTransfer, len(x.edges))
	for id, e := range x.edges {
		raw := make([]string, len(e.walk.Edges))
		for j, w := range e.walk.Edges {
			raw[j] = w.ID
		}
		view.Transfers[id] = TREXTransfer{FromTrip: x.base.trips[e.from].id, Exit: e.exit + 1, ToTrip: x.base.trips[e.to].id, Board: e.board, WalkSeconds: e.walk.Duration, RawEdges: raw, Rank: int(x.ranks[id])}
	}
	b, _ := json.Marshal(view)
	var s TREXSnapshot
	_ = json.Unmarshal(b, &s)
	return s
}
func (x *TREXIndex) tick(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if x.snap.Stats.Work >= x.cfg.Limits.MaxWork {
		return fmt.Errorf("T-REX incomplete: construction work budget exhausted")
	}
	x.snap.Stats.Work++
	return nil
}
func (x *TREXIndex) phase(name string, f func() error) error {
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start, work := time.Now(), x.snap.Stats.Work
	e := f()
	runtime.ReadMemStats(&after)
	x.snap.Phases[name] = TREXPhase{time.Since(start).Nanoseconds(), after.TotalAlloc - before.TotalAlloc, x.snap.Stats.Work - work}
	return e
}

func BuildTREX(ctx context.Context, d model.Dataset, cfg TREXConfig) (*TREXIndex, error) {
	return buildTREX(ctx, d, cfg, nil)
}

// BuildTREXStaticRulesCandidate is an opt-in qualification extension for static
// type-2/type-3 stop-pair rules and pickup/dropoff restrictions. It retains all
// ranked walking/resource guards; it is not a capped-walking T-REX certificate.
// The original paper's unrestricted-event proof is not claimed for this profile.
func BuildTREXStaticRulesCandidate(ctx context.Context, d model.Dataset, cfg TREXConfig) (*TREXIndex, error) {
	return buildTREXPolicy(ctx, d, cfg, nil, true)
}

// BuildTREXFixedTransferCapRankedCandidate customizes one fixed-cap transfer
// graph. Ranks and overlays remain active; per-query replay and independent
// frontier certification are still required. Query transfer caps must match.
func BuildTREXFixedTransferCapRankedCandidate(ctx context.Context, d model.Dataset, cfg TREXConfig, transferWalkingCap int64) (*TREXIndex, error) {
	if transferWalkingCap < 0 {
		return nil, fmt.Errorf("fixed transfer walking cap must be nonnegative")
	}
	return buildTREXPolicy(ctx, d, cfg, &transferWalkingCap, true)
}

func buildTREX(ctx context.Context, d model.Dataset, cfg TREXConfig, transferWalkLimit *int64) (*TREXIndex, error) {
	return buildTREXPolicy(ctx, d, cfg, transferWalkLimit, false)
}

func buildTREXPolicy(ctx context.Context, d model.Dataset, cfg TREXConfig, transferWalkLimit *int64, staticRulesCandidate bool) (x *TREXIndex, err error) {
	part := map[string]int{}
	for k, v := range cfg.Partition {
		part[k] = v
	}
	cfg.Partition = part
	x = &TREXIndex{staticRulesCandidate: staticRulesCandidate, transferWalkLimit: transferWalkLimit, cfg: cfg, ids: map[string]int{}, snap: TREXSnapshot{Profile: "trex_positive_static_transitive_nonbinding_v1", Config: cfg, Transfers: []TREXTransfer{}, Witnesses: []TREXWitness{}, Phases: map[string]TREXPhase{}}}
	if staticRulesCandidate {
		x.snap.Profile = "trex_static_stop_pair_nonbinding_candidate_v1"
		if transferWalkLimit != nil {
			x.snap.Profile = "trex_static_stop_pair_fixed_transfer_cap_ranked_candidate_v1"
		}
	}
	defer func() {
		x.snap.Complete = err == nil
		if err != nil {
			x.snap.Error = err.Error()
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	l := cfg.Limits
	if cfg.Levels < 0 || cfg.Levels > 12 || cfg.MaxBoardings < 1 || cfg.MaxBoardings > 32 || l.MaxStops < 1 || l.MaxTrips < 1 || l.MaxInputEdges < 1 || l.MaxClosurePaths < 1 || l.MaxWalkSettled < 1 || l.MaxTransfers < 1 || l.MaxRuns < 1 || l.MaxWork < 1 || l.MaxWitnesses < 1 || l.MaxOverlayEntries < 1 || l.MaxQueuedSegments < 1 || l.MaxQueryWork < 1 {
		err = fmt.Errorf("T-REX requires explicit positive limits, 0..12 levels and 1..32 boardings")
		return
	}
	if len(d.Stops) > l.MaxStops || len(d.Trips) > l.MaxTrips || len(d.Connections)+len(d.Footpaths) > l.MaxInputEdges {
		err = fmt.Errorf("T-REX incomplete: input size budget exhausted")
		return
	}
	err = x.phase("timetable", func() error {
		if e := d.Validate(); e != nil {
			return e
		}
		if !staticRulesCandidate && len(d.Transfers) > 0 {
			return fmt.Errorf("T-REX capability: GTFS transfer rules unsupported")
		}
		if staticRulesCandidate {
			for _, r := range d.Transfers {
				if r.Type != 2 && r.Type != 3 || r.FromTrip != "" || r.ToTrip != "" || r.FromRoute != "" || r.ToRoute != "" {
					return fmt.Errorf("T-REX candidate capability: only static type-2/type-3 stop-pair rules supported")
				}
			}
		}
		for _, c := range d.Connections {
			if e := x.tick(ctx); e != nil {
				return e
			}
			if !staticRulesCandidate && (!c.Pickup || !c.Dropoff) || c.Arrival <= c.Departure || c.From == c.To {
				return fmt.Errorf("T-REX capability: positive unrestricted non-self rides required")
			}
		}
		for _, p := range d.Footpaths {
			if e := x.tick(ctx); e != nil {
				return e
			}
			if p.Duration <= 0 {
				return fmt.Errorf("T-REX capability: positive walking edges required")
			}
		}
		var e error
		x.base, e = newIndex(ctx, d, Config{StrictLineOrder: true, MaxTransfers: l.MaxTransfers, MaxQueuedSegments: l.MaxQueuedSegments}, false)
		if e != nil {
			return e
		}
		for _, s := range d.Stops {
			x.names = append(x.names, s.ID)
		}
		sort.Strings(x.names)
		for i, s := range x.names {
			x.ids[s] = i
		}
		for _, line := range x.base.lines {
			ids := []string{}
			for _, ti := range line.trips {
				ids = append(ids, x.base.trips[ti].id)
			}
			x.snap.Lines = append(x.snap.Lines, ids)
		}
		return ctx.Err()
	})
	if err != nil {
		return
	}
	err = x.phase("walking_closure", func() error {
		carrier := &FlashIndex{names: x.names, ids: x.ids, cfg: FlashConfig{Limits: FlashLimits{MaxWork: l.MaxWork, MaxClosurePaths: l.MaxClosurePaths, MaxWalkSettled: l.MaxWalkSettled}}, snap: FlashSnapshot{Stats: FlashStats{Work: x.snap.Stats.Work}}}
		e := carrier.closeWalks(x.base.p.Data, &flashWork{ctx, carrier})
		x.snap.Stats.Work = carrier.snap.Stats.Work
		x.snap.Stats.ClosurePaths = carrier.snap.Stats.ClosurePaths
		x.snap.Stats.WalkSettled = carrier.snap.Stats.WalkSettled
		x.snap.ClosureDiameter = carrier.snap.ClosureDiameter
		x.walks = carrier.walks
		x.walkMap = carrier.walkMap
		if e != nil {
			return fmt.Errorf("T-REX walking closure: %w", e)
		}
		return nil
	})
	if err != nil {
		return
	}
	if err = x.phase("compact_layout_partition", func() error { return x.partitionTREX(ctx) }); err != nil {
		return
	}
	if err = x.phase("transfers", func() error { return x.buildTREXTransfers(ctx) }); err != nil {
		return
	}
	if err = x.phase("customization", func() error { return x.customizeTREX(ctx) }); err != nil {
		return
	}
	if err = x.phase("overlays_successors", func() error { return x.buildTREXOverlays(ctx) }); err != nil {
		return
	}
	x.snap.Stats.Ranks = make([]int, cfg.Levels+1)
	for _, rank := range x.ranks {
		x.snap.Stats.Ranks[rank]++
	}
	return
}

func (x *TREXIndex) scanTREXTransfers(ctx context.Context, emit func(int, int, int, int, *paper.Path) error) error {
	for ti, t := range x.base.trips {
		for i, c := range t.cs {
			if !c.Dropoff {
				continue
			}
			for wi := range x.walks[x.ids[c.To]] {
				p := &x.walks[x.ids[c.To]][wi]
				if x.transferWalkLimit != nil {
					if e := x.tick(ctx); e != nil {
						return e
					}
					if p.Duration > *x.transferWalkLimit {
						continue
					}
				}
				ready := c.Arrival + p.Duration
				if x.staticRulesCandidate {
					if e := x.tick(ctx); e != nil {
						return e
					}
					var allowed bool
					var e error
					ready, allowed, e = x.base.p.ReadyTime(model.Query{}, c.To, p.To, c.Arrival, p.Duration)
					if e != nil {
						return e
					}
					if !allowed {
						continue
					}
				}
				for _, o := range x.base.at[p.To] {
					if e := x.tick(ctx); e != nil {
						return e
					}
					line := x.base.lines[o.line]
					if !x.base.trips[line.trips[0]].cs[o.position].Pickup {
						continue
					}
					lo, hi := 0, len(line.trips)
					for lo < hi {
						if e := x.tick(ctx); e != nil {
							return e
						}
						m := (lo + hi) / 2
						if x.base.trips[line.trips[m]].cs[o.position].Departure < ready {
							lo = m + 1
						} else {
							hi = m
						}
					}
					if lo == len(line.trips) {
						continue
					}
					to := line.trips[lo]
					if to == ti || o.line == t.line && lo >= t.rank && o.position >= i+1 {
						x.snap.Stats.SameLineDiscarded++
						continue
					}
					if e := emit(ti, i, to, o.position, p); e != nil {
						return e
					}
				}
			}
		}
	}
	return nil
}

func (x *TREXIndex) checkTREXQuery(ctx context.Context, q model.Query) error {
	if e := x.checkTREXResources(ctx, q); e != nil {
		return e
	}
	if q.Walking.Transfer < x.snap.ClosureDiameter {
		return fmt.Errorf("T-REX capability: intermediate walking cap may bind")
	}
	for _, p := range x.walks[x.ids[q.Origin]] {
		if e := ctx.Err(); e != nil {
			return e
		}
		if p.Duration > q.Walking.Access || p.To == q.Destination && p.Duration > q.Walking.Direct {
			return fmt.Errorf("T-REX capability: access/direct cap may bind")
		}
	}
	dst := x.ids[q.Destination]
	for i := range x.walkMap {
		if e := ctx.Err(); e != nil {
			return e
		}
		if p := x.walkMap[i][dst]; p != nil && p.Duration > q.Walking.Egress {
			return fmt.Errorf("T-REX capability: egress cap may bind")
		}
	}
	return nil
}

// Shared resource checks do not certify any walking-role cap.
func (x *TREXIndex) checkTREXResources(ctx context.Context, q model.Query) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if !x.Complete() {
		return fmt.Errorf("T-REX incomplete index cannot answer queries")
	}
	if e := x.base.p.CheckContext(ctx, q); e != nil {
		return e
	}
	if q.TransferBuffer != 0 || q.AccessibleOnly || q.MaxBoardings > x.cfg.MaxBoardings {
		return fmt.Errorf("T-REX capability: buffer/accessibility/boarding mismatch")
	}
	return nil
}
