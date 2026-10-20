// Scalable Transfer Patterns, ALENEX2016 sections3.2,3.4,3.5: all-local
// specialization with border-only global profiles. See the contract document.
package tripbased

import (
	"container/heap"
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"runtime"
	"slices"
	"sort"
	"time"
)

type ScalableConfig struct {
	LongDistanceTrips       []string      `json:"long_distance_trips,omitempty"`
	Hubs                    []string      `json:"hubs,omitempty"`
	DirectIndex             string        `json:"direct_index,omitempty"` // incidence (default), pairs or demand
	MaxDirectPairs          int           `json:"max_direct_pairs"`
	BorderStorage           string        `json:"border_storage,omitempty"` // source (default) or cluster
	MaxClusterDAGs          int           `json:"max_cluster_dags"`
	BorderSearchMode        string        `json:"border_search_mode,omitempty"` // empty="border"; "expanded" is an ablation
	MaxLocalCacheEntries    int           `json:"max_local_cache_entries"`
	BorderMergeMaxSize      int           `json:"border_merge_max_size"`
	BorderReductionPercent  int           `json:"border_reduction_percent"`
	DisableLocalSufficiency bool          `json:"disable_local_sufficiency,omitempty"`
	Patterns                PatternConfig `json:"patterns"`
	MaxClusterSize          int           `json:"max_cluster_size"`
	MaxDirectArcs           int           `json:"max_direct_arcs"`
	MaxProfileRuns          int           `json:"max_profile_runs"`
	MaxProfileLabels        int           `json:"max_profile_labels"`
	// Optional complete stop partition. Walking components must remain intact.
	Partition map[string]int `json:"partition,omitempty"`
}
type ScalableStats struct {
	LongDistanceStations  int              `json:"long_distance_stations"`
	Hubs                  int              `json:"hubs"`
	LongDistanceRuns      int              `json:"long_distance_runs"`
	LongQueueLabels       int              `json:"long_queue_labels"`
	HubStopLabels         int              `json:"hub_stop_labels"`
	LongEvaluations       int              `json:"long_evaluations"`
	LongGraphEdges        int              `json:"long_graph_edges"`
	DirectCache           DirectCacheStats `json:"direct_cache"`
	IndexedLinePairs      int              `json:"indexed_line_pairs"`
	ConstructionWork      int              `json:"construction_work"` // main + both insertion stores
	BorderBeforeReduction int              `json:"border_before_reduction"`
	BorderAfterReduction  int              `json:"border_after_reduction"`
	BorderClusterMerges   int              `json:"border_cluster_merges"`
	GlobalQueueLabels     int              `json:"global_queue_labels"`
	LocalGraphEdges       int              `json:"local_graph_edges"`
	LocalEvaluations      int              `json:"local_evaluations"`
	LocalCacheHits        int              `json:"local_cache_hits"`
	LocalCacheEntries     int              `json:"local_cache_entries"`
	LocalSufficientCells  int              `json:"local_sufficient_cells"`
	Work                  int              `json:"work"`
	Clusters              int              `json:"clusters"`
	BorderStops           int              `json:"border_stops"`
	LocalArcs             int              `json:"local_arcs"`
	BorderGraphArcs       int              `json:"border_graph_arcs"`
	LocalRuns             int              `json:"local_runs"`
	BorderRuns            int              `json:"border_runs"`
	ProfileLabels         int              `json:"profile_labels"`
}
type ScalablePhaseCost struct {
	ElapsedNS      int64  `json:"elapsed_ns"`
	AllocatedBytes uint64 `json:"allocated_bytes"`
	HeapAfter      uint64 `json:"heap_after_bytes"`
}
type ScalableSnapshot struct {
	LongDistance    PatternSnapshot              `json:"long_distance"`
	LongStations    []string                     `json:"long_stations,omitempty"`
	HubAccess       map[string][]string          `json:"hub_access,omitempty"`
	ClusterDAGs     []ClusterPatternDAG          `json:"cluster_dags,omitempty"`
	ClusterStats    ClusterPatternStats          `json:"cluster_stats"`
	LocalSufficient []bool                       `json:"local_sufficient"`
	PhaseCosts      map[string]ScalablePhaseCost `json:"phase_costs"`
	Profile         string                       `json:"profile"`
	Complete        bool                         `json:"complete"`
	Error           string                       `json:"error,omitempty"`
	Config          ScalableConfig               `json:"config"`
	Stats           ScalableStats                `json:"stats"`
	Cells           map[string]int               `json:"cells"`
	Borders         []string                     `json:"borders"`
	Local           PatternSnapshot              `json:"local"`
	Border          PatternSnapshot              `json:"border"`
	PhasesNS        map[string]int64             `json:"phases_ns"`
}
type ScalableIndex struct {
	long                     *PatternIndex
	localDirect              *PatternIndex
	longTrips                map[string]bool
	longStops, hubs, portals []bool
	longStations             []int
	access                   map[int]map[int]bool
	longGraphs               map[int]map[int][]PatternEdge
	directCache              *patternDirectCache
	shared                   *clusterPatterns
	cfg                      ScalableConfig
	snap                     ScalableSnapshot
	local, border            *PatternIndex
	cells                    []int
	borders                  [][]int
	borderSet                []bool
	localGraphs              map[int]map[int][]PatternEdge
	localCache               map[scalableLocalKey][]scalableLocalResult
}

