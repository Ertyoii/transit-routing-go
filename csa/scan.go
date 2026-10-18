// Package csa implements a correctness-first path-enumerating connection scan.
// It extends the departure-order CSA mechanism, not the paper's single-label EA pruning.
// Enumeration, heuristic candidate, and certified bounded-top-k modes have
// separate completeness contracts.
package csa

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/transfer"
	"sort"
	"time"
)

type Config struct {
	MaxLabels   int `json:"max_labels"`
	MaxJourneys int `json:"max_journeys"`
	// Zero disables this heuristic. A positive cap can lose feasible continuations.
	HeuristicLabelsPerStop int `json:"heuristic_labels_per_stop"`
	// Optional diagnostic witnesses; observation only, never used for pruning.
	TraceWitnesses []model.Journey `json:"trace_witnesses,omitempty"`
	// ExactTopK enables K-prefix resource dominance and optimistic suffix
	// bounds. It cannot be combined with heuristic per-stop pruning.
	ExactTopK int `json:"exact_top_k,omitempty"`
}
type Stats struct {
	Labels             int `json:"labels"`
	ConnectionAttempts int `json:"connection_attempts"`
	FootpathAttempts   int `json:"footpath_attempts"`
	HeuristicDiscarded int `json:"heuristic_discarded"`
	EquivalentPruned   int `json:"certified_prefixes_pruned,omitempty"`
	ArrivalPruned      int `json:"arrival_bound_pruned,omitempty"`
	TerminalCandidates int `json:"terminal_candidates,omitempty"`
	EquivalentStates   int `json:"incoming_event_groups,omitempty"`
	DeadEndPruned      int `json:"dead_end_prefixes_pruned,omitempty"`
}
type Result struct {
	Binding           model.ResultBinding `json:"binding"`
	QueryID           string              `json:"query_id"`
	Journeys          []model.Journey     `json:"journeys"`
	Complete          bool                `json:"complete"`
	Error             string              `json:"error,omitempty"`
	Stats             Stats               `json:"stats"`
	StartedOffsetNS   int64               `json:"started_offset_ns"`
	CompletedOffsetNS int64               `json:"completed_offset_ns"`
	Trace             []TraceEvent        `json:"trace,omitempty"`
	TraceTruncated    bool                `json:"trace_truncated,omitempty"`
	TopK              int                 `json:"top_k,omitempty"`
	TopKComplete      bool                `json:"top_k_complete"`
	CapTopK           int                 `json:"per_boarding_cap_k,omitempty"`
	CapComplete       bool                `json:"all_boarding_caps_complete"`
	CapResults        []CapResult         `json:"cap_results,omitempty"`
}
type Batch struct {
	Results                []Result `json:"results"`
	SharedConnectionVisits int      `json:"shared_connection_visits"`
	MakespanNS             int64    `json:"makespan_ns"`
}
type BudgetError struct{ Resource string }

func (e *BudgetError) Error() string { return "CSA incomplete: " + e.Resource + " budget exhausted" }

type Index struct {
	datasetSHA256 string
	data          model.Dataset
	stops         map[string]int
	access        []model.Access
	tripAccess    map[string]model.Access
	walks         [][]model.Footpath
	transfers     *transfer.Resolver
	chains        [][]int
	incomingWalks [][]model.Footpath
}

func New(d model.Dataset) (*Index, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	d = d.Clone()
	sort.Slice(d.Connections, func(i, j int) bool {
		a, b := d.Connections[i], d.Connections[j]
		if a.Departure != b.Departure {
			return a.Departure < b.Departure
		}
		return a.ID < b.ID
	})
	idx := &Index{datasetSHA256: model.DatasetFingerprint(d), data: d, stops: map[string]int{}, tripAccess: map[string]model.Access{}, walks: make([][]model.Footpath, len(d.Stops)), transfers: transfer.New(d)}
	for i, s := range d.Stops {
		idx.stops[s.ID] = i
		idx.access = append(idx.access, s.Accessible)
	}
	for _, t := range d.Trips {
		idx.tripAccess[t.ID] = t.Accessible
	}
	for _, w := range d.Footpaths {
		idx.walks[idx.stops[w.From]] = append(idx.walks[idx.stops[w.From]], w)
	}
	for i := range idx.walks {
		sort.Slice(idx.walks[i], func(a, b int) bool { return idx.walks[i][a].ID < idx.walks[i][b].ID })
	}
	idx.incomingWalks = make([][]model.Footpath, len(d.Stops))
	for _, w := range d.Footpaths {
		idx.incomingWalks[idx.stops[w.To]] = append(idx.incomingWalks[idx.stops[w.To]], w)
	}
	chains := map[string][]int{}
	for i, c := range d.Connections {
		chains[c.TripID] = append(chains[c.TripID], i)
	}
	tripIDs := []string{}
	for id := range chains {
		tripIDs = append(tripIDs, id)
	}
	sort.Strings(tripIDs)
	for _, id := range tripIDs {
		chain := chains[id]
		sort.Slice(chain, func(i, j int) bool { return d.Connections[chain[i]].Sequence < d.Connections[chain[j]].Sequence })
		idx.chains = append(idx.chains, chain)
	}
	return idx, nil
}

