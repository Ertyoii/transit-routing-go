// FLASH-TB implements the canonical transfer/profile construction in
// Grossmann et al., arXiv:2312.13146v3, sections 4.3–5. The inspected MIT reference
// and its notices are pinned under research/reference/FLASH-TB.
package tripbased

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
	"time"
)

type FlashLimits struct {
	MaxRetainedReadiness int `json:"max_retained_readiness,omitempty"`
	MaxRetainedRuns      int `json:"max_retained_runs,omitempty"`
	MaxRetainedSources   int `json:"max_retained_sources,omitempty"`
	MaxRetainedQueued    int `json:"max_retained_queued,omitempty"`
	MaxSuffixBytes       int `json:"max_suffix_bytes,omitempty"`
	MaxSnapshotTransfers int `json:"max_snapshot_transfers,omitempty"`
	MaxStops             int `json:"max_stops"`
	MaxTrips             int `json:"max_trips"`
	MaxClosurePaths      int `json:"max_closure_paths"`
	MaxWalkSettled       int `json:"max_walk_settled"`
	MaxTransfers         int `json:"max_transfers"`
	MaxRuns              int `json:"max_runs"`
	MaxWork              int `json:"max_work"`
	MaxFlagBytes         int `json:"max_flag_bytes"`
	MaxWitnesses         int `json:"max_witnesses"`
	MaxQuerySegments     int `json:"max_query_segments"`
}
type FlashConfig struct {
	RetainedSourceStart string `json:"retained_source_start,omitempty"`
	ProfilePolicy       string `json:"profile_policy,omitempty"`
	// Empty keeps nonbinding-role certification. Only conservative semantic flags
	// may compose with the independently role-capped query kernel.
	WalkingPolicy string `json:"walking_policy,omitempty"`
	// Empty retains the original canonical/restricted contract.
	Semantics    string      `json:"semantics,omitempty"`
	Cells        int         `json:"cells"`
	MaxBoardings int         `json:"max_boardings"`
	Limits       FlashLimits `json:"limits"`
}
type FlashStats struct {
	RetainedWork                  int    `json:"retained_profile_work,omitempty"`
	RetainedProfileNS             int64  `json:"retained_profile_ns,omitempty"`
	RetainedSourcesStarted        int    `json:"retained_sources_started,omitempty"`
	RetainedSourcesCompleted      int    `json:"retained_sources_completed,omitempty"`
	RetainedReadiness             int    `json:"retained_offered_readiness,omitempty"`
	RetainedPropagationRuns       int    `json:"retained_propagation_runs,omitempty"`
	RetainedSeedOffers            int    `json:"retained_seed_offers,omitempty"`
	RetainedTripInitializations   int    `json:"retained_trip_initializations,omitempty"`
	RetainedTargetInitializations int    `json:"retained_target_initializations,omitempty"`
	RetainedEnqueueTests          int    `json:"retained_enqueue_tests,omitempty"`
	RetainedEvents                int    `json:"retained_exposed_events,omitempty"`
	RetainedWalkRelaxations       int    `json:"retained_walk_relaxations,omitempty"`
	RetainedTransferTests         int    `json:"retained_transfer_tests,omitempty"`
	RetainedTargetChecks          int    `json:"retained_target_checks,omitempty"`
	RetainedEmissions             int    `json:"retained_emitted_witnesses,omitempty"`
	RetainedPathNodes             int    `json:"retained_path_nodes_allocated,omitempty"`
	RetainedPeakQueued            int    `json:"retained_peak_pending_segments,omitempty"`
	RetainedQueuePayloadBytes     int    `json:"retained_peak_queue_capacity_payload_bytes,omitempty"`
	RetainedScratchPayloadBytes   int    `json:"retained_scratch_payload_bytes,omitempty"`
	RetainedSeedPayloadBytes      int    `json:"retained_seed_capacity_payload_bytes,omitempty"`
	Work                          int    `json:"work"`
	WalkSettled                   int    `json:"walk_settled"`
	ClosurePaths                  int    `json:"closure_paths"`
	TransferRuns                  int    `json:"transfer_runs"`
	ProfileRuns                   int    `json:"profile_runs"`
	CanonicalTransfers            int    `json:"canonical_transfers"`
	SetFlags                      int    `json:"set_flags"`
	UnflaggedTransfers            int    `json:"unflagged_transfers"`
	FlagBytes                     int    `json:"flag_bytes"`
	Queued                        int    `json:"queued_segments"`
	LocalPruned                   int    `json:"local_pruned"`
	FootpathPruned                int    `json:"footpath_pruned"`
	EqualArrivalRepairs           int    `json:"equal_arrival_repairs"`
	EqualReachedRepairs           int    `json:"equal_reached_repairs"`
	EnqueuePruned                 [4]int `json:"enqueue_pruned_e1_e2a_e2b_e2c"`
}
type FlashTransfer struct {
	FromTrip    string        `json:"from_trip"`
	Exit        int           `json:"exit_event_index"`
	ToTrip      string        `json:"to_trip"`
	Board       int           `json:"board_event_index"`
	WalkSeconds int64         `json:"walk_seconds"`
	RawEdges    []string      `json:"raw_edges"`
	Cells       []int         `json:"cells"`
	Source      string        `json:"supporting_source"`
	Departure   int64         `json:"supporting_departure"`
	Target      string        `json:"supporting_target"`
	Candidate   model.Journey `json:"supporting_candidate"`
}
type FlashWitness struct {
	Source      string        `json:"source"`
	Departure   int64         `json:"departure"`
	Target      string        `json:"target"`
	Boardings   int           `json:"boardings"`
	TransferIDs []int         `json:"transfer_ids"`
	Journey     model.Journey `json:"journey"`
}
type FlashPartition struct {
	Method      string         `json:"method"`
	Cells       map[string]int `json:"cells"`
	Sizes       []int          `json:"sizes"`
	LayoutEdges int            `json:"layout_edges"`
	TotalWeight int            `json:"total_weight"`
	CutWeight   int            `json:"cut_weight"`
}

