// Basic exact Transfer Patterns: ESA2010 sections4 and6, ALENEX2016 section2.1.
// The shared canonical Profile-TB backend streams all useful journeys, including
// journeys that set no new FLASH flag. See docs/TRANSFER_PATTERNS_CONTRACT.md.
package tripbased

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
	"time"
)

type PatternConfig struct {
	Profile          FlashConfig `json:"profile"`
	MaxPatterns      int         `json:"max_patterns"`
	MaxNodes         int         `json:"max_nodes"`
	MaxEdges         int         `json:"max_edges"`
	MaxDirectEntries int         `json:"max_direct_entries"`
	MaxBuildWork     int         `json:"max_build_work"`
	MaxQueryWork     int         `json:"max_query_work"`
	MaxQueryLabels   int         `json:"max_query_labels"`
}
type PatternNode struct {
	Stop   string `json:"stop"`
	Parent int    `json:"parent"`
	Kind   string `json:"kind"`
}
type PatternArc struct {
	Parent int    `json:"parent"`
	Kind   string `json:"kind"`
}
type PatternDAG struct {
	Source  string                  `json:"source"`
	Nodes   []PatternNode           `json:"nodes"`
	Targets map[string][]PatternArc `json:"targets"`
}
type PatternEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}
type PatternWitness struct {
	Source    string        `json:"source"`
	Target    string        `json:"target"`
	Departure int64         `json:"departure"`
	Edges     []PatternEdge `json:"edges"`
	Journey   model.Journey `json:"journey"`
}
type PatternStats struct {
	Work            int `json:"work"`
	ProfileJourneys int `json:"profile_journeys"`
	Patterns        int `json:"patterns"`
	Nodes           int `json:"nodes"`
	Edges           int `json:"edges"`
	DirectEntries   int `json:"direct_entries"`
}
type PatternSnapshot struct {
	Storage   string           `json:"storage,omitempty"`
	Profile   string           `json:"profile"`
	Complete  bool             `json:"complete"`
	Error     string           `json:"error,omitempty"`
	Config    PatternConfig    `json:"config"`
	Stats     PatternStats     `json:"stats"`
	DAGs      []PatternDAG     `json:"dags"`
	Witnesses []PatternWitness `json:"witnesses"`
	Backend   FlashSnapshot    `json:"backend"`
	PhasesNS  map[string]int64 `json:"phases_ns"`
}
type patternOccurrence struct{ line, pos int }
type patternPrefix struct {
	parent     int
	kind, stop string
}
type PatternIndex struct {
	pairCache   *patternDirectCache
	linePairs   map[[2]int][]patternLinePair
	patternSink func(context.Context, PatternWitness) error
	buildCharge func(context.Context) error // optional owning construction ledger
	base        *FlashIndex
	cfg         PatternConfig
	snap        PatternSnapshot
	dags        map[string]*PatternDAG
	prefix      map[string]map[patternPrefix]int
	seen        map[string]bool
	incident    [][]patternOccurrence
	connections map[string]model.Connection
	footpaths   map[string]model.Footpath
}

