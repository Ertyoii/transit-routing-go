package tripbased

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/model"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type flashPath struct {
	previous          *flashPath
	walk              *paper.Path
	trip, board, exit int
}
type flashLabel struct {
	time int64
	path *flashPath
	walk *paper.Path
}

func flashEmpty(n int) []flashLabel {
	r := make([]flashLabel, n)
	for i := range r {
		r[i].time = paper.Infinity
	}
	return r
}
func (x *FlashIndex) unpackPath(p *flashPath, final *paper.Path, departure int64, w *flashWork) (model.Journey, error) {
	chain := []*flashPath{}
	for at := p; at != nil; at = at.previous {
		if e := w.tick(); e != nil {
			return model.Journey{}, e
		}
		chain = append(chain, at)
	}
	var parent *paper.Node
	at := departure
	for i := len(chain) - 1; i >= 0; i-- {
		v := chain[i]
		if v.walk != nil {
			for _, e := range v.walk.Edges {
				if err := w.tick(); err != nil {
					return model.Journey{}, err
				}
				parent = &paper.Node{Previous: parent, Leg: model.Leg{Kind: "walk", Ref: e.ID, Departure: at, Arrival: at + e.Duration}}
				at += e.Duration
			}
		}
		for k := v.board; k < v.exit; k++ {
			if e := w.tick(); e != nil {
				return model.Journey{}, e
			}
			c := x.base.trips[v.trip].cs[k]
			parent = paper.Ride(parent, c)
			at = c.Arrival
		}
	}
	if final != nil {
		for _, e := range final.Edges {
			if err := w.tick(); err != nil {
				return model.Journey{}, err
			}
			parent = &paper.Node{Previous: parent, Leg: model.Leg{Kind: "walk", Ref: e.ID, Departure: at, Arrival: at + e.Duration}}
			at += e.Duration
		}
	}
	return paper.Journey(parent), nil
}

// Complete fresh canonical RAPTOR rounds implement the Trans-ULTRA construction.
// There is no retained-label or candidate-radius approximation in this phase.
func (x *FlashIndex) canonicalRound(prev []flashLabel, w *flashWork) ([]flashLabel, error) {
	curr := append([]flashLabel{}, prev...)
	rides := flashEmpty(len(prev))
	for _, l := range x.base.lines {
		current := -1
		var boarded flashLabel
		board := -1
		for pos := 0; pos < len(x.base.trips[l.trips[0]].cs); pos++ {
			if e := w.tick(); e != nil {
				return nil, e
			}
			from := x.ids[x.base.trips[l.trips[0]].cs[pos].From]
			end := len(l.trips)
			if current >= 0 {
				end = current
			}
			rank := sort.Search(end, func(i int) bool { return x.base.trips[l.trips[i]].cs[pos].Departure >= prev[from].time })
			if rank < len(l.trips) && (current < 0 || rank < current) {
				current = rank
				boarded = prev[from]
				board = pos
			}
			if current < 0 {
				continue
			}
			ti := l.trips[current]
			c := x.base.trips[ti].cs[pos]
			to := x.ids[c.To]
			if c.Arrival < curr[to].time {
				v := flashLabel{c.Arrival, &flashPath{boarded.path, boarded.walk, ti, board, pos + 1}, nil}
				curr[to] = v
				rides[to] = v
			}
		}
	}
	exits := []int{}
	for v, l := range rides {
		if l.time < paper.Infinity {
			exits = append(exits, v)
		}
	}
	sort.Slice(exits, func(i, j int) bool {
		a, b := rides[exits[i]].path, rides[exits[j]].path
		return x.eventOffset[a.trip]+a.exit < x.eventOffset[b.trip]+b.exit
	})
	for _, u := range exits {
		l := rides[u]
		for i := range x.walks[u] {
			if e := w.tick(); e != nil {
				return nil, e
			}
			p := &x.walks[u][i]
			if p.Duration == 0 {
				continue
			}
			v := x.ids[p.To]
			arrival := l.time + p.Duration
			if arrival < curr[v].time {
				curr[v] = flashLabel{arrival, l.path, p}
			}
		}
	}
	return curr, nil
}