// FlashTransferDiagnostics describes diagnostic availability, independently of
// whole-index completeness. False phase flags must never be treated as empty
// complete transfer/flag graphs.
type FlashTransferDiagnostics struct {
	TransferIndexComplete bool   `json:"transfer_index_complete"`
	FlagMatrixComplete    bool   `json:"flag_matrix_complete"`
	RecordsStatus         string `json:"records_status"`
}

type FlashSnapshot struct {
	TransferDiagnostics    *FlashTransferDiagnostics `json:"transfer_diagnostics,omitempty"`
	OmittedTransferRecords int                       `json:"omitted_transfer_records,omitempty"`
	TransferConstruction   string                    `json:"transfer_construction,omitempty"`
	FlagConstruction       string                    `json:"flag_construction,omitempty"`
	Profile                string                    `json:"profile"`
	Complete               bool                      `json:"complete"`
	Error                  string                    `json:"error,omitempty"`
	Config                 FlashConfig               `json:"config"`
	Stats                  FlashStats                `json:"stats"`
	Partition              FlashPartition            `json:"partition"`
	Lines                  [][]string                `json:"lines"`
	Transfers              []FlashTransfer           `json:"transfers"`
	Witnesses              []FlashWitness            `json:"flag_witnesses"`
	PhasesNS               map[string]int64          `json:"phases_ns"`
	ClosureDiameter        int64                     `json:"closure_diameter"`
}
type FlashIndex struct {
	ruleTransfersReady, ruleFlagsReady bool
	ruleTransferCount                  int
	ruleOffsets                        [][]int
	base, cp                           *Index
	cfg                                FlashConfig
	snap                               FlashSnapshot
	names                              []string
	ids                                map[string]int
	walks                              [][]paper.Path
	walkMap                            []map[int]*paper.Path
	eventOffset                        []int
	transferIDs                        [][][]int
	flags                              [][]uint64               // cell-major, bit per canonical transfer
	profileSink                        func(FlashWitness) error // complete profile stream for Transfer Patterns
}
type flashWork struct {
	ctx context.Context
	x   *FlashIndex
}