type label struct {
	stop                     int
	time, walk, block        int64
	boards, depth            int
	lastRide, lastConnection int
	parent                   *label
	leg                      model.Leg
	discarded                bool
	identity                 string
}
type lane struct {
	index              *Index
	query              model.Query
	config             Config
	pools              [][]*label
	result             Result
	err                error
	ctx                context.Context
	states             map[stateKey][]*label
	terminal           []*label
	walkToDestination  []int64
	lastVehicleArrival []int64
}

func (idx *Index) newLane(ctx context.Context, q model.Query, cfg Config) (*lane, error) {
	if err := q.Validate(idx.data); err != nil {
		return nil, err
	}
	if cfg.MaxLabels <= 0 || cfg.MaxJourneys <= 0 || cfg.HeuristicLabelsPerStop < 0 || cfg.ExactTopK < 0 || cfg.ExactTopK > cfg.MaxJourneys || cfg.ExactTopK > 0 && cfg.HeuristicLabelsPerStop > 0 {
		return nil, fmt.Errorf("invalid CSA budgets")
	}
	if q.Walking != nil {
		w := *q.Walking
		q.Walking = &w
	}
	l := &lane{index: idx, query: q, config: cfg, pools: make([][]*label, len(idx.data.Stops)), ctx: ctx, result: Result{Binding: model.BindQuery(idx.datasetSHA256, q), QueryID: q.ID, Journeys: []model.Journey{}}}
	if cfg.ExactTopK > 0 {
		l.states = map[stateKey][]*label{}
		l.result.TopK = cfg.ExactTopK
		l.prepareLastBoardingBound()
	}
	if l.stopOK(idx.stops[q.Origin]) && l.stopOK(idx.stops[q.Destination]) {
		l.add(&label{stop: idx.stops[q.Origin], time: q.Departure, lastRide: -1, lastConnection: -1})
	}
	return l, nil
}
func (l *lane) stopOK(stop int) bool {
	return !l.query.AccessibleOnly || l.index.access[stop] == model.Yes
}
func (l *lane) canExit(p *label) bool {
	return p.lastConnection < 0 || l.index.data.Connections[p.lastConnection].Dropoff && l.stopOK(p.stop)
}
func (l *lane) add(p *label) {
	if l.err != nil {
		return
	}
	if err := l.ctx.Err(); err != nil {
		l.err = err
		return
	}
	if l.result.Stats.Labels >= l.config.MaxLabels {
		l.trace(p, "label_budget")
		l.err = &BudgetError{"labels"}
		return
	}
	l.result.Stats.Labels++
	q := l.query
	if l.beyondArrival(p.time) {
		l.result.Stats.ArrivalPruned++
		return
	}
	if p.stop == l.index.stops[q.Destination] && l.canExit(p) {
		if q.Walking != nil {
			cap := q.Walking.Egress
			if p.boards == 0 {
				cap = q.Walking.Direct
			}
			if p.block > cap {
				return
			}
		}
		l.result.Stats.TerminalCandidates++
		if l.config.ExactTopK > 0 {
			l.keepTerminal(p)
			l.trace(p, "destination")
			return
		}
		if len(l.result.Journeys) >= l.config.MaxJourneys {
			l.trace(p, "journey_budget")
			l.err = &BudgetError{"journeys"}
			return
		}
		legs := make([]model.Leg, p.depth)
		for cursor := p; cursor.parent != nil; cursor = cursor.parent {
			legs[cursor.depth-1] = cursor.leg
		}
		l.result.Journeys = append(l.result.Journeys, model.Journey{Legs: legs})
		l.trace(p, "destination")
		return
	}
	if p.depth >= q.MaxLegs {
		l.trace(p, "leg_bound")
		return
	}
	if l.config.ExactTopK > 0 && l.lastBoardingDeadEnd(p) {
		l.result.Stats.DeadEndPruned++
		return
	}
	if l.config.ExactTopK > 0 && !l.keepEquivalent(p) {
		l.result.Stats.EquivalentPruned++
		return
	}
	if l.config.HeuristicLabelsPerStop > 0 && len(l.pools[p.stop]) >= l.config.HeuristicLabelsPerStop {
		l.trace(p, "per_stop_cap")
		l.result.Stats.HeuristicDiscarded++
		return
	}
	l.pools[p.stop] = append(l.pools[p.stop], p)
	l.trace(p, "retained")
	if !l.canExit(p) {
		return
	}
	for _, w := range l.index.walks[p.stop] {
		l.result.Stats.FootpathAttempts++
		if p.walk+w.Duration > q.MaxWalkSeconds || p.time+w.Duration > q.LatestArrival {
			continue
		}
		to := l.index.stops[w.To]
		if !l.stopOK(p.stop) || !l.stopOK(to) || q.AccessibleOnly && w.Accessible != model.Yes {
			continue
		}
		if q.Walking != nil {
			cap := max(q.Walking.Transfer, q.Walking.Egress)
			if p.boards == 0 {
				cap = max(q.Walking.Access, q.Walking.Direct)
			}
			if p.block+w.Duration > cap {
				continue
			}
		}
		child := &label{stop: to, time: p.time + w.Duration, walk: p.walk + w.Duration, block: p.block + w.Duration, boards: p.boards, depth: p.depth + 1, lastRide: p.lastRide, lastConnection: -1, parent: p, leg: model.Leg{Kind: "walk", Ref: w.ID, Departure: p.time, Arrival: p.time + w.Duration}}
		l.add(child)
		if l.err != nil {
			return
		}
	}
}
func (l *lane) ride(p *label, ci int) {
	if p.discarded {
		return
	}
	l.result.Stats.ConnectionAttempts++
	c := l.index.data.Connections[ci]
	q := l.query
	if p.time > c.Departure || c.Arrival > q.LatestArrival {
		return
	}
	if l.beyondArrival(c.Arrival) {
		l.result.Stats.ArrivalPruned++
		return
	}
	boards := p.boards
	staying := false
	if p.lastConnection >= 0 {
		prior := l.index.data.Connections[p.lastConnection]
		staying = prior.TripID == c.TripID && prior.Sequence+1 == c.Sequence
	}
	if !staying {
		if !l.canExit(p) || !c.Pickup || !l.stopOK(p.stop) || q.AccessibleOnly && l.index.tripAccess[c.TripID] != model.Yes {
			return
		}
		if q.Walking != nil {
			cap := q.Walking.Access
			if boards > 0 {
				cap = q.Walking.Transfer
			}
			if p.block > cap {
				return
			}
		}
		if boards > 0 {
			previous := l.index.data.Connections[p.lastRide]
			decision, err := l.index.transfers.Resolve(previous, c, q.TransferBuffer)
			if err != nil {
				l.err = err
				return
			}
			if !decision.Allowed || c.Departure < previous.Arrival+decision.Minimum {
				return
			}
		}
		boards++
	}
	if boards > q.MaxBoardings {
		return
	}
	// Reject a provably hopeless transition before materializing a search label.
	// ConnectionAttempts still records its work; MaxLabels counts generated labels.
	if l.config.ExactTopK > 0 && boards == q.MaxBoardings && l.lastVehicleArrival[ci] > q.LatestArrival {
		l.result.Stats.DeadEndPruned++
		return
	}
	l.add(&label{stop: l.index.stops[c.To], time: c.Arrival, walk: p.walk, boards: boards, depth: p.depth + 1, lastRide: ci, lastConnection: ci, parent: p, leg: model.Leg{Kind: "ride", Ref: c.ID, Departure: c.Departure, Arrival: c.Arrival}})
}
func (l *lane) finish(start time.Time) Result {
	if l.err == nil {
		l.err = l.ctx.Err()
	}
	l.result.Complete = l.err == nil && l.result.Stats.HeuristicDiscarded == 0 && l.config.ExactTopK == 0
	if l.config.ExactTopK > 0 {
		l.result.TopKComplete = l.err == nil
		l.result.Journeys = []model.Journey{}
		for _, p := range l.terminal {
			l.result.Journeys = append(l.result.Journeys, labelJourney(p))
		}
		l.result.Stats.EquivalentStates = len(l.states)
	}
	if l.err != nil {
		l.result.Error = l.err.Error()
	}
	l.result.CompletedOffsetNS = time.Since(start).Nanoseconds()
	return l.result
}