type flashKey struct{ from, exit, to, board int }

func (x *FlashIndex) transULTRA(w *flashWork) error {
	keys := map[flashKey]int{}
	x.transferIDs = make([][][]int, len(x.base.trips))
	for i, t := range x.base.trips {
		x.transferIDs[i] = make([][]int, len(t.cs))
	}
	deps := make([]map[int64]bool, len(x.names))
	for i := range deps {
		deps[i] = map[int64]bool{}
	}
	for _, t := range x.base.trips {
		for _, c := range t.cs {
			deps[x.ids[c.From]][c.Departure] = true
		}
	}
	for s := range x.names {
		times := []int64{}
		for t := range deps[s] {
			times = append(times, t)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] > times[j] })
		for _, dep := range times {
			if e := w.run(false); e != nil {
				return e
			}
			zero := flashEmpty(len(x.names))
			for i := range x.walks[s] {
				p := &x.walks[s][i]
				zero[x.ids[p.To]] = flashLabel{dep + p.Duration, nil, p}
			}
			one, e := x.canonicalRound(zero, w)
			if e != nil {
				return e
			}
			two, e := x.canonicalRound(one, w)
			if e != nil {
				return e
			}
			for target, label := range two {
				if e := w.tick(); e != nil {
					return e
				}
				p := label.path
				if p == nil || p.previous == nil || p.previous.previous != nil || label.time >= one[target].time || label.walk != nil && label.walk.Duration != 0 || p.previous.walk != nil && p.previous.walk.Duration != 0 {
					continue
				}
				k := flashKey{p.previous.trip, p.previous.exit - 1, p.trip, p.board}
				if _, exists := keys[k]; exists {
					continue
				}
				if len(x.snap.Transfers) >= x.cfg.Limits.MaxTransfers {
					return fmt.Errorf("FLASH-TB incomplete: canonical transfer budget exhausted")
				}
				walk := p.walk
				if walk == nil {
					u := x.ids[x.base.trips[p.previous.trip].cs[p.previous.exit-1].To]
					walk = x.walkMap[u][u]
				}
				candidate, e := x.unpackPath(p, nil, dep, w)
				if e != nil {
					return e
				}
				id := len(x.snap.Transfers)
				keys[k] = id
				x.cp.links[k.from][k.exit] = append(x.cp.links[k.from][k.exit], link{k.to, k.board, walk})
				x.transferIDs[k.from][k.exit] = append(x.transferIDs[k.from][k.exit], id)
				raw := []string{}
				for _, e := range walk.Edges {
					raw = append(raw, e.ID)
				}
				x.snap.Transfers = append(x.snap.Transfers, FlashTransfer{FromTrip: x.base.trips[k.from].id, Exit: k.exit + 1, ToTrip: x.base.trips[k.to].id, Board: k.board, WalkSeconds: walk.Duration, RawEdges: raw, Cells: []int{}, Source: x.names[s], Departure: dep, Target: x.names[target], Candidate: candidate})
			}
		}
	}
	// Canonical transfer scan order is receiving event order within each exit.
	for ti := range x.cp.links {
		for i := range x.cp.links[ti] {
			links, ids := x.cp.links[ti][i], x.transferIDs[ti][i]
			order := make([]int, len(links))
			for j := range order {
				order[j] = j
			}
			sort.Slice(order, func(a, b int) bool {
				u, v := links[order[a]], links[order[b]]
				return x.eventOffset[u.trip]+u.board < x.eventOffset[v.trip]+v.board
			})
			ll := make([]link, len(links))
			ii := make([]int, len(ids))
			for j, k := range order {
				ll[j] = links[k]
				ii[j] = ids[k]
			}
			x.cp.links[ti][i], x.transferIDs[ti][i] = ll, ii
		}
	}
	x.snap.Stats.CanonicalTransfers = len(x.snap.Transfers)
	return nil
}