func (x *ScalableIndex) Complete() bool { return x.snap.Complete }
func (x *ScalableIndex) Snapshot() ScalableSnapshot {
	s := x.snap
	s.Config.LongDistanceTrips = slices.Clone(x.cfg.LongDistanceTrips)
	s.Config.Hubs = slices.Clone(x.cfg.Hubs)
	s.LongStations = slices.Clone(x.snap.LongStations)
	s.HubAccess = map[string][]string{}
	for a, b := range x.snap.HubAccess {
		s.HubAccess[a] = slices.Clone(b)
	}
	if x.long != nil {
		s.LongDistance = x.long.Snapshot()
	}
	if x.directCache != nil {
		s.Stats.DirectCache = x.directCache.stats
	}
	if x.shared != nil {
		s.ClusterDAGs, s.ClusterStats = x.shared.snapshot()
	}
	s.LocalSufficient = append([]bool{}, x.snap.LocalSufficient...)
	s.PhaseCosts = map[string]ScalablePhaseCost{}
	for k, v := range x.snap.PhaseCosts {
		s.PhaseCosts[k] = v
	}
	s.Cells = map[string]int{}
	for k, v := range x.snap.Cells {
		s.Cells[k] = v
	}
	s.Config.Partition = map[string]int{}
	for k, v := range x.cfg.Partition {
		s.Config.Partition[k] = v
	}
	s.Borders = append([]string{}, s.Borders...)
	s.PhasesNS = map[string]int64{}
	for k, v := range x.snap.PhasesNS {
		s.PhasesNS[k] = v
	}
	if x.local != nil {
		s.Local = x.local.Snapshot()
	}
	if x.border != nil {
		s.Border = x.border.Snapshot()
	}
	return s
}
func (x *ScalableIndex) tick(ctx context.Context) error {
	if e := x.charge(ctx); e != nil {
		return e
	}
	x.snap.Stats.Work++
	return nil
}
func (x *ScalableIndex) charge(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if x.snap.Stats.ConstructionWork >= x.cfg.Patterns.MaxBuildWork {
		return fmt.Errorf("Scalable TP incomplete: aggregate construction work budget")
	}
	x.snap.Stats.ConstructionWork++
	return nil
}
func emptyPatterns(c PatternConfig) *PatternIndex {
	return &PatternIndex{cfg: c, dags: map[string]*PatternDAG{}, prefix: map[string]map[patternPrefix]int{}, seen: map[string]bool{}, connections: map[string]model.Connection{}, footpaths: map[string]model.Footpath{}, snap: PatternSnapshot{Profile: "scalable_tp_component_v1", Config: c, Witnesses: []PatternWitness{}, PhasesNS: map[string]int64{}}}
}
func BuildScalable(ctx context.Context, d model.Dataset, c ScalableConfig) (x *ScalableIndex, err error) {
	x = &ScalableIndex{cfg: c, snap: ScalableSnapshot{Profile: "scalable_tp_explicit_hierarchy_positive_nonbinding_v4", Config: c, Cells: map[string]int{}, Borders: []string{}, PhasesNS: map[string]int64{}, PhaseCosts: map[string]ScalablePhaseCost{}}}
	if c.BorderSearchMode == "" {
		c.BorderSearchMode = "border"
		x.cfg.BorderSearchMode = "border"
		x.snap.Config.BorderSearchMode = "border"
	}
	if c.DirectIndex == "" {
		c.DirectIndex = "incidence"
	}
	if c.BorderStorage == "" {
		c.BorderStorage = "source"
	}
	x.cfg.DirectIndex, x.cfg.BorderStorage = c.DirectIndex, c.BorderStorage
	x.snap.Config.DirectIndex, x.snap.Config.BorderStorage = c.DirectIndex, c.BorderStorage
	x.localCache = map[scalableLocalKey][]scalableLocalResult{}
	x.localGraphs = map[int]map[int][]PatternEdge{}
	x.cfg.LongDistanceTrips = slices.Clone(c.LongDistanceTrips)
	x.cfg.Hubs = slices.Clone(c.Hubs)
	phaseName := ""
	phaseStart := time.Now()
	var phaseMem runtime.MemStats
	phase := func(name string) {
		var now runtime.MemStats
		runtime.ReadMemStats(&now)
		if phaseName != "" {
			elapsed := time.Since(phaseStart).Nanoseconds()
			x.snap.PhasesNS[phaseName] = elapsed
			x.snap.PhaseCosts[phaseName] = ScalablePhaseCost{elapsed, now.TotalAlloc - phaseMem.TotalAlloc, now.HeapAlloc}
		}
		phaseName = name
		phaseStart = time.Now()
		phaseMem = now
	}
	defer phase("")
	x.cfg.Partition = map[string]int{}
	for k, v := range c.Partition {
		x.cfg.Partition[k] = v
	}
	defer func() {
		x.localCache = nil
		x.longGraphs = nil
		if x.directCache != nil {
			x.directCache.open = false
		}
		if x.shared != nil {
			x.shared.finish()
		}
		x.localGraphs = nil
		x.snap.Complete = err == nil
		if err != nil {
			x.snap.Error = err.Error()
		}
		for _, p := range []*PatternIndex{x.local, x.border, x.long} {
			if p != nil {
				p.snap.Complete = err == nil
				p.prefix = nil
				p.seen = nil
				p.buildCharge = nil
				p.patternSink = nil
			}
		}
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	pc := c.Patterns
	fc := pc.Profile
	l := fc.Limits
	if (c.DirectIndex != "incidence" && c.DirectIndex != "pairs" && c.DirectIndex != "demand") || (c.DirectIndex != "incidence" && c.MaxDirectPairs < 1) || (c.BorderStorage != "source" && c.BorderStorage != "cluster") || (c.BorderStorage == "cluster" && c.MaxClusterDAGs < 1) || (c.BorderSearchMode != "border" && c.BorderSearchMode != "expanded") || c.MaxLocalCacheEntries < 0 || (c.BorderMergeMaxSize != 0 || c.BorderReductionPercent != 0) && (c.BorderMergeMaxSize < c.MaxClusterSize || c.BorderReductionPercent < 1 || c.BorderReductionPercent > 100) || c.MaxClusterSize < 1 || c.MaxDirectArcs < 1 || c.MaxProfileRuns < 1 || c.MaxProfileLabels < 1 || pc.Profile.Cells != 1 || pc.MaxPatterns < 1 || pc.MaxNodes < 1 || pc.MaxEdges < 1 || pc.MaxDirectEntries < 1 || pc.MaxBuildWork < 1 || pc.MaxQueryWork < 1 || pc.MaxQueryLabels < 1 || fc.MaxBoardings < 1 || fc.MaxBoardings > 32 || l.MaxStops < 1 || l.MaxTrips < 1 || l.MaxClosurePaths < 1 || l.MaxWalkSettled < 1 || l.MaxTransfers < 1 || l.MaxQuerySegments < 1 || l.MaxWork < 1 {
		err = fmt.Errorf("Scalable TP requires positive explicit budgets and one direct backend cell")
		return
	}
	if len(d.Stops) > l.MaxStops || len(d.Trips) > l.MaxTrips {
		err = fmt.Errorf("Scalable TP incomplete: network size budget")
		return
	}
	if err = d.Validate(); err != nil {
		return
	}
	if c.Patterns.Profile.Semantics != "" || c.Patterns.Profile.ProfilePolicy != "" {
		err = fmt.Errorf("Scalable TP capability: semantic profile policy not qualified")
		return
	}
	if len(d.Transfers) > 0 {
		err = fmt.Errorf("Scalable TP capability: GTFS transfer rules unsupported")
		return
	}
	for _, v := range d.Connections {
		if !v.Pickup || !v.Dropoff || v.Arrival <= v.Departure || v.From == v.To {
			err = fmt.Errorf("Scalable TP capability: unrestricted positive non-self rides required")
			return
		}
	}
	base := &FlashIndex{cfg: fc, ids: map[string]int{}, snap: FlashSnapshot{PhasesNS: map[string]int64{}}}
	for _, s := range d.Stops {
		base.names = append(base.names, s.ID)
	}
	sort.Strings(base.names)
	for i, s := range base.names {
		base.ids[s] = i
	}
	phase("walking_closure")
	if err = base.closeWalks(d, &flashWork{ctx, base}); err != nil {
		return
	}
	phase("direct_timetable_without_transfers")
	base.base, err = newIndex(ctx, d, Config{StrictLineOrder: true, TransferWalkingLimit: base.snap.ClosureDiameter, MaxTransfers: l.MaxTransfers, MaxQueuedSegments: l.MaxQuerySegments}, false)
	if err != nil {
		return
	}
	base.cp = base.base
	base.snap.Profile = "scalable_tp_direct_backend_without_transfers_v2"
	for _, line := range base.cp.lines {
		ids := []string{}
		for _, ti := range line.trips {
			ids = append(ids, base.cp.trips[ti].id)
		}
		base.snap.Lines = append(base.snap.Lines, ids)
	}
	base.snap.Complete = true

	x.local = emptyPatterns(pc)
	x.border = emptyPatterns(pc)
	for _, p := range []*PatternIndex{x.local, x.border} {
		p.buildCharge = x.charge
		p.base = base
		for _, v := range d.Connections {
			p.connections[v.ID] = v
		}
		for _, v := range d.Footpaths {
			p.footpaths[v.ID] = v
		}
	}
	x.local.incident = make([][]patternOccurrence, len(base.names))
	for li, line := range base.cp.lines {
		t := base.cp.trips[line.trips[0]]
		for pos := 0; pos <= len(t.cs); pos++ {
			if err = x.tick(ctx); err != nil {
				return
			}
			s := t.cs[0].From
			if pos > 0 {
				s = t.cs[pos-1].To
			}
			x.local.incident[base.ids[s]] = append(x.local.incident[base.ids[s]], patternOccurrence{li, pos})
			x.local.snap.Stats.DirectEntries += len(line.trips)
			if x.local.snap.Stats.DirectEntries > pc.MaxDirectEntries {
				err = fmt.Errorf("Scalable TP incomplete: direct incidence budget")
				return
			}
		}
	}
	x.border.incident = x.local.incident
	if c.DirectIndex == "demand" {
		x.directCache = &patternDirectCache{open: true, limit: c.MaxDirectPairs, pairs: map[[2]int][]patternLinePair{}}
		x.local.pairCache = x.directCache
		x.border.pairCache = x.directCache
	}
	phase("hierarchy_classification")
	if err = x.classifyHierarchy(ctx, d); err != nil {
		return
	}
	if c.DirectIndex == "pairs" {
		phase("direct_line_pairs")
		if err = x.indexDirectPairs(ctx); err != nil {
			return
		}
	}
	phase("partition")
	if err = x.partition(ctx, d); err != nil {
		return
	}
	phase("border_reduction")
	if err = x.reduceBorders(ctx); err != nil {
		return
	}
	phase("direct_arcs")
	localSets := make([]map[PatternEdge]bool, x.snap.Stats.Clusters)
	for i := range localSets {
		localSets[i] = map[PatternEdge]bool{}
	}
	cross := map[PatternEdge]bool{}
	border := make([]bool, len(base.names))
	arcs := 0
	add := func(dst map[PatternEdge]bool, e PatternEdge) error {
		if err := x.tick(ctx); err != nil {
			return err
		}
		if !dst[e] {
			if arcs >= c.MaxDirectArcs {
				return fmt.Errorf("Scalable TP incomplete: direct arc budget")
			}
			dst[e] = true
			arcs++
		}
		return nil
	}
	for _, line := range base.cp.lines {
		t := base.cp.trips[line.trips[0]]
		visited := map[int]bool{}
		for _, v := range t.cs {
			visited[x.cells[base.ids[v.From]]] = true
			visited[x.cells[base.ids[v.To]]] = true
		}
		if len(visited) > 1 {
			for _, v := range t.cs {
				border[base.ids[v.From]] = true
				border[base.ids[v.To]] = true
			}
		}
		for i, a := range t.cs {
			for j := i; j < len(t.cs); j++ {
				b := t.cs[j]
				if a.From == b.To {
					continue
				}
				e := PatternEdge{a.From, b.To, "ride"}
				ca, cb := x.cells[base.ids[a.From]], x.cells[base.ids[b.To]]
				if ca == cb {
					err = add(localSets[ca], e)
				} else {
					err = add(cross, e)
				}
				if err != nil {
					return
				}
			}
		}
	}
	for u, paths := range base.walks {
		for _, p := range paths {
			if p.Duration > 0 {
				if err = add(localSets[x.cells[u]], PatternEdge{base.names[u], p.To, "walk"}); err != nil {
					return
				}
			}
		}
	}
	x.borders = make([][]int, len(localSets))
	for u, b := range border {
		if b {
			x.borders[x.cells[u]] = append(x.borders[x.cells[u]], u)
			x.snap.Borders = append(x.snap.Borders, base.names[u])
		}
	}
	x.snap.Stats.BorderStops = len(x.snap.Borders)
	x.borderSet = border
	x.snap.LocalSufficient = make([]bool, len(localSets))
	for i := range x.snap.LocalSufficient {
		x.snap.LocalSufficient[i] = !c.DisableLocalSufficiency
	}
	if err = x.initializeHierarchy(ctx); err != nil {
		return
	}
	phase("local_profiles")
	for cell, set := range localSets {
		graph := sortedPatternEdges(set)
		x.snap.Stats.LocalArcs += len(graph)
		targets := map[int]bool{}
		sources := []int{}
		for u, c := range x.cells {
			if c == cell {
				sources = append(sources, u)
				targets[u] = true
			}
		}
		if len(graph) == 0 {
			continue
		}
		if err = x.profiles(ctx, x.local, graph, sources, targets, false); err != nil {
			return
		}
	}
	if len(x.longStations) > 0 {
		phase("long_distance_profiles")
		if err = x.buildLongPatterns(ctx, sortedPatternEdges(cross)); err != nil {
			return
		}
	}
	phase("border_graph_assembly")
	// Only the ablation pushes local-pattern interiors into the global queue.
	if c.BorderSearchMode == "expanded" {
		ps := PatternQueryStats{}
		for _, bs := range x.borders {
			for _, u := range bs {
				for _, v := range bs {
					if err = x.local.extract(base.names[u], base.names[v], cross, &ps, func() error { return x.tick(ctx) }); err != nil {
						return
					}
				}
			}
		}
	}
	if len(x.longStations) > 0 {
		cross, err = x.localCrossGraph(ctx)
		if err != nil {
			return
		}
	}
	borderGraph := sortedPatternEdges(cross)
	if len(borderGraph) > c.MaxDirectArcs {
		err = fmt.Errorf("Scalable TP incomplete: border graph budget")
		return
	}
	x.snap.Stats.BorderGraphArcs = len(borderGraph)
	if c.BorderStorage == "cluster" {
		x.shared = newClusterPatterns(c.MaxClusterDAGs, pc.MaxNodes, pc.MaxEdges, x.tick)
		x.border.snap.Storage = "cluster_dags"
		x.border.patternSink = func(ctx context.Context, w PatternWitness) error {
			return x.shared.add(ctx, x.cells[base.ids[w.Target]], w.Edges)
		}
	}
	phase("border_profiles")
	targets := map[int]bool{}
	sources := []int{}
	for u, b := range border {
		if b {
			targets[u] = true
			sources = append(sources, u)
		}
	}
	if err = x.profiles(ctx, x.border, borderGraph, sources, targets, true); err != nil {
		return
	}
	for _, ok := range x.snap.LocalSufficient {
		if ok {
			x.snap.Stats.LocalSufficientCells++
		}
	}
	x.localCache = nil
	x.localGraphs = nil
	return
}
func sortedPatternEdges(set map[PatternEdge]bool) []PatternEdge {
	a := make([]PatternEdge, 0, len(set))
	for e := range set {
		a = append(a, e)
	}
	sort.Slice(a, func(i, j int) bool {
		if a[i].From != a[j].From {
			return a[i].From < a[j].From
		}
		if a[i].To != a[j].To {
			return a[i].To < a[j].To
		}
		return a[i].Kind < a[j].Kind
	})
	return a
}
func (x *ScalableIndex) Search(ctx context.Context, q model.Query) (PatternResult, error) {
	if e := ctx.Err(); e != nil {
		return PatternResult{Journeys: []model.Journey{}, Error: e.Error()}, e
	}
	if !x.Complete() {
		e := fmt.Errorf("Scalable TP incomplete index cannot answer queries")
		return PatternResult{Journeys: []model.Journey{}, Error: e.Error()}, e
	}
	return x.local.searchGraph(ctx, q, func(q model.Query, edges map[PatternEdge]bool, s *PatternQueryStats, tick func() error) error {
		base := x.local.base
		sc, tc := x.cells[base.ids[q.Origin]], x.cells[base.ids[q.Destination]]
		if sc == tc {
			if e := x.local.extract(q.Origin, q.Destination, edges, s, tick); e != nil {
				return e
			}
			if x.snap.LocalSufficient[sc] {
				s.LocalOnly = true
				return nil
			}
		}
		for _, u := range x.borders[sc] {
			if e := tick(); e != nil {
				return e
			}
			if e := x.local.extract(q.Origin, base.names[u], edges, s, tick); e != nil {
				return e
			}
			if x.shared == nil {
				for _, v := range x.borders[tc] {
					if e := tick(); e != nil {
						return e
					}
					if e := x.border.extract(base.names[u], base.names[v], edges, s, tick); e != nil {
						return e
					}
				}
			}
		}
		if x.shared != nil {
			sources := []string{}
			targets := []string{}
			for _, u := range x.borders[sc] {
				sources = append(sources, base.names[u])
			}
			for _, v := range x.borders[tc] {
				targets = append(targets, base.names[v])
			}
			if e := x.shared.extract(tc, sources, targets, edges, s, tick); e != nil {
				return e
			}
		}
		for _, v := range x.borders[tc] {
			if e := tick(); e != nil {
				return e
			}
			if e := x.local.extract(base.names[v], q.Destination, edges, s, tick); e != nil {
				return e
			}
		}
		return nil
	})
}

// Profiles enumerate all walking-adjusted boarding breakpoints plus time zero.
// Each run uses fresh labels: no retained timestamp or canonical-TB pruning.
func (x *ScalableIndex) profiles(ctx context.Context, store *PatternIndex, graph []PatternEdge, sources []int, targets map[int]bool, border bool) error {
	mode := "local"
	if border {
		mode = "border"
	}
	return x.profilesMode(ctx, store, graph, sources, targets, mode)
}
func (x *ScalableIndex) profilesMode(ctx context.Context, store *PatternIndex, graph []PatternEdge, sources []int, targets map[int]bool, mode string) error {
	border := mode == "border"
	base := x.local.base
	adj := map[int][]PatternEdge{}
	for _, e := range graph {
		adj[base.ids[e.From]] = append(adj[base.ids[e.From]], e)
	}
	keys := []int{}
	for t := range targets {
		keys = append(keys, t)
	}
	sort.Ints(keys)
	for _, source := range sources {
		times := map[int64]bool{0: true}
		for _, p := range base.walks[source] {
			for _, o := range x.local.incident[base.ids[p.To]] {
				line := base.cp.lines[o.line]
				for _, ti := range line.trips {
					if e := x.tick(ctx); e != nil {
						return e
					}
					t := base.cp.trips[ti]
					if o.pos == len(t.cs) {
						continue
					}
					dep := t.cs[o.pos].Departure - p.Duration
					if dep >= 0 {
						times[dep] = true
					}
				}
			}
		}
		sorted := []int64{}
		for t := range times {
			sorted = append(sorted, t)
		}
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] > sorted[j] })
		for _, dep := range sorted {
			if x.snap.Stats.LocalRuns+x.snap.Stats.BorderRuns+x.snap.Stats.LongDistanceRuns >= x.cfg.MaxProfileRuns {
				return fmt.Errorf("Scalable TP incomplete: profile run budget")
			}
			if mode == "long" {
				x.snap.Stats.LongDistanceRuns++
			} else if border {
				x.snap.Stats.BorderRuns++
			} else {
				x.snap.Stats.LocalRuns++
			}
			var labels map[patternState]*patternLabel
			var e error
			if mode == "long" {
				labels, e = x.portalProfileRun(ctx, source, dep, adj, true)
			} else if border && (x.cfg.BorderSearchMode == "border" || len(x.longStations) > 0) {
				labels, e = x.borderProfileRun(ctx, source, dep, adj)
			} else {
				labels, e = x.profileRun(ctx, source, dep, adj)
			}
			if e != nil {
				return e
			}
			if mode == "long" {
				if e = x.recordAccess(ctx, source, labels); e != nil {
					return e
				}
			}
			if border && !x.cfg.DisableLocalSufficiency {
				if e = x.certifyLocal(ctx, source, dep, labels); e != nil {
					return e
				}
			}
			for _, target := range keys {
				if target == source {
					continue
				}
				best := paper.Infinity
				for b := 0; b <= x.cfg.Patterns.Profile.MaxBoardings; b++ {
					var chosen *patternLabel
					for _, walked := range []bool{false, true} {
						l := labels[patternState{target, b, walked}]
						if l != nil && (chosen == nil || l.at < chosen.at) {
							chosen = l
						}
					}
					if chosen == nil || chosen.at >= best {
						continue
					}
					best = chosen.at
					store.snap.Stats.ProfileJourneys++
					if e := store.addPattern(ctx, base.names[source], base.names[target], dep, paper.Journey(chosen.parent)); e != nil {
						return e
					}
				}
			}
		}
	}
	return nil
}
func (x *ScalableIndex) profileRun(ctx context.Context, source int, dep int64, adj map[int][]PatternEdge) (map[patternState]*patternLabel, error) {
	return x.localRun(ctx, source, dep, false, x.cfg.Patterns.Profile.MaxBoardings, adj)
}

