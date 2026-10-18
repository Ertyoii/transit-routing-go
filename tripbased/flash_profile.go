package tripbased

import (
	"fmt"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
)

type flashArrival struct {
	time              int64
	run               int
	trip, board, exit int
	walk              *paper.Path
}
type flashTripParent struct {
	stop, edge int
	walk       *paper.Path
}
type flashSegment struct{ trip, begin, end int }
type flashProfile struct {
	x           *FlashIndex
	w           *flashWork
	arrivals    [][]flashArrival
	reached     [][]int
	runReached  []int
	tripParents [][]flashTripParent
	source, run int
	departure   int64
	queues      [][]flashSegment
}

func (x *FlashIndex) profiles(w *flashWork) error {
	// Enumerate every ready-time breakpoint, including initial footpaths and >24h.
	for source := range x.names {
		departures := map[int64]bool{}
		for _, t := range x.cp.trips {
			for _, c := range t.cs {
				if e := w.tick(); e != nil {
					return e
				}
				p := x.walkMap[source][x.ids[c.From]]
				if p != nil {
					dep := c.Departure - p.Duration
					if dep >= 0 {
						departures[dep] = true
					}
				}
			}
		}
		times := []int64{}
		for t := range departures {
			times = append(times, t)
		}
		sort.Slice(times, func(i, j int) bool { return times[i] > times[j] })
		if len(times) == 0 {
			continue
		}
		p := flashProfile{x: x, w: w, source: source}
		rounds := x.cfg.MaxBoardings + 1
		p.arrivals = make([][]flashArrival, rounds)
		p.reached = make([][]int, rounds)
		p.tripParents = make([][]flashTripParent, rounds)
		for n := 0; n < rounds; n++ {
			p.arrivals[n] = make([]flashArrival, len(x.names))
			for i := range p.arrivals[n] {
				p.arrivals[n][i] = flashArrival{time: paper.Infinity, trip: -1}
			}
			p.reached[n] = make([]int, len(x.cp.trips))
			for i, t := range x.cp.trips {
				p.reached[n][i] = len(t.cs)
			}
			p.tripParents[n] = make([]flashTripParent, len(x.cp.trips))
		}
		for i, dep := range times {
			if e := w.run(true); e != nil {
				return e
			}
			p.run = i + 1
			p.departure = dep
			if e := p.search(); e != nil {
				return e
			}
			if e := p.flagJourneys(); e != nil {
				return e
			}
		}
	}
	return nil
}
func (p *flashProfile) enqueue(ti, board, n int, parent flashTripParent) error {
	if e := p.w.tick(); e != nil {
		return e
	}
	if n > p.x.cfg.MaxBoardings {
		return nil
	}
	s := &p.x.snap.Stats
	rt := p.reached[n]
	t := p.x.cp.trips[ti]
	if p.runReached[ti] <= board {
		s.EnqueuePruned[0]++
		return nil
	}
	if rt[ti] < board {
		s.EnqueuePruned[1]++
		return nil
	}
	if n > 1 && p.reached[n-1][ti] <= board {
		s.EnqueuePruned[2]++
		return nil
	}
	if t.rank > 0 && rt[p.x.cp.lines[t.line].trips[t.rank-1]] <= board {
		s.EnqueuePruned[3]++
		return nil
	}
	if rt[ti] == board {
		s.EqualReachedRepairs++
	}
	p.queues[n] = append(p.queues[n], flashSegment{ti, board, p.runReached[ti]})
	s.Queued++
	for _, later := range p.x.cp.lines[t.line].trips[t.rank:] {
		if e := p.w.tick(); e != nil {
			return e
		}
		p.runReached[later] = min(p.runReached[later], board)
		for m := n; m <= p.x.cfg.MaxBoardings; m++ {
			p.reached[m][later] = min(p.reached[m][later], board)
		}
	}
	// Only a genuinely enqueued trip owns a parent; propagated later-trip labels
	// are pruning bounds, never witness pointers.
	p.tripParents[n][ti] = parent
	return nil
}
func (p *flashProfile) add(stop, n int, time int64, ti, exit int, walk *paper.Path) error {
	if e := p.w.tick(); e != nil {
		return e
	}
	old := p.arrivals[n][stop]
	if old.time == time && old.run == p.run || old.time < time || n > 0 && p.arrivals[n-1][stop].time <= time {
		return nil
	}
	if old.time == time && old.run != p.run {
		p.x.snap.Stats.EqualArrivalRepairs++
	}
	board := -1
	if ti >= 0 {
		board = p.runReached[ti]
	}
	p.arrivals[n][stop] = flashArrival{time, p.run, ti, board, exit, walk}
	for m := n + 1; m <= p.x.cfg.MaxBoardings; m++ {
		if p.arrivals[m][stop].time <= time {
			break
		}
		p.arrivals[m][stop] = flashArrival{time: time, trip: -1}
	}
	return nil
}
func (p *flashProfile) search() error {
	x := p.x
	p.queues = make([][]flashSegment, x.cfg.MaxBoardings+1)
	p.runReached = make([]int, len(x.cp.trips))
	for i, t := range x.cp.trips {
		p.runReached[i] = len(t.cs)
	}
	for i := range x.walks[p.source] {
		fp := &x.walks[p.source][i]
		v := x.ids[fp.To]
		if e := p.add(v, 0, p.departure+fp.Duration, -1, -1, fp); e != nil {
			return e
		}
	}
	// Sorted lines and stop indices; every earliest boardable line occurrence.
	for _, l := range x.cp.lines {
		for pos := 0; pos < len(x.cp.trips[l.trips[0]].cs); pos++ {
			if e := p.w.tick(); e != nil {
				return e
			}
			stop := x.ids[x.cp.trips[l.trips[0]].cs[pos].From]
			fp := x.walkMap[p.source][stop]
			if fp == nil {
				continue
			}
			ready := p.departure + fp.Duration
			rank := sort.Search(len(l.trips), func(i int) bool { return x.cp.trips[l.trips[i]].cs[pos].Departure >= ready })
			if rank < len(l.trips) {
				if e := p.enqueue(l.trips[rank], pos, 1, flashTripParent{p.source, -1, fp}); e != nil {
					return e
				}
			}
		}
	}
	for n := 1; n <= x.cfg.MaxBoardings && len(p.queues[n]) > 0; n++ {
		queue := p.queues[n]
		sort.Slice(queue, func(i, j int) bool {
			a, b := queue[i], queue[j]
			ai, bi := x.eventOffset[a.trip]+a.begin, x.eventOffset[b.trip]+b.begin
			if ai == bi {
				return a.end < b.end
			}
			return ai < bi
		})
		// Four phases: all direct arrivals, all footpaths, local then walking transfers.
		for phase := 0; phase < 4; phase++ {
			for _, seg := range queue {
				t := x.cp.trips[seg.trip]
				for i := seg.begin; i < seg.end; i++ {
					if e := p.w.tick(); e != nil {
						return e
					}
					c := t.cs[i]
					stop := x.ids[c.To]
					if phase == 0 {
						if e := p.add(stop, n, c.Arrival, seg.trip, i+1, nil); e != nil {
							return e
						}
						continue
					}
					if phase == 1 {
						for wi := range x.walks[stop] {
							fp := &x.walks[stop][wi]
							if fp.Duration == 0 {
								continue
							}
							if e := p.add(x.ids[fp.To], n, c.Arrival+fp.Duration, seg.trip, i+1, fp); e != nil {
								return e
							}
						}
						continue
					}
					if n == x.cfg.MaxBoardings {
						continue
					}
					if c.Arrival > p.arrivals[n][stop].time {
						x.snap.Stats.LocalPruned++
						continue
					}
					for ei, tr := range x.cp.links[seg.trip][i] {
						if e := p.w.tick(); e != nil {
							return e
						}
						local := tr.walk.Duration == 0
						if phase == 2 && !local || phase == 3 && local {
							continue
						}
						if !local && c.Arrival+tr.walk.Duration > p.arrivals[n][x.ids[tr.walk.To]].time {
							x.snap.Stats.FootpathPruned++
							continue
						}
						if e := p.enqueue(tr.trip, tr.board, n+1, flashTripParent{stop, x.transferIDs[seg.trip][i][ei], tr.walk}); e != nil {
							return e
						}
					}
				}
			}
		}
	}
	return nil
}
func (p *flashProfile) path(stop, n int) (*flashPath, *paper.Path, []int, error) {
	if e := p.w.tick(); e != nil {
		return nil, nil, nil, e
	}
	a := p.arrivals[n][stop]
	if n == 0 {
		return nil, p.x.walkMap[p.source][stop], nil, nil
	}
	if a.trip < 0 || a.board < 0 || a.exit <= a.board {
		return nil, nil, nil, fmt.Errorf("FLASH-TB internal: missing canonical stop parent")
	}
	parent := p.tripParents[n][a.trip]
	var prefix *flashPath
	ids := []int{}
	if n > 1 {
		prev, _, edges, e := p.path(parent.stop, n-1)
		if e != nil {
			return nil, nil, nil, e
		}
		prefix = prev
		ids = append(ids, edges...)
		if parent.edge < 0 {
			return nil, nil, nil, fmt.Errorf("FLASH-TB internal: missing transfer parent")
		}
		ids = append(ids, parent.edge)
		// Local pruning must make the chosen stop representative end by exiting a
		// vehicle here, so this parent has no final nonempty footpath to discard.
		old := p.arrivals[n-1][parent.stop]
		if old.walk != nil && old.walk.Duration > 0 {
			return nil, nil, nil, fmt.Errorf("FLASH-TB internal: noncanonical transfer parent")
		}
	}
	return &flashPath{prefix, parent.walk, a.trip, a.board, a.exit}, a.walk, ids, nil
}
func (p *flashProfile) flagJourneys() error {
	x := p.x
	for target := range x.names {
		for n := 1; n <= x.cfg.MaxBoardings; n++ {
			if e := p.w.tick(); e != nil {
				return e
			}
			a := p.arrivals[n][target]
			if a.run != p.run || a.time >= p.arrivals[n-1][target].time {
				continue
			}
			path, walk, ids, e := p.path(target, n)
			if e != nil {
				return e
			}
			cell := x.snap.Partition.Cells[x.names[target]]
			newFlag := false
			for _, edge := range ids {
				if !x.flagged(edge, cell) {
					x.flags[cell][edge/64] |= uint64(1) << uint(edge%64)
					x.snap.Stats.SetFlags++
					newFlag = true
				}
			}
			if !newFlag && x.profileSink == nil {
				continue
			}
			if newFlag && len(x.snap.Witnesses) >= x.cfg.Limits.MaxWitnesses {
				return fmt.Errorf("FLASH-TB incomplete: flag witness budget exhausted")
			}
			j, e := x.unpackPath(path, walk, p.departure, p.w)
			if e != nil {
				return e
			}
			witness := FlashWitness{Source: x.names[p.source], Departure: p.departure, Target: x.names[target], Boardings: n, TransferIDs: ids, Journey: j}
			if x.profileSink != nil {
				if e := x.profileSink(witness); e != nil {
					return e
				}
			}
			if newFlag {
				x.snap.Witnesses = append(x.snap.Witnesses, witness)
			}
		}
	}
	return nil
}
