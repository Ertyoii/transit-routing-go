package tripbased

import (
	"container/heap"
	"fmt"
	"github.com/ertyoii/transit-routing-go/paper"
	"sort"
	"time"
	"unsafe"
)

type retainedSeed struct {
	trip, pos int
	departure int64
}
type retainedSegment struct {
	prefix           *flashPath
	walk             *paper.Path
	trip, begin, end int
}

// Per-source exact-event state, reused across descending readiness points.
// Only reached positions are retained for dominance, never scalar walked-stop
// labels or propagated later-trip parents. Absolute-event parent chains remain
// feasible for earlier readiness; unpackPath replays the initial walk then.
type retainedRuleProfile struct {
	x         *FlashIndex
	w         *flashWork
	source    int
	departure int64
	reached   [][]int
	arrivals  [][]ruleProfileArrival
	queues    [][]retainedSegment
	dirty     []int
	isDirty   []bool
	pending   int
}

func (x *FlashIndex) retainedRuleProfiles(w *flashWork) error {
	start, initialWork := time.Now(), x.snap.Stats.Work
	defer func() {
		x.snap.Stats.RetainedWork = x.snap.Stats.Work - initialWork
		x.snap.Stats.RetainedProfileNS = time.Since(start).Nanoseconds()
	}()
	lists := make([][]retainedSeed, len(x.names))
	_, end := x.ruleWindow()
	for ti, t := range x.cp.trips {
		for pos, c := range t.cs {
			if e := w.tick(); e != nil {
				return e
			}
			if c.Pickup && c.Arrival <= end {
				v := x.ids[c.From]
				lists[v] = append(lists[v], retainedSeed{ti, pos, c.Departure})
			}
		}
	}
	for v := range lists {
		sort.Slice(lists[v], func(i, j int) bool {
			a, b := lists[v][i], lists[v][j]
			if a.departure != b.departure {
				return a.departure > b.departure
			}
			if a.trip != b.trip {
				return a.trip < b.trip
			}
			return a.pos < b.pos
		})
		x.snap.Stats.RetainedSeedPayloadBytes += cap(lists[v]) * int(unsafe.Sizeof(retainedSeed{}))
	}
	p := retainedRuleProfile{x: x, w: w}
	lower, upper := x.ruleWindow()
	first := 0
	if x.cfg.RetainedSourceStart != "" {
		first = x.ids[x.cfg.RetainedSourceStart]
	}
	// Rotation is a complete permutation, never a source subset. It permits
	// owner-controlled staged samples at bus/rail origins without changing data.
	for offset := range x.names {
		source := (first + offset) % len(x.names)
		q := &ruleBreakpointQueue{}
		for _, fp := range x.walks[source] {
			if e := w.tick(); e != nil {
				return e
			}
			v := x.ids[fp.To]
			if len(lists[v]) > 0 && lists[v][0].departure-fp.Duration >= lower {
				heap.Push(q, ruleBreakpoint{v, 0, fp.Duration, lists[v][0].departure - fp.Duration})
			}
		}
		if q.Len() == 0 {
			continue
		}
		if x.snap.Stats.RetainedSourcesStarted >= x.cfg.Limits.MaxRetainedSources {
			return fmt.Errorf("retained profiles incomplete: source guard")
		}
		x.snap.Stats.RetainedSourcesStarted++
		if e := p.reset(source); e != nil {
			return e
		}
		for q.Len() > 0 {
			ready := (*q)[0].ready
			if ready > upper {
				return fmt.Errorf("retained profiles internal: readiness exceeds window")
			}
			if e := w.tick(); e != nil {
				return e
			}
			if x.snap.Stats.RetainedReadiness >= x.cfg.Limits.MaxRetainedReadiness {
				return fmt.Errorf("retained profiles incomplete: offered-readiness guard")
			}
			x.snap.Stats.RetainedReadiness++
			p.departure = ready
			// Offer each newly eligible raw boarding event once. Equal exact
			// readiness times coalesce; no event is rounded, sampled or dropped.
			for q.Len() > 0 && (*q)[0].ready == ready {
				if e := w.tick(); e != nil {
					return e
				}
				v := heap.Pop(q).(ruleBreakpoint)
				seed := lists[v.stop][v.index]
				x.snap.Stats.RetainedSeedOffers++
				if e := p.enqueue(1, seed.trip, seed.pos, nil, x.walkMap[source][v.stop]); e != nil {
					return e
				}
				v.index++
				if v.index < len(lists[v.stop]) {
					v.ready = lists[v.stop][v.index].departure - v.offset
					if v.ready >= lower {
						heap.Push(q, v)
					}
				}
			}
			if p.pending > 0 {
				if x.snap.Stats.RetainedPropagationRuns >= x.cfg.Limits.MaxRetainedRuns {
					return fmt.Errorf("retained profiles incomplete: propagation-run guard")
				}
				x.snap.Stats.RetainedPropagationRuns++
				if e := p.propagate(); e != nil {
					return e
				}
				if e := p.emit(); e != nil {
					return e
				}
			}
		}
		x.snap.Stats.RetainedSourcesCompleted++
	}
	return nil
}