// Parent traces are constructed only after a candidate survives objective
// dominance and the label budget. This changes allocations, not pruning rules.
func (x *ScalableIndex) localRun(ctx context.Context, source int, dep int64, initialWalked bool, maxBoardings int, adj map[int][]PatternEdge) (map[patternState]*patternLabel, error) {
	return x.localRunMode(ctx, source, dep, initialWalked, maxBoardings, adj, false)
}
func (x *ScalableIndex) localRunMode(ctx context.Context, source int, dep int64, initialWalked bool, maxBoardings int, adj map[int][]PatternEdge, pruneHubs bool) (map[patternState]*patternLabel, error) {
	base := x.local.base
	labels := map[patternState]*patternLabel{}
	queue := &patternQueue{}
	count := 0
	stats := PatternQueryStats{}
	tick := func() error { return x.tick(ctx) }
	merge := func(stop, b int, walked bool, at int64, parent func() (*paper.Node, error)) error {
		if e := tick(); e != nil {
			return e
		}
		if at > model.MaxServiceSecond {
			return nil
		}
		for n := 0; n <= b; n++ {
			if old := labels[patternState{stop, n, walked}]; old != nil && old.at <= at {
				return nil
			}
		}
		if count >= x.cfg.MaxProfileLabels {
			return fmt.Errorf("Scalable TP incomplete: profile label budget")
		}
		p, e := parent()
		if e != nil {
			return e
		}
		l := &patternLabel{stop, b, walked, at, p, count}
		count++
		x.snap.Stats.ProfileLabels++
		labels[patternState{stop, b, walked}] = l
		heap.Push(queue, l)
		return nil
	}
	if e := merge(source, 0, initialWalked, dep, func() (*paper.Node, error) { return nil, nil }); e != nil {
		return nil, e
	}
	for queue.Len() > 0 {
		if e := tick(); e != nil {
			return nil, e
		}
		l := heap.Pop(queue).(*patternLabel)
		if labels[patternState{l.stop, l.boards, l.walked}] != l {
			continue
		}
		if pruneHubs && x.hubs[l.stop] {
			continue
		}
		for _, edge := range adj[l.stop] {
			if e := tick(); e != nil {
				return nil, e
			}
			v := base.ids[edge.To]
			if edge.Kind == "walk" {
				if l.walked {
					continue
				}
				path := base.walkMap[l.stop][v]
				if path == nil {
					return nil, fmt.Errorf("Scalable TP internal: missing walk")
				}
				if e := merge(v, l.boards, true, l.at+path.Duration, func() (*paper.Node, error) {
					p, at := l.parent, l.at
					for _, w := range path.Edges {
						if e := tick(); e != nil {
							return nil, e
						}
						p = &paper.Node{Previous: p, Leg: model.Leg{Kind: "walk", Ref: w.ID, Departure: at, Arrival: at + w.Duration}}
						at += w.Duration
					}
					return p, nil
				}); e != nil {
					return nil, e
				}
			} else {
				if l.boards == maxBoardings {
					continue
				}
				r, ok, e := x.local.direct(l.stop, v, l.at, &stats, tick)
				if e != nil {
					return nil, e
				}
				if !ok {
					continue
				}
				cs := base.cp.trips[r.trip].cs[r.board:r.exit]
				if e := merge(v, l.boards+1, false, cs[len(cs)-1].Arrival, func() (*paper.Node, error) {
					p := l.parent
					for _, c := range cs {
						if e := tick(); e != nil {
							return nil, e
						}
						p = paper.Ride(p, c)
					}
					return p, nil
				}); e != nil {
					return nil, e
				}
			}
		}
	}
	return labels, nil
}