// Single is an independent single-lane scan scheduler. ExactTopK enables the
// declared bounded arrival-order pruning; zero enumerates the finite universe.
func (idx *Index) Single(ctx context.Context, q model.Query, cfg Config) (Result, error) {
	start := time.Now()
	l, err := idx.newLane(ctx, q, cfg)
	if err != nil {
		return Result{}, err
	}
	first := sort.Search(len(idx.data.Connections), func(i int) bool { return idx.data.Connections[i].Departure >= q.Departure })
	for begin := first; begin < len(idx.data.Connections) && l.err == nil; {
		if idx.data.Connections[begin].Departure > q.LatestArrival || l.beyondArrival(idx.data.Connections[begin].Departure) {
			break
		}
		end := begin + 1
		for end < len(idx.data.Connections) && idx.data.Connections[end].Departure == idx.data.Connections[begin].Departure {
			end++
		}
		cursors := make([]int, end-begin)
		for changed := true; changed && l.err == nil; {
			changed = false
			for ci := begin; ci < end && l.err == nil; ci++ {
				if l.beyondArrival(idx.data.Connections[ci].Departure) {
					continue
				}
				stop := idx.stops[idx.data.Connections[ci].From]
				for cursors[ci-begin] < len(l.pools[stop]) && l.err == nil {
					p := l.pools[stop][cursors[ci-begin]]
					cursors[ci-begin]++
					changed = true
					l.ride(p, ci)
				}
			}
		}
		begin = end
	}
	return l.finish(start), l.err
}

