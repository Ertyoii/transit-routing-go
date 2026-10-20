package ultra

import (
	"container/heap"
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
)

// CappedConfig selects a conservative transfer-complete ULTRA-RAPTOR control.
// It does not run ULTRA's unrestricted timetable witness deletion. Walking rows
// contain every reachable transit endpoint within TransferWalkingLimit, with
// full raw witnesses. This trades shortcut sparsity for binding-cap support.
type CappedConfig struct {
	TransferWalkingLimit int64  `json:"transfer_walking_limit"`
	AccessibleOnly       bool   `json:"accessible_only"`
	Limits               Limits `json:"limits"`
}

type CappedPreparation struct {
	Complete      bool         `json:"complete"`
	Error         string       `json:"error,omitempty"`
	Profile       string       `json:"profile"`
	Config        CappedConfig `json:"config"`
	Routes        int          `json:"routes"`
	TransferRows  int          `json:"transfer_rows"`
	TransferPairs int          `json:"transfer_pairs"`
	Stats         Stats        `json:"stats"`
}

type CappedStats struct {
	Stats
	Rounds             int `json:"rounds"`
	RowUses            int `json:"row_uses"`
	TransferPairVisits int `json:"transfer_pair_visits"`
	DestinationRepairs int `json:"destination_repairs"`
	WalkSearches       int `json:"walk_searches"`
}

type CappedResult struct {
	Journeys []model.Journey `json:"journeys"`
	Complete bool            `json:"complete"`
	Error    string          `json:"error,omitempty"`
	Stats    CappedStats     `json:"stats"`
}

// CappedIndex owns immutable timetable and walking data. Queries may vary every
// role cap (transfer <= prepared ceiling), buffer, source and destination. The
// supported objective is arrival/boarding representatives, not walking Pareto.
// Static stop-pair rules and pickup/dropoff are supported. All paper.Profile
// aggregate-resource and trip/route-rule guards remain in force.
type CappedIndex struct {
	x    *Index
	prep CappedPreparation
	rows map[string][]paper.Path
	at   map[string][]occurrence
}

func (x *CappedIndex) Preparation() CappedPreparation { return x.prep }
func (x *CappedIndex) Complete() bool                 { return x.prep.Complete }

// BuildCapped performs finite, budgeted shortest walking searches, independent
// of timetable event count and query destination. No query can use an incomplete
// index. MaxRuns bounds rows, MaxShortcuts bounds nonempty transfer pairs, and
// MaxOutputLegs bounds retained raw edge witnesses. Other limits retain their
// ordinary ULTRA work meanings. No Core-CH or timetable shortcut sparsity is
// claimed in this mode.
func BuildCapped(ctx context.Context, d model.Dataset, cfg CappedConfig) (out *CappedIndex, err error) {
	out = &CappedIndex{prep: CappedPreparation{Profile: "ultra_raptor_transfer_complete_capped_v1", Config: cfg}, rows: map[string][]paper.Path{}, at: map[string][]occurrence{}}
	defer func() {
		out.prep.Complete = err == nil
		if err != nil {
			out.prep.Error = err.Error()
		}
	}()
	if !validLimits(cfg.Limits) || cfg.TransferWalkingLimit < 0 || cfg.TransferWalkingLimit > model.MaxServiceSecond {
		return out, fmt.Errorf("ULTRA capped: invalid preparation configuration")
	}
	if err = ctx.Err(); err != nil {
		return
	}
	p, e := paper.New(d)
	if e != nil {
		return out, e
	}
	x := &Index{p: p, transit: map[string]bool{}, rawOut: map[string][]model.Footpath{}, rawIn: map[string][]model.Footpath{}}
	out.x = x
	byTrip := map[string][]model.Connection{}
	for _, c := range p.Data.Connections {
		byTrip[c.TripID] = append(byTrip[c.TripID], c)
		x.transit[c.From] = true
		x.transit[c.To] = true
	}
	for _, edge := range p.Data.Footpaths {
		if cfg.AccessibleOnly && (edge.Accessible != model.Yes || p.Stops[edge.From].Accessible != model.Yes || p.Stops[edge.To].Accessible != model.Yes) {
			continue
		}
		x.rawOut[edge.From] = append(x.rawOut[edge.From], edge)
		x.rawIn[edge.To] = append(x.rawIn[edge.To], edge)
	}
	for _, m := range []map[string][]model.Footpath{x.rawOut, x.rawIn} {
		for id := range m {
			sort.Slice(m[id], func(i, j int) bool { return m[id][i].ID < m[id][j].ID })
		}
	}
	w := work{ctx, cfg.Limits, &out.prep.Stats}
	if err = out.buildCappedRoutes(byTrip, &w); err != nil {
		return
	}
	out.prep.Routes = len(x.routes)
	sources := []string{}
	for id := range x.transit {
		if !cfg.AccessibleOnly || p.Stops[id].Accessible == model.Yes {
			sources = append(sources, id)
		}
	}
	sort.Strings(sources)
	for _, source := range sources {
		if err = w.check(); err != nil {
			return
		}
		if out.prep.Stats.Runs == cfg.Limits.MaxRuns {
			return out, fmt.Errorf("ULTRA capped incomplete: transfer-row budget exhausted")
		}
		out.prep.Stats.Runs++
		var r rawSearch
		r, err = out.walk(source, cfg.TransferWalkingLimit, false, "", &w)
		if err != nil {
			return
		}
		paths := []paper.Path{}
		targets := []string{}
		for target := range r.distance {
			if x.transit[target] {
				targets = append(targets, target)
			}
		}
		sort.Strings(targets)
		for _, target := range targets {
			duration := r.distance[target]
			if target != source {
				if out.prep.TransferPairs == cfg.Limits.MaxShortcuts {
					return out, fmt.Errorf("ULTRA capped incomplete: transfer-pair budget exhausted")
				}
				out.prep.TransferPairs++
			}
			path := paper.Path{To: target, Duration: duration}
			// Charge before allocating potentially long retained raw witnesses.
			path.Edges, err = out.path(r, target, &w)
			if err != nil {
				return
			}
			paths = append(paths, path)
		}
		out.rows[source] = paths
		out.prep.TransferRows++
	}
	return
}

