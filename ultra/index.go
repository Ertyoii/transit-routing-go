// Package ultra implements the paper's independent canonical two-round ULTRA
// shortcut construction and a stop-to-stop ULTRA-RAPTOR query. Optimized range
// reuse and Bucket-CH are not claimed. See docs/ULTRA_CONTRACT.md.
package ultra

import (
	"context"
	"fmt"
	"github.com/ertyoii/transit-routing-go/corech"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type Limits struct {
	MaxRuns             int `json:"max_runs"`
	MaxConnectionVisits int `json:"max_connection_visits"`
	MaxWalkSettled      int `json:"max_walk_settled"`
	MaxWalkEdges        int `json:"max_walk_edges"`
	MaxShortcuts        int `json:"max_shortcuts"`
	MaxOutputLegs       int `json:"max_output_legs"`
}
type Stats struct {
	RouteOccurrenceChecks int `json:"route_occurrence_checks"`
	EqualLabelRepairs     int `json:"equal_label_repairs"`
	RetainedLabelPrunes   int `json:"retained_label_prunes"`
	PrefixCopies          int `json:"absolute_prefix_copies"`
	Runs                  int `json:"runs"`
	GroupingComparisons   int `json:"grouping_comparisons"`
	TripProbes            int `json:"trip_probes"`
	RouteScans            int `json:"route_scans"`
	ConnectionVisits      int `json:"connection_visits"`
	WalkSettled           int `json:"walk_settled"`
	WalkEdges             int `json:"walk_edges"`
	CandidateEndpoints    int `json:"candidate_endpoints"`
	NoncandidateEndpoints int `json:"noncandidate_endpoints"`
	DuplicateShortcuts    int `json:"duplicate_shortcuts"`
	OutputLegs            int `json:"output_legs"`
}
type Config struct {
	ActiveRoutes    bool          `json:"active_routes"`
	ReuseDepartures bool          `json:"reuse_departures"`
	RouteMode       string        `json:"route_mode"`
	Core            corech.Config `json:"core"`
	Limits          Limits        `json:"limits"`
}
type Shortcut struct {
	From      string        `json:"from"`
	To        string        `json:"to"`
	Duration  int64         `json:"duration"`
	RawEdges  []string      `json:"raw_edges"`
	Source    string        `json:"supporting_source"`
	Departure int64         `json:"supporting_departure"`
	Target    string        `json:"supporting_target"`
	Candidate model.Journey `json:"supporting_candidate"`
}
type Run struct {
	Source       string `json:"source"`
	Departure    int64  `json:"departure"`
	Complete     bool   `json:"complete"`
	Candidates   int    `json:"candidate_endpoints"`
	NewShortcuts int    `json:"new_shortcuts"`
}
type Snapshot struct {
	Routes      []RouteSummary `json:"routes"`
	Complete    bool           `json:"complete"`
	Error       string         `json:"error,omitempty"`
	Profile     string         `json:"profile"`
	Config      Config         `json:"config"`
	CoreStats   corech.Stats   `json:"core_stats"`
	Stats       Stats          `json:"stats"`
	Runs        []Run          `json:"runs"`
	Shortcuts   []Shortcut     `json:"shortcuts"`
	Sources     int            `json:"sources"`
	PlannedRuns int            `json:"planned_runs"`
}
type arc struct {
	to, id   int
	duration int64
}
type trip struct {
	id string
	cs []model.Connection
}
type Index struct {
	p              *paper.Profile
	s              Snapshot
	core           corech.Snapshot
	ids            map[string]int
	names          []string
	graph          [][]arc
	routes         []route
	at             [][]occurrence
	allRoutes      []occurrence
	outgoing       map[string][]int
	raw            map[string]model.Footpath
	rawOut, rawIn  map[string][]model.Footpath
	transit        map[string]bool
	maximumArrival int64
}

func (x *Index) Snapshot() Snapshot {
	s := x.s
	s.Routes = append([]RouteSummary{}, s.Routes...)
	for i := range s.Routes {
		s.Routes[i].TripIDs = append([]string{}, s.Routes[i].TripIDs...)
		s.Routes[i].Stops = append([]string{}, s.Routes[i].Stops...)
	}
	s.Runs = append([]Run{}, s.Runs...)
	s.Shortcuts = append([]Shortcut{}, s.Shortcuts...)
	for i := range s.Shortcuts {
		s.Shortcuts[i].RawEdges = append([]string{}, s.Shortcuts[i].RawEdges...)
		s.Shortcuts[i].Candidate.Legs = append([]model.Leg{}, s.Shortcuts[i].Candidate.Legs...)
	}
	return s
}
func (x *Index) CoreSnapshot() corech.Snapshot {
	s := x.core
	s.Nodes = append([]corech.Node{}, s.Nodes...)
	s.Arcs = append([]corech.Arc{}, s.Arcs...)
	s.CoreArcIDs = append([]int{}, s.CoreArcIDs...)
	return s
}
func (x *Index) Complete() bool { return x.s.Complete }
func validLimits(l Limits) bool {
	return l.MaxRuns > 0 && l.MaxConnectionVisits > 0 && l.MaxWalkSettled > 0 && l.MaxWalkEdges > 0 && l.MaxShortcuts > 0 && l.MaxOutputLegs > 0
}

// Build performs complete two-round candidate/witness searches at each source
// departure. ReuseDepartures optionally retains per-source labels across runs
// with timestamped equal-label repair; otherwise each run starts fresh.
func Build(ctx context.Context, d model.Dataset, c Config) (x *Index, err error) {
	x = &Index{s: Snapshot{Profile: "ultra_positive_unrestricted_nonbinding_caps_v2", Config: c, Runs: []Run{}, Shortcuts: []Shortcut{}}, ids: map[string]int{}, raw: map[string]model.Footpath{}, rawOut: map[string][]model.Footpath{}, rawIn: map[string][]model.Footpath{}, transit: map[string]bool{}, outgoing: map[string][]int{}}
	defer func() {
		x.s.Complete = err == nil
		if err != nil {
			x.s.Error = err.Error()
		}
	}()
	if !validLimits(c.Limits) {
		err = fmt.Errorf("ULTRA requires explicit positive work/output limits")
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	if c.ReuseDepartures && !c.ActiveRoutes {
		err = fmt.Errorf("ULTRA departure reuse requires active-route collection")
		return
	}
	if c.RouteMode != "" && c.RouteMode != IndependentTrips && c.RouteMode != NonOvertakingGroups {
		err = fmt.Errorf("ULTRA unknown route mode %q", c.RouteMode)
		return
	}
	if c.RouteMode == "" {
		c.RouteMode = IndependentTrips
		x.s.Config.RouteMode = c.RouteMode
	}
	if len(d.Transfers) != 0 {
		err = fmt.Errorf("ULTRA capability: GTFS stop-pair rules are not implicit departure buffers; unsupported")
		return
	}
	for _, e := range d.Connections {
		if !e.Pickup || !e.Dropoff {
			err = fmt.Errorf("ULTRA capability: boarding/alighting restrictions require separate preprocessing proof")
			return
		}
	}
	x.p, err = paper.New(d)
	if err != nil {
		return
	}
	byTrip := map[string][]model.Connection{}
	departures := map[string]map[int64]bool{}
	for _, e := range x.p.Data.Connections {
		byTrip[e.TripID] = append(byTrip[e.TripID], e)
		x.transit[e.From] = true
		x.transit[e.To] = true
		x.maximumArrival = max(x.maximumArrival, e.Arrival)
		if departures[e.From] == nil {
			departures[e.From] = map[int64]bool{}
		}
		departures[e.From][e.Departure] = true
	}
	if len(byTrip) == 0 {
		err = fmt.Errorf("ULTRA requires a nonempty timetable")
		return
	}
	w := work{ctx: ctx, l: c.Limits, s: &x.s.Stats}
	if err = x.buildRoutes(byTrip, c.RouteMode, &w); err != nil {
		return
	}
	vertices, protected := []string{}, []string{}
	for _, v := range x.p.Data.Stops {
		vertices = append(vertices, v.ID)
		if x.transit[v.ID] {
			protected = append(protected, v.ID)
		}
	}
	for _, e := range x.p.Data.Footpaths {
		x.raw[e.ID] = e
		x.rawOut[e.From] = append(x.rawOut[e.From], e)
		x.rawIn[e.To] = append(x.rawIn[e.To], e)
	}
	for _, m := range []map[string][]model.Footpath{x.rawOut, x.rawIn} {
		for v := range m {
			sort.Slice(m[v], func(i, j int) bool { return m[v][i].ID < m[v][j].ID })
		}
	}
	// Target is the transit set; an average-degree stop may retain extra vertices.
	c.Core.TargetCoreVertices = len(protected)
	x.s.Config.Core = c.Core
	var ch *corech.Index
	ch, err = corech.Build(ctx, vertices, x.p.Data.Footpaths, protected, c.Core)
	x.s.CoreStats = ch.Stats()
	x.core = ch.Snapshot()
	if err != nil {
		return
	}
	for _, v := range x.core.Nodes {
		if v.Core {
			x.ids[v.ID] = len(x.names)
			x.names = append(x.names, v.ID)
		}
	}
	x.graph = make([][]arc, len(x.names))
	for _, id := range x.core.CoreArcIDs {
		a := x.core.Arcs[id]
		u := x.ids[x.core.Nodes[a.From].ID]
		v := x.ids[x.core.Nodes[a.To].ID]
		x.graph[u] = append(x.graph[u], arc{v, id, a.Duration})
	}
	for u := range x.graph {
		sort.Slice(x.graph[u], func(i, j int) bool {
			a, b := x.graph[u][i], x.graph[u][j]
			if a.to == b.to {
				return a.id < b.id
			}
			return a.to < b.to
		})
	}
	x.at = make([][]occurrence, len(x.names))
	for ri, r := range x.routes {
		x.allRoutes = append(x.allRoutes, occurrence{ri, 0})
		for pos, co := range r.trips[0].cs {
			v := x.ids[co.From]
			x.at[v] = append(x.at[v], occurrence{ri, pos})
		}
	}
	sources := []string{}
	for s, ts := range departures {
		sources = append(sources, s)
		x.s.PlannedRuns += len(ts)
	}
	sort.Strings(sources)
	x.s.Sources = len(sources)
	seen := map[[2]string]int{}
	for _, source := range sources {
		direct := emptyLabels(len(x.names))
		direct[x.ids[source]] = label{time: 0}
		if err = x.relaxCore(direct, x.maximumArrival, true, -1, &w); err != nil {
			return
		}
		times := []int64{}
		for t := range departures[source] {
			times = append(times, t)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] > times[j] })
		var retained rangeState
		if c.ReuseDepartures {
			retained = newRangeState(len(x.names))
		}
		for _, departure := range times {
			if err = w.check(); err != nil {
				return
			}
			if x.s.Stats.Runs == c.Limits.MaxRuns {
				err = fmt.Errorf("ULTRA incomplete: departure-run budget exhausted")
				return
			}
			x.s.Stats.Runs++
			run := Run{Source: source, Departure: departure}
			x.s.Runs = append(x.s.Runs, run)
			ri := len(x.s.Runs) - 1
			var prev []label
			if c.ReuseDepartures {
				prev, err = x.runRange(direct, departure, ri+1, &retained, &w)
				if err != nil {
					return
				}
			} else {
				prev = append([]label{}, direct...)
				for i := range prev {
					if prev[i].time < paper.Infinity {
						prev[i].time += departure
					}
				}
				for round := 0; round < 2; round++ {
					curr := append([]label{}, prev...)
					if err = x.scan(prev, curr, x.maximumArrival, -1, &w); err != nil {
						return
					}
					if err = x.relaxCore(curr, x.maximumArrival, false, -1, &w); err != nil {
						return
					}
					prev = curr
				}
			}
			for target, lab := range prev {
				if !x.transit[x.names[target]] || lab.time == paper.Infinity || lab.trips != 2 {
					continue
				}
				var j model.Journey
				j, err = x.materialize(lab.path, departure, &w)
				if err != nil {
					return
				}
				from, to, refs, duration, candidate := x.candidate(j)
				if !candidate {
					x.s.Stats.NoncandidateEndpoints++
					continue
				}
				x.s.Stats.CandidateEndpoints++
				x.s.Runs[ri].Candidates++
				if len(refs) == 0 {
					continue
				}
				key := [2]string{from, to}
				if old, ok := seen[key]; ok {
					if x.s.Shortcuts[old].Duration != duration {
						err = fmt.Errorf("ULTRA invariant: inconsistent shortest shortcut duration")
						return
					}
					x.s.Stats.DuplicateShortcuts++
					continue
				}
				if len(x.s.Shortcuts) == c.Limits.MaxShortcuts {
					err = fmt.Errorf("ULTRA incomplete: shortcut budget exhausted")
					return
				}
				seen[key] = len(x.s.Shortcuts)
				x.outgoing[from] = append(x.outgoing[from], len(x.s.Shortcuts))
				x.s.Shortcuts = append(x.s.Shortcuts, Shortcut{From: from, To: to, Duration: duration, RawEdges: refs, Source: source, Departure: departure, Target: x.names[target], Candidate: j})
				x.s.Runs[ri].NewShortcuts++
			}
			x.s.Runs[ri].Complete = true
		}
	}
	return
}
func (x *Index) candidate(j model.Journey) (from, to string, refs []string, duration int64, ok bool) {
	refs = []string{}
	if len(j.Legs) == 0 || j.Legs[0].Kind != "ride" || j.Legs[len(j.Legs)-1].Kind != "ride" {
		return
	}
	stage := 0
	for _, l := range j.Legs {
		if l.Kind == "walk" {
			if stage == 2 {
				return
			}
			stage = 1
			e := x.raw[l.Ref]
			if len(refs) == 0 {
				from = e.From
			}
			to = e.To
			duration += e.Duration
			refs = append(refs, l.Ref)
		} else if stage == 1 {
			stage = 2
		}
	}
	// Empty intermediate transfers need no edge. Actual two-trip count is checked
	// by the search label and independently by every supporting itinerary audit.
	ok = true
	return
}