// Shared scans each connection/time group across independent OD lanes before moving on.
// It does not call Single. Time-tied zero-duration dependencies are closed to a fixed point.
// Budgets are per lane, so unrelated queries cannot consume another query's allowance.
func (idx *Index) Shared(ctx context.Context, queries []model.Query, cfg Config) (Batch, error) {
	start := time.Now()
	batch := Batch{Results: []Result{}}
	lanes := make([]*lane, len(queries))
	ids := map[string]bool{}
	// Validate all queries before initializing any lane, so malformed batches are atomic.
	for _, q := range queries {
		if ids[q.ID] {
			return batch, fmt.Errorf("duplicate batch query ID %q", q.ID)
		}
		ids[q.ID] = true
		if err := q.Validate(idx.data); err != nil {
			return batch, err
		}
	}
	for i, q := range queries {
		initOffset := time.Since(start).Nanoseconds()
		l, err := idx.newLane(ctx, q, cfg)
		if err != nil {
			return batch, err
		}
		l.result.StartedOffsetNS = initOffset
		lanes[i] = l
	}
	minimumDeparture := int64(model.MaxServiceSecond)
	for _, q := range queries {
		minimumDeparture = min(minimumDeparture, q.Departure)
	}
	first := sort.Search(len(idx.data.Connections), func(i int) bool { return idx.data.Connections[i].Departure >= minimumDeparture })
	for begin := first; begin < len(idx.data.Connections); {
		alive := false
		for _, l := range lanes {
			if l.err == nil && idx.data.Connections[begin].Departure <= l.query.LatestArrival && !l.beyondArrival(idx.data.Connections[begin].Departure) {
				alive = true
				break
			}
		}
		if !alive {
			break
		}
		end := begin + 1
		for end < len(idx.data.Connections) && idx.data.Connections[end].Departure == idx.data.Connections[begin].Departure {
			end++
		}
		cursors := make([][]int, len(lanes))
		for i := range cursors {
			cursors[i] = make([]int, end-begin)
		}
		for changed := true; changed; {
			changed = false
			for ci := begin; ci < end; ci++ {
				batch.SharedConnectionVisits++
				stop := idx.stops[idx.data.Connections[ci].From]
				for i, l := range lanes {
					if idx.data.Connections[ci].Departure < l.query.Departure || idx.data.Connections[ci].Departure > l.query.LatestArrival || l.beyondArrival(idx.data.Connections[ci].Departure) {
						continue
					}
					for l.err == nil && cursors[i][ci-begin] < len(l.pools[stop]) {
						p := l.pools[stop][cursors[i][ci-begin]]
						cursors[i][ci-begin]++
						changed = true
						l.ride(p, ci)
					}
				}
			}
		}
		begin = end
	}
	var firstErr error
	for _, l := range lanes {
		batch.Results = append(batch.Results, l.finish(start))
		if firstErr == nil && l.err != nil {
			firstErr = l.err
		}
	}
	batch.MakespanNS = time.Since(start).Nanoseconds()
	return batch, firstErr
}
