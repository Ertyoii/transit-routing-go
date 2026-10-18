package ultra

import (
	"container/heap"
	"github.com/ertyoii/transit-routing-go/paper"
)

type rangeLabel struct {
	label
	run       int
	candidate bool
}
type rangeState struct{ rounds [3][]rangeLabel }

func newRangeState(n int) rangeState {
	s := rangeState{}
	for k := range s.rounds {
		s.rounds[k] = make([]rangeLabel, n)
		for i := range s.rounds[k] {
			s.rounds[k][i].time = paper.Infinity
		}
	}
	return s
}
func values(a []rangeLabel) []label {
	out := make([]label, len(a))
	for i, v := range a {
		out[i] = v.label
	}
	return out
}

// Retained paths must keep absolute times: the caller's current departure cannot
// be applied to a path inherited from a later run. Copies share immutable tails.
func absolutePrefix(p *trace, departure int64, memo map[*trace]*trace, w *work) (*trace, error) {
	if p == nil {
		return nil, nil
	}
	stack := []*trace{}
	at := p
	for at != nil && memo[at] == nil {
		stack = append(stack, at)
		at = at.previous
	}
	for i := len(stack) - 1; i >= 0; i-- {
		if e := w.edge(); e != nil {
			return nil, e
		}
		w.s.PrefixCopies++
		old := stack[i]
		next := *old
		next.previous = memo[old.previous]
		if next.relative {
			next.departure += departure
			next.arrival += departure
			next.relative = false
		}
		memo[old] = &next
	}
	return memo[p], nil
}

// Section3.2 self-pruning repair: a current candidate prefix may replace an
// equal old-run label, but never a same-run tie or a fewer-trip witness.
func rangeAccept(old rangeLabel, next rangeLabel, lower rangeLabel, run int, w *work) bool {
	if next.time < old.time {
		return true
	}
	if next.time == old.time && next.candidate && next.run == run && old.run != run && lower.time > next.time {
		w.s.EqualLabelRepairs++
		return true
	}
	if old.time <= next.time && old.run != run {
		w.s.RetainedLabelPrunes++
	}
	return false
}
func propagateLower(s *rangeState, round, v int) {
	for k := round + 1; k < 3; k++ {
		if s.rounds[round][v].time < s.rounds[k][v].time {
			s.rounds[k][v] = s.rounds[round][v]
		}
	}
}

// A source's rounds persist across descending departure runs. Walking searches
// finish completely; optional retained witness queues/limit pruning are omitted.
func (x *Index) runRange(direct []label, departure int64, run int, s *rangeState, w *work) ([]label, error) {
	marked0 := make([]bool, len(x.names))
	marked1 := make([]bool, len(x.names))
	memo := map[*trace]*trace{}
	for v, l := range direct {
		s.rounds[0][v] = rangeLabel{label: label{time: paper.Infinity}}
		if l.time >= paper.Infinity || l.time+departure > x.maximumArrival {
			continue
		}
		p, e := absolutePrefix(l.path, departure, memo, w)
		if e != nil {
			return nil, e
		}
		a := rangeLabel{label: label{l.time + departure, 0, p}, run: run, candidate: l.path == nil}
		s.rounds[0][v] = a
		marked0[v] = true
		if a.time < s.rounds[1][v].time {
			s.rounds[1][v] = a
			marked1[v] = true
			propagateLower(s, 1, v)
		}
	}
	for round := 1; round <= 2; round++ {
		inputMarks := marked0
		if round == 2 {
			inputMarks = marked1
		}
		seeds := make([]bool, len(x.names))
		changed := make([]bool, len(x.names))
		if e := x.scanRange(s, round, run, inputMarks, seeds, changed, w); e != nil {
			return nil, e
		}
		if e := x.walkRange(s, round, run, seeds, changed, w); e != nil {
			return nil, e
		}
		if round == 1 {
			for v, b := range changed {
				marked1[v] = marked1[v] || b
			}
		}
	}
	out := emptyLabels(len(x.names))
	for v, l := range s.rounds[2] {
		if l.run == run && l.candidate && l.trips == 2 && l.time < s.rounds[1][v].time {
			out[v] = l.label
		}
	}
	return out, nil
}
func (x *Index) scanRange(s *rangeState, round, run int, marked, seeds, changed []bool, w *work) error {
	prev, curr := s.rounds[round-1], s.rounds[round]
	selected, e := x.collectRoutes(values(prev), x.maximumArrival, -1, marked, w)
	if e != nil {
		return e
	}
	for _, o := range selected {
		r := x.routes[o.route]
		w.s.RouteScans++
		current := -1
		var parent rangeLabel
		live := false
		for pos := o.position; pos < len(r.trips[0].cs); pos++ {
			if e := w.connection(); e != nil {
				return e
			}
			from := x.ids[r.trips[0].cs[pos].From]
			lo, hi := 0, len(r.trips)
			if current >= 0 {
				hi = current
			}
			for lo < hi {
				if e := w.connection(); e != nil {
					return e
				}
				w.s.TripProbes++
				mid := lo + (hi-lo)/2
				if r.trips[mid].cs[pos].Departure >= prev[from].time {
					hi = mid
				} else {
					lo = mid + 1
				}
			}
			if lo < len(r.trips) && (current < 0 || lo < current) {
				current = lo
				parent = prev[from]
				live = marked[from]
			}
			if current < 0 {
				continue
			}
			if !live && marked[from] && r.trips[current].cs[pos].Departure >= prev[from].time {
				parent = prev[from]
				live = true
			}
			if !live {
				continue
			}
			// The current marked prefix also repairs an old parent on the same trip.
			if marked[from] && prev[from].run == run && prev[from].candidate && parent.run != run && r.trips[current].cs[pos].Departure >= prev[from].time {
				parent = prev[from]
			}
			c := r.trips[current].cs[pos]
			to := x.ids[c.To]
			p := &trace{previous: parent.path, kind: "ride", ref: c.ID, departure: c.Departure, arrival: c.Arrival}
			next := rangeLabel{label: label{c.Arrival, parent.trips + 1, p}, run: parent.run, candidate: parent.candidate}
			// Keep the boarded trip's parent count, not one extra boarding per segment.
			if rangeAccept(curr[to], next, prev[to], run, w) {
				curr[to] = next
				seeds[to] = true
				changed[to] = true
				propagateLower(s, round, to)
			}
			parent.path = p
		}
	}
	return nil
}
func (x *Index) walkRange(s *rangeState, round, run int, seeds, changed []bool, w *work) error {
	curr, lower := s.rounds[round], s.rounds[round-1]
	q := &queue{}
	for v, b := range seeds {
		if b {
			heap.Push(q, entry{v, curr[v].time})
		}
	}
	for q.Len() > 0 {
		u := heap.Pop(q).(entry)
		if curr[u.v].time != u.t {
			continue
		}
		if e := w.settle(); e != nil {
			return e
		}
		for _, a := range x.graph[u.v] {
			if e := w.edge(); e != nil {
				return e
			}
			nt := u.t + a.duration
			if nt > x.maximumArrival {
				continue
			}
			base := curr[u.v]
			candidate := round == 1 && base.candidate
			p := &trace{previous: base.path, kind: "core", arc: a.id, departure: u.t, arrival: nt}
			next := rangeLabel{label: label{nt, base.trips, p}, run: base.run, candidate: candidate}
			if rangeAccept(curr[a.to], next, lower[a.to], run, w) {
				curr[a.to] = next
				changed[a.to] = true
				propagateLower(s, round, a.to)
				heap.Push(q, entry{a.to, nt})
			}
		}
	}
	return nil
}