func (x *PatternIndex) Complete() bool { return x.snap.Complete }
func (x *PatternIndex) Snapshot() PatternSnapshot {
	s := x.snap
	s.Backend = x.baseSnapshot()
	s.DAGs = []PatternDAG{}
	keys := []string{}
	for k := range x.dags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d := *x.dags[k]
		d.Nodes = append([]PatternNode{}, d.Nodes...)
		d.Targets = map[string][]PatternArc{}
		for t, a := range x.dags[k].Targets {
			d.Targets[t] = append([]PatternArc{}, a...)
		}
		s.DAGs = append(s.DAGs, d)
	}
	s.Witnesses = append([]PatternWitness{}, x.snap.Witnesses...)
	for i := range s.Witnesses {
		s.Witnesses[i].Edges = append([]PatternEdge{}, s.Witnesses[i].Edges...)
		s.Witnesses[i].Journey.Legs = append([]model.Leg{}, s.Witnesses[i].Journey.Legs...)
	}
	s.PhasesNS = map[string]int64{}
	for k, v := range x.snap.PhasesNS {
		s.PhasesNS[k] = v
	}
	return s
}
func (x *PatternIndex) baseSnapshot() FlashSnapshot {
	if x.base == nil {
		return FlashSnapshot{}
	}
	return x.base.Snapshot()
}
func (x *PatternIndex) tick(ctx context.Context) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if x.snap.Stats.Work >= x.cfg.MaxBuildWork {
		return fmt.Errorf("Transfer Patterns incomplete: build work budget")
	}
	if x.buildCharge != nil {
		if e := x.buildCharge(ctx); e != nil {
			return e
		}
	}
	x.snap.Stats.Work++
	return nil
}
func BuildPatterns(ctx context.Context, d model.Dataset, c PatternConfig) (x *PatternIndex, err error) {
	x = &PatternIndex{cfg: c, dags: map[string]*PatternDAG{}, prefix: map[string]map[patternPrefix]int{}, seen: map[string]bool{}, connections: map[string]model.Connection{}, footpaths: map[string]model.Footpath{}, snap: PatternSnapshot{Profile: "basic_tp_positive_transitive_nonbinding_v1", Config: c, Witnesses: []PatternWitness{}, PhasesNS: map[string]int64{}}}
	defer func() {
		x.snap.Complete = err == nil
		if err != nil {
			x.snap.Error = err.Error()
		}
		if x.base != nil {
			x.base.profileSink = nil
		}
		x.prefix = nil
		x.seen = nil
	}()
	if err = ctx.Err(); err != nil {
		return
	}
	if c.Profile.Cells != 1 || c.MaxPatterns < 1 || c.MaxNodes < 1 || c.MaxEdges < 1 || c.MaxDirectEntries < 1 || c.MaxBuildWork < 1 || c.MaxQueryWork < 1 || c.MaxQueryLabels < 1 {
		err = fmt.Errorf("Transfer Patterns requires one backend cell and explicit positive budgets")
		return
	}
	for _, v := range d.Connections {
		if err = x.tick(ctx); err != nil {
			return
		}
		x.connections[v.ID] = v
	}
	for _, v := range d.Footpaths {
		if err = x.tick(ctx); err != nil {
			return
		}
		x.footpaths[v.ID] = v
	}
	start := time.Now()
	x.base, err = buildFlash(ctx, d, c.Profile, func(w FlashWitness) error {
		x.snap.Stats.ProfileJourneys++
		return x.addPattern(ctx, w.Source, w.Target, w.Departure, w.Journey)
	})
	x.snap.PhasesNS["canonical_backend_and_pattern_insertion"] = time.Since(start).Nanoseconds()
	if err != nil {
		return
	}
	start = time.Now()
	// Walking-only patterns are valid at every departure, including after service.
	for source, paths := range x.base.walks {
		for _, p := range paths {
			if err = x.tick(ctx); err != nil {
				return
			}
			if p.Duration == 0 {
				continue
			}
			j := paper.Journey(p.Append(nil, 0))
			if err = x.addPattern(ctx, x.base.names[source], p.To, 0, j); err != nil {
				return
			}
		}
	}
	x.snap.PhasesNS["walking_patterns"] = time.Since(start).Nanoseconds()
	start = time.Now()
	x.incident = make([][]patternOccurrence, len(x.base.names))
	for li, line := range x.base.cp.lines {
		t := x.base.cp.trips[line.trips[0]]
		for pos := 0; pos <= len(t.cs); pos++ {
			if err = x.tick(ctx); err != nil {
				return
			}
			stop := t.cs[0].From
			if pos > 0 {
				stop = t.cs[pos-1].To
			}
			sid := x.base.ids[stop]
			x.incident[sid] = append(x.incident[sid], patternOccurrence{li, pos})
			if x.snap.Stats.DirectEntries > c.MaxDirectEntries-len(line.trips) {
				err = fmt.Errorf("Transfer Patterns incomplete: direct timetable entry budget")
				return
			}
			x.snap.Stats.DirectEntries += len(line.trips)
		}
	}
	x.snap.PhasesNS["direct_incidence"] = time.Since(start).Nanoseconds()
	return
}
func (x *PatternIndex) addPattern(ctx context.Context, source, target string, departure int64, j model.Journey) error {
	edges := []PatternEdge{}
	lastTrip := ""
	lastSequence := -2
	lastKind := ""
	for _, leg := range j.Legs {
		if e := x.tick(ctx); e != nil {
			return e
		}
		var from, to string
		if leg.Kind == "ride" {
			c := x.connections[leg.Ref]
			from, to = c.From, c.To
			if lastKind == "ride" && lastTrip == c.TripID && lastSequence+1 == c.Sequence {
				edges[len(edges)-1].To = to
			} else {
				edges = append(edges, PatternEdge{from, to, "ride"})
			}
			lastTrip = c.TripID
			lastSequence = c.Sequence
		} else {
			w := x.footpaths[leg.Ref]
			from, to = w.From, w.To
			if lastKind == "walk" {
				edges[len(edges)-1].To = to
			} else {
				edges = append(edges, PatternEdge{from, to, "walk"})
			}
			lastTrip = ""
			lastSequence = -2
		}
		lastKind = leg.Kind
	}
	if len(edges) == 0 {
		return nil
	}
	if edges[0].From != source || edges[len(edges)-1].To != target {
		return fmt.Errorf("Transfer Patterns internal: malformed profile witness")
	}
	keyBytes, _ := json.Marshal(edges)
	key := string(keyBytes)
	if x.seen[key] {
		return nil
	}
	if x.snap.Stats.Patterns >= x.cfg.MaxPatterns {
		return fmt.Errorf("Transfer Patterns incomplete: pattern budget")
	}
	x.seen[key] = true
	if x.patternSink != nil {
		w := PatternWitness{source, target, departure, append([]PatternEdge{}, edges...), model.Journey{Legs: append([]model.Leg{}, j.Legs...)}}
		if e := x.patternSink(ctx, w); e != nil {
			return e
		}
		x.snap.Stats.Patterns++
		x.snap.Witnesses = append(x.snap.Witnesses, w)
		return nil
	}
	dag := x.dags[source]
	if dag == nil {
		if x.snap.Stats.Nodes >= x.cfg.MaxNodes {
			return fmt.Errorf("Transfer Patterns incomplete: DAG node budget")
		}
		dag = &PatternDAG{Source: source, Nodes: []PatternNode{{source, -1, "root"}}, Targets: map[string][]PatternArc{}}
		x.dags[source] = dag
		x.prefix[source] = map[patternPrefix]int{}
		x.snap.Stats.Nodes++
	}
	parent := 0
	for i, edge := range edges {
		if e := x.tick(ctx); e != nil {
			return e
		}
		if i == len(edges)-1 {
			arc := PatternArc{parent, edge.Kind}
			found := false
			for _, a := range dag.Targets[target] {
				if e := x.tick(ctx); e != nil {
					return e
				}
				if a == arc {
					found = true
					break
				}
			}
			if !found {
				if x.snap.Stats.Edges >= x.cfg.MaxEdges {
					return fmt.Errorf("Transfer Patterns incomplete: DAG edge budget")
				}
				dag.Targets[target] = append(dag.Targets[target], arc)
				x.snap.Stats.Edges++
			}
			break
		}
		key := patternPrefix{parent, edge.Kind, edge.To}
		id, ok := x.prefix[source][key]
		if !ok {
			if x.snap.Stats.Nodes >= x.cfg.MaxNodes {
				return fmt.Errorf("Transfer Patterns incomplete: DAG node budget")
			}
			if x.snap.Stats.Edges >= x.cfg.MaxEdges {
				return fmt.Errorf("Transfer Patterns incomplete: DAG edge budget")
			}
			id = len(dag.Nodes)
			dag.Nodes = append(dag.Nodes, PatternNode{edge.To, parent, edge.Kind})
			x.prefix[source][key] = id
			x.snap.Stats.Nodes++
			x.snap.Stats.Edges++
		}
		parent = id
	}
	x.snap.Stats.Patterns++
	x.snap.Witnesses = append(x.snap.Witnesses, PatternWitness{source, target, departure, append([]PatternEdge{}, edges...), model.Journey{Legs: append([]model.Leg{}, j.Legs...)}})
	return nil
}