func (p *retainedRuleProfile) reset(source int) error {
	x := p.x
	k := x.cfg.MaxBoardings
	if p.reached == nil {
		p.reached = make([][]int, k+1)
		p.arrivals = make([][]ruleProfileArrival, k+1)
		p.queues = make([][]retainedSegment, k+1)
		p.isDirty = make([]bool, len(x.names))
		p.dirty = make([]int, 0, len(x.names))
		for n := 1; n <= k; n++ {
			p.reached[n] = make([]int, len(x.cp.trips))
			p.arrivals[n] = make([]ruleProfileArrival, len(x.names))
		}
		x.snap.Stats.RetainedScratchPayloadBytes = k*len(x.cp.trips)*int(unsafe.Sizeof(int(0))) + k*len(x.names)*int(unsafe.Sizeof(ruleProfileArrival{})) + len(x.names)*(1+int(unsafe.Sizeof(int(0))))
	}
	p.source = source
	for n := 1; n <= k; n++ {
		for ti, t := range x.cp.trips {
			if e := p.w.tick(); e != nil {
				return e
			}
			p.reached[n][ti] = len(t.cs)
			x.snap.Stats.RetainedTripInitializations++
		}
		for v := range p.arrivals[n] {
			if e := p.w.tick(); e != nil {
				return e
			}
			p.arrivals[n][v] = ruleProfileArrival{at: paper.Infinity}
			x.snap.Stats.RetainedTargetInitializations++
		}
	}
	return nil
}

func (p *retainedRuleProfile) enqueue(n, ti, board int, prefix *flashPath, walk *paper.Path) error {
	if e := p.w.tick(); e != nil {
		return e
	}
	s := &p.x.snap.Stats
	s.RetainedEnqueueTests++
	old := p.reached[n][ti]
	if board >= old {
		return nil
	}
	if p.pending >= p.x.cfg.Limits.MaxRetainedQueued {
		return fmt.Errorf("retained profiles incomplete: pending-segment guard")
	}
	p.reached[n][ti] = board
	p.queues[n] = append(p.queues[n], retainedSegment{prefix, walk, ti, board, old})
	p.pending++
	s.RetainedPeakQueued = max(s.RetainedPeakQueued, p.pending)
	capacity := 0
	for _, q := range p.queues {
		capacity += cap(q)
	}
	s.RetainedQueuePayloadBytes = max(s.RetainedQueuePayloadBytes, capacity*int(unsafe.Sizeof(retainedSegment{})))
	return nil
}

func (p *retainedRuleProfile) propagate() error {
	x := p.x
	s := &x.snap.Stats
	_, windowEnd := x.ruleWindow()
	k := x.cfg.MaxBoardings
	for n := 1; n <= k; n++ {
		for head := 0; head < len(p.queues[n]); head++ {
			seg := p.queues[n][head]
			p.queues[n][head] = retainedSegment{} // release parent pointers for GC
			p.pending--
			t := x.cp.trips[seg.trip]
			for i := seg.begin; i < seg.end; i++ {
				if e := p.w.tick(); e != nil {
					return e
				}
				s.RetainedEvents++
				c := t.cs[i]
				if c.Arrival > windowEnd {
					break
				}
				if !c.Dropoff {
					continue
				}
				var path *flashPath
				parent := func() *flashPath {
					if path == nil {
						path = &flashPath{seg.prefix, seg.walk, seg.trip, seg.begin, i + 1}
						s.RetainedPathNodes++
					}
					return path
				}
				for wi := range x.walks[x.ids[c.To]] {
					if e := p.w.tick(); e != nil {
						return e
					}
					s.RetainedWalkRelaxations++
					fp := &x.walks[x.ids[c.To]][wi]
					v := x.ids[fp.To]
					at := c.Arrival + fp.Duration
					if at <= windowEnd && at < p.arrivals[n][v].at {
						p.arrivals[n][v] = ruleProfileArrival{at, parent(), fp}
						if !p.isDirty[v] {
							p.isDirty[v] = true
							p.dirty = append(p.dirty, v)
						}
					}
				}
				if n == k {
					continue
				}
				for _, tr := range x.cp.links[seg.trip][i] {
					if e := p.w.tick(); e != nil {
						return e
					}
					s.RetainedTransferTests++
					if tr.board < p.reached[n+1][tr.trip] {
						if e := p.enqueue(n+1, tr.trip, tr.board, parent(), tr.walk); e != nil {
							return e
						}
					}
				}
			}
		}
		p.queues[n] = p.queues[n][:0]
	}
	return nil
}

func (p *retainedRuleProfile) emit() error {
	x := p.x
	// Changes are emitted only for touched target vectors. Unchanged witnesses
	// already inserted at later readiness remain feasible; lower-board/direct
	// arrivals never get worse as readiness decreases, so cannot uncover an old
	// previously dominated transit arrival. Extra feasible patterns are harmless.
	sort.Ints(p.dirty)
	for _, target := range p.dirty {
		best := paper.Infinity
		if fp := x.walkMap[p.source][target]; fp != nil {
			best = p.departure + fp.Duration
		}
		for n := 1; n <= x.cfg.MaxBoardings; n++ {
			if e := p.w.tick(); e != nil {
				return e
			}
			x.snap.Stats.RetainedTargetChecks++
			a := p.arrivals[n][target]
			if a.at >= best {
				continue
			}
			best = a.at
			j, e := x.unpackPath(a.path, a.walk, p.departure, p.w)
			if e != nil {
				return e
			}
			if e = x.profileSink(FlashWitness{Source: x.names[p.source], Target: x.names[target], Departure: p.departure, Boardings: n, Journey: j}); e != nil {
				return e
			}
			x.snap.Stats.RetainedEmissions++
		}
		p.isDirty[target] = false
	}
	p.dirty = p.dirty[:0]
	return nil
}