// Capped groups include boarding restrictions and trip accessibility. Earlier
// FIFO trips dominate later trips only when these signatures match. Equal-time
// and repeated-stop trips are safe: each position is separately indexed.
func (x *CappedIndex) buildCappedRoutes(byTrip map[string][]model.Connection, w *work) error {
	patterns := map[string][]trip{}
	ids := []string{}
	for id := range byTrip {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		cs := byTrip[id]
		sort.Slice(cs, func(i, j int) bool { return cs[i].Sequence < cs[j].Sequence })
		type signature struct {
			From, To        string
			Pickup, Dropoff bool
		}
		ss := []signature{}
		for _, c := range cs {
			if e := w.connection(); e != nil {
				return e
			}
			ss = append(ss, signature{c.From, c.To, c.Pickup, c.Dropoff})
		}
		key, _ := json.Marshal(struct {
			Segments   []signature
			Accessible model.Access
		}{ss, x.x.p.Trips[id].Accessible})
		patterns[string(key)] = append(patterns[string(key)], trip{id, cs})
	}
	keys := []string{}
	for key := range patterns {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		ts := patterns[key]
		sort.Slice(ts, func(i, j int) bool {
			if ts[i].cs[0].Departure == ts[j].cs[0].Departure {
				return ts[i].id < ts[j].id
			}
			return ts[i].cs[0].Departure < ts[j].cs[0].Departure
		})
		groups := []route{}
		for _, t := range ts {
			placed := false
			for g := range groups {
				prior := groups[g].trips[len(groups[g].trips)-1]
				ok := true
				for i, c := range t.cs {
					if e := w.connection(); e != nil {
						return e
					}
					w.s.GroupingComparisons++
					if c.Departure < prior.cs[i].Departure || c.Arrival < prior.cs[i].Arrival {
						ok = false
						break
					}
				}
				if ok {
					groups[g].trips = append(groups[g].trips, t)
					placed = true
					break
				}
			}
			if !placed {
				groups = append(groups, route{[]trip{t}})
			}
		}
		x.x.routes = append(x.x.routes, groups...)
	}
	for ri, r := range x.x.routes {
		for pos, c := range r.trips[0].cs {
			if c.Pickup {
				x.at[c.From] = append(x.at[c.From], occurrence{ri, pos})
			}
		}
	}
	return w.check()
}

// walk uses the same raw directed Dijkstra/tie policy as legacy ULTRA, with an
// absorbing destination and the accessibility-filtered prepared graph.
func (x *CappedIndex) walk(source string, maximum int64, reverse bool, absorbing string, w *work) (rawSearch, error) {
	r := rawSearch{map[string]int64{source: 0}, map[string]model.Footpath{}, source, reverse}
	q := &rawQueue{{source, 0}}
	for q.Len() > 0 {
		u := heap.Pop(q).(rawEntry)
		if r.distance[u.v] != u.t {
			continue
		}
		if e := w.settle(); e != nil {
			return r, e
		}
		if !reverse && u.v == absorbing {
			continue
		}
		edges := x.x.rawOut[u.v]
		if reverse {
			edges = x.x.rawIn[u.v]
		}
		for _, edge := range edges {
			if e := w.edge(); e != nil {
				return r, e
			}
			to := edge.To
			if reverse {
				to = edge.From
			}
			nt := u.t + edge.Duration
			if nt > maximum {
				continue
			}
			if old, ok := r.distance[to]; ok && old <= nt {
				continue
			}
			r.distance[to] = nt
			r.parent[to] = edge
			heap.Push(q, rawEntry{to, nt})
		}
	}
	return r, nil
}

func (x *CappedIndex) path(r rawSearch, target string, w *work) ([]model.Footpath, error) {
	edges := []model.Footpath{}
	for at := target; at != r.source; {
		if e := w.leg(); e != nil {
			return nil, e
		}
		edge, ok := r.parent[at]
		if !ok {
			return nil, fmt.Errorf("ULTRA capped: missing raw path parent")
		}
		edges = append(edges, edge)
		if r.reverse {
			at = edge.To
		} else {
			at = edge.From
		}
	}
	if !r.reverse {
		for i, j := 0, len(edges)-1; i < j; i, j = i+1, j-1 {
			edges[i], edges[j] = edges[j], edges[i]
		}
	}
	return edges, nil
}
