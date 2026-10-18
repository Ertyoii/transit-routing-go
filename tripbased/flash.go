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
	MaxStops         int `json:"max_stops"`
	MaxTrips         int `json:"max_trips"`
	MaxClosurePaths  int `json:"max_closure_paths"`
	MaxWalkSettled   int `json:"max_walk_settled"`
	MaxTransfers     int `json:"max_transfers"`
	MaxRuns          int `json:"max_runs"`
	MaxWork          int `json:"max_work"`
	MaxFlagBytes     int `json:"max_flag_bytes"`
	MaxWitnesses     int `json:"max_witnesses"`
	MaxQuerySegments int `json:"max_query_segments"`
}
type FlashConfig struct {
	Cells        int         `json:"cells"`
	MaxBoardings int         `json:"max_boardings"`
	Limits       FlashLimits `json:"limits"`
}
type FlashStats struct {
	Work                int    `json:"work"`
	WalkSettled         int    `json:"walk_settled"`
	ClosurePaths        int    `json:"closure_paths"`
	TransferRuns        int    `json:"transfer_runs"`
	ProfileRuns         int    `json:"profile_runs"`
	CanonicalTransfers  int    `json:"canonical_transfers"`
	SetFlags            int    `json:"set_flags"`
	UnflaggedTransfers  int    `json:"unflagged_transfers"`
	FlagBytes           int    `json:"flag_bytes"`
	Queued              int    `json:"queued_segments"`
	LocalPruned         int    `json:"local_pruned"`
	FootpathPruned      int    `json:"footpath_pruned"`
	EqualArrivalRepairs int    `json:"equal_arrival_repairs"`
	EqualReachedRepairs int    `json:"equal_reached_repairs"`
	EnqueuePruned       [4]int `json:"enqueue_pruned_e1_e2a_e2b_e2c"`
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
type FlashSnapshot struct {
	Profile         string           `json:"profile"`
	Complete        bool             `json:"complete"`
	Error           string           `json:"error,omitempty"`
	Config          FlashConfig      `json:"config"`
	Stats           FlashStats       `json:"stats"`
	Partition       FlashPartition   `json:"partition"`
	Lines           [][]string       `json:"lines"`
	Transfers       []FlashTransfer  `json:"transfers"`
	Witnesses       []FlashWitness   `json:"flag_witnesses"`
	PhasesNS        map[string]int64 `json:"phases_ns"`
	ClosureDiameter int64            `json:"closure_diameter"`
}
type FlashIndex struct {
	base, cp    *Index
	cfg         FlashConfig
	snap        FlashSnapshot
	names       []string
	ids         map[string]int
	walks       [][]paper.Path
	walkMap     []map[int]*paper.Path
	eventOffset []int
	transferIDs [][][]int
	flags       [][]uint64               // cell-major, bit per canonical transfer
	profileSink func(FlashWitness) error // complete profile stream for Transfer Patterns
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
	if len(d.Transfers) > 0 {
		err = fmt.Errorf("FLASH-TB capability: GTFS transfer rules unsupported")
		return
	}
	for _, v := range d.Connections {
		if !v.Pickup || !v.Dropoff || v.Arrival <= v.Departure || v.From == v.To {
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
	cp.links = make([][][]link, len(cp.trips))
	for ti, t := range cp.trips {
		cp.links[ti] = make([][]link, len(t.cs))
	}
	x.cp = &cp
	start = time.Now()
	if err = x.partition(w); err != nil {
		return
	}
	x.snap.PhasesNS["partition"] = time.Since(start).Nanoseconds()
	start = time.Now()
	if err = x.transULTRA(w); err != nil {
		return
	}
	x.snap.PhasesNS["trans_ultra"] = time.Since(start).Nanoseconds()
	words := (len(x.snap.Transfers) + 63) / 64
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
	if err = x.profiles(w); err != nil {
		return
	}
	x.snap.PhasesNS["canonical_profiles"] = time.Since(start).Nanoseconds()
	for i := range x.snap.Transfers {
		for cell := range x.flags {
			if x.flagged(i, cell) {
				x.snap.Transfers[i].Cells = append(x.snap.Transfers[i].Cells, cell)
			}
		}
		if len(x.snap.Transfers[i].Cells) == 0 {
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

	if mode == 2 {
		return x.base.search(ctx, q, nil, true)
	}
	if mode == 1 {
		return x.cp.search(ctx, q, nil, true)
	}
	cell := x.snap.Partition.Cells[q.Destination]
	return x.cp.search(ctx, q, func(t, i, e int) bool { return x.flagged(x.transferIDs[t][i][e], cell) }, true)
}