func (w *flashWork) tick() error {
	if e := w.ctx.Err(); e != nil {
		return e
	}
	if w.x.snap.Stats.Work >= w.x.cfg.Limits.MaxWork {
		return fmt.Errorf("FLASH-TB incomplete: work budget exhausted")
	}
	w.x.snap.Stats.Work++
	return nil
}
func (w *flashWork) run(profile bool) error {
	if e := w.tick(); e != nil {
		return e
	}
	s := &w.x.snap.Stats
	if s.TransferRuns+s.ProfileRuns >= w.x.cfg.Limits.MaxRuns {
		return fmt.Errorf("FLASH-TB incomplete: departure-run budget exhausted")
	}
	if profile {
		s.ProfileRuns++
	} else {
		s.TransferRuns++
	}
	return nil
}
func (x *FlashIndex) Complete() bool { return x.snap.Complete }
func (x *FlashIndex) Snapshot() FlashSnapshot {
	s := x.snap
	if x.cfg.Semantics == "static_stop_pair_v1" {
		diagnostic := x.ruleTransferDiagnostics()
		s.TransferDiagnostics = &diagnostic
		s.Transfers = []FlashTransfer{}
		s.OmittedTransferRecords = 0
		if diagnostic.RecordsStatus == "available" {
			s.Transfers = x.ruleTransferSnapshot()
		} else if diagnostic.TransferIndexComplete {
			// Only a completed offset/count phase supplies an exact omitted
			// record count. Otherwise the unavailability reason signals unknown.
			s.OmittedTransferRecords = x.ruleTransferCount
		}
	}
	s.Lines = make([][]string, len(x.snap.Lines))
	for i, v := range x.snap.Lines {
		s.Lines[i] = append([]string{}, v...)
	}
	s.Partition.Cells = map[string]int{}
	for k, v := range x.snap.Partition.Cells {
		s.Partition.Cells[k] = v
	}
	s.Partition.Sizes = append([]int{}, s.Partition.Sizes...)
	s.PhasesNS = map[string]int64{}
	for k, v := range x.snap.PhasesNS {
		s.PhasesNS[k] = v
	}
	s.Transfers = append([]FlashTransfer{}, s.Transfers...)
	for i := range s.Transfers {
		v := &s.Transfers[i]
		v.RawEdges = append([]string{}, v.RawEdges...)
		v.Cells = append([]int{}, v.Cells...)
		v.Candidate.Legs = append([]model.Leg{}, v.Candidate.Legs...)
	}
	s.Witnesses = append([]FlashWitness{}, s.Witnesses...)
	for i := range s.Witnesses {
		v := &s.Witnesses[i]
		v.TransferIDs = append([]int{}, v.TransferIDs...)
		v.Journey.Legs = append([]model.Leg{}, v.Journey.Legs...)
	}
	return s
}
func BuildFlash(ctx context.Context, d model.Dataset, c FlashConfig) (*FlashIndex, error) {
	return buildFlash(ctx, d, c, nil)
}
func buildFlash(ctx context.Context, d model.Dataset, c FlashConfig, sink func(FlashWitness) error) (x *FlashIndex, err error) {
	x = &FlashIndex{profileSink: sink, cfg: c, ids: map[string]int{}, snap: FlashSnapshot{Profile: "flash_tb_positive_transitive_nonbinding_v1", Config: c, Transfers: []FlashTransfer{}, Witnesses: []FlashWitness{}, PhasesNS: map[string]int64{}}}
	defer func() {
		x.snap.Complete = err == nil
		if err != nil {
			x.snap.Error = err.Error()
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	l := c.Limits
	if c.Cells < 1 || c.Cells > len(d.Stops) || c.MaxBoardings < 1 || c.MaxBoardings > 32 || l.MaxStops < 1 || l.MaxTrips < 1 || l.MaxClosurePaths < 1 || l.MaxWalkSettled < 1 || l.MaxTransfers < 1 || l.MaxRuns < 1 || l.MaxWork < 1 || l.MaxFlagBytes < 1 || l.MaxWitnesses < 1 || l.MaxQuerySegments < 1 {
		err = fmt.Errorf("FLASH-TB requires explicit positive configuration/budgets")
		return
	}
	if len(d.Stops) > l.MaxStops || len(d.Trips) > l.MaxTrips {
		err = fmt.Errorf("FLASH-TB incomplete: network size budget exhausted")
		return
	}
	if err = d.Validate(); err != nil {
		return
	}
	if c.WalkingPolicy != "" && (c.WalkingPolicy != "query_role_caps_v1" || c.Semantics != "static_stop_pair_v1") {
		err = fmt.Errorf("FLASH-TB capability: role-capped walking requires static-stop-pair conservative flags")
		return
	}
	if c.ProfilePolicy != "" && (c.ProfilePolicy != "retained_exact_event_v1" || c.Semantics != "static_stop_pair_v1" || sink == nil) {
		err = fmt.Errorf("FLASH-TB capability: retained profile policy requires static-stop-pair TP construction")
		return
	}
	if c.RetainedSourceStart != "" && c.ProfilePolicy != "retained_exact_event_v1" {
		err = fmt.Errorf("retained source rotation requires retained exact-event profiles")
		return
	}
	if c.ProfilePolicy != "" && (l.MaxRetainedReadiness < 1 || l.MaxRetainedRuns < 1 || l.MaxRetainedSources < 1 || l.MaxRetainedQueued < 1) {
		err = fmt.Errorf("retained profiles require explicit positive readiness, propagation, source and queue guards")
		return
	}
	if c.Semantics != "" && c.Semantics != "static_stop_pair_v1" {
		err = fmt.Errorf("FLASH-TB capability: unknown semantics policy")
		return
	}
	if c.Semantics == "static_stop_pair_v1" {
		if l.MaxSuffixBytes <= 0 || l.MaxSnapshotTransfers <= 0 {
			err = fmt.Errorf("FLASH-TB requires positive suffix-state byte and snapshot-record budgets")
			return
		}
		if len(d.Connections) > l.MaxSuffixBytes/8/((c.Cells+63)/64) {
			err = fmt.Errorf("FLASH-TB incomplete: suffix-state byte budget exhausted")
			return
		}
		for _, r := range d.Transfers {
			if r.FromTrip != "" || r.ToTrip != "" || r.FromRoute != "" || r.ToRoute != "" {
				err = fmt.Errorf("FLASH-TB capability: trip/route-specific rules unsupported")
				return
			}
		}
		x.snap.Profile = "flash_tb_static_stop_pair_suffix_flags_nonbinding_v1"
		if c.WalkingPolicy == "query_role_caps_v1" {
			x.snap.Profile = "flash_tb_static_stop_pair_suffix_flags_role_caps_v1"
		}
		x.snap.TransferConstruction = "complete earliest feasible event-to-line transfers; conservative superset, not Trans-ULTRA reduction"
		x.snap.FlagConstruction = "all feasible suffix target cells; conservative, not canonical optimal-profile flags"
	}
	if c.Semantics == "" && len(d.Transfers) > 0 {
		err = fmt.Errorf("FLASH-TB capability: GTFS transfer rules unsupported")
		return
	}
	for _, v := range d.Connections {
		if c.Semantics == "" && (!v.Pickup || !v.Dropoff) || v.Arrival <= v.Departure || v.From == v.To {
			err = fmt.Errorf("FLASH-TB capability: unrestricted positive non-self rides required")
			return
		}
	}
	for _, s := range d.Stops {
		x.names = append(x.names, s.ID)
	}
	sort.Strings(x.names)
	for i, s := range x.names {
		x.ids[s] = i
	}
	if c.RetainedSourceStart != "" {
		if _, ok := x.ids[c.RetainedSourceStart]; !ok {
			err = fmt.Errorf("retained source rotation requires an existing stop")
			return
		}
	}
	w := &flashWork{ctx, x}
	start := time.Now()
	if err = x.closeWalks(d, w); err != nil {
		return
	}
	x.snap.PhasesNS["walking_closure"] = time.Since(start).Nanoseconds()
	start = time.Now()
	x.base, err = New(ctx, d, Config{StrictLineOrder: true, TransferWalkingLimit: x.snap.ClosureDiameter, MaxTransfers: l.MaxTransfers, MaxQueuedSegments: l.MaxQuerySegments})
	if err != nil {
		return
	}
	x.snap.PhasesNS["baseline_tb"] = time.Since(start).Nanoseconds()
	x.eventOffset = make([]int, len(x.base.trips))
	offset := 0
	for _, line := range x.base.lines {
		ids := []string{}
		for _, ti := range line.trips {
			x.eventOffset[ti] = offset
			offset += len(x.base.trips[ti].cs) + 1
			ids = append(ids, x.base.trips[ti].id)
		}
		x.snap.Lines = append(x.snap.Lines, ids)
	}
	cp := *x.base
	if c.Semantics == "" {
		cp.links = make([][][]link, len(cp.trips))
		for ti, t := range cp.trips {
			cp.links[ti] = make([][]link, len(t.cs))
		}
	}
	x.cp = &cp
	start = time.Now()
	if err = x.partition(w); err != nil {
		return
	}
	x.snap.PhasesNS["partition"] = time.Since(start).Nanoseconds()
	start = time.Now()
	if c.Semantics == "static_stop_pair_v1" {
		err = x.ruleTransfers(w)
	} else {
		err = x.transULTRA(w)
	}
	if err != nil {
		return
	}
	if c.Semantics == "static_stop_pair_v1" {
		x.snap.PhasesNS["complete_rule_transfer_graph"] = time.Since(start).Nanoseconds()
	} else {
		x.snap.PhasesNS["trans_ultra"] = time.Since(start).Nanoseconds()
	}
	words := (x.transferCount() + 63) / 64
	if words > l.MaxFlagBytes/8/c.Cells {
		err = fmt.Errorf("FLASH-TB incomplete: flag byte budget exhausted")
		return
	}
	x.flags = make([][]uint64, c.Cells)
	for i := range x.flags {
		x.flags[i] = make([]uint64, words)
	}
	x.snap.Stats.FlagBytes = words * 8 * c.Cells
	start = time.Now()
	if c.Semantics == "static_stop_pair_v1" {
		err = x.suffixFlags(w)
		if err == nil && sink != nil {
			err = x.ruleProfiles(w)
		}
	} else {
		err = x.profiles(w)
	}
	if err != nil {
		return
	}
	if c.Semantics == "static_stop_pair_v1" {
		x.snap.PhasesNS["suffix_flags_and_optional_complete_profiles"] = time.Since(start).Nanoseconds()
	} else {
		x.snap.PhasesNS["canonical_profiles"] = time.Since(start).Nanoseconds()
	}
	for id := 0; id < x.transferCount(); id++ {
		if c.Semantics != "" {
			if err = w.tick(); err != nil {
				return
			}
		}
		flagged := false
		for cell := range x.flags {
			if x.flagged(id, cell) {
				flagged = true
				if c.Semantics == "" {
					x.snap.Transfers[id].Cells = append(x.snap.Transfers[id].Cells, cell)
				}
			}
		}
		if !flagged {
			x.snap.Stats.UnflaggedTransfers++
		}
	}
	return
}
func (x *FlashIndex) flagged(edge, cell int) bool {
	return x.flags[cell][edge/64]&(uint64(1)<<uint(edge%64)) != 0
}

// Search applies the target-cell flags. SearchUnflagged is the matched CP-TB
// control; SearchBaseline uses the original complete earliest-line transfer set.
func (x *FlashIndex) Search(ctx context.Context, q model.Query) (Result, error) {
	return x.searchFlash(ctx, q, 0)
}
func (x *FlashIndex) SearchUnflagged(ctx context.Context, q model.Query) (Result, error) {
	return x.searchFlash(ctx, q, 1)
}
func (x *FlashIndex) SearchBaseline(ctx context.Context, q model.Query) (Result, error) {
	return x.searchFlash(ctx, q, 2)
}
func (x *FlashIndex) checkQuery(ctx context.Context, q model.Query) (model.Query, error) {
	if e := ctx.Err(); e != nil {
		return q, e
	}
	if !x.Complete() {
		return q, fmt.Errorf("FLASH-TB incomplete index cannot answer queries")
	}
	if e := x.cp.p.CheckContext(ctx, q); e != nil {
		return q, e
	}
	if q.TransferBuffer != 0 || q.AccessibleOnly || q.MaxBoardings > x.cfg.MaxBoardings {
		return q, fmt.Errorf("FLASH-TB capability: buffer/accessibility/boarding profile mismatch")
	}
	if x.cfg.WalkingPolicy == "query_role_caps_v1" {
		if q.Walking == nil {
			return q, fmt.Errorf("FLASH-TB capability: explicit walking roles required")
		}
		return q, nil
	}
	wl := q.Walking
	if wl.Transfer < x.snap.ClosureDiameter {
		return q, fmt.Errorf("FLASH-TB capability: transfer cap may bind")
	}
	for _, p := range x.walks[x.ids[q.Origin]] {
		if p.Duration > wl.Access || p.To == q.Destination && p.Duration > wl.Direct {
			return q, fmt.Errorf("FLASH-TB capability: access/direct cap may bind")
		}
	}
	dst := x.ids[q.Destination]
	for i := range x.walkMap {
		if p := x.walkMap[i][dst]; p != nil && p.Duration > wl.Egress {
			return q, fmt.Errorf("FLASH-TB capability: egress cap may bind")
		}
	}
	bounded := *wl
	bounded.Transfer = x.snap.ClosureDiameter
	q.Walking = &bounded
	return q, nil
}
func (x *FlashIndex) searchFlash(ctx context.Context, q model.Query, mode int) (Result, error) {
	checked, e := x.checkQuery(ctx, q)
	if e != nil {
		return Result{Journeys: []model.Journey{}, Error: e.Error()}, e
	}
	q = checked
	if x.cfg.WalkingPolicy == "query_role_caps_v1" {
		return x.searchRoleCapped(ctx, q, mode)
	}

	if mode == 2 {
		return x.base.search(ctx, q, nil, true)
	}
	if mode == 1 {
		return x.cp.search(ctx, q, nil, true)
	}
	cell := x.snap.Partition.Cells[q.Destination]
	return x.cp.search(ctx, q, func(t, i, e int) bool { return x.flagged(x.transferID(t, i, e), cell) }, true)
}
