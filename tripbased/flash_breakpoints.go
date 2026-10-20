package tripbased

import (
	"container/heap"
	"github.com/ertyoii/transit-routing-go/model"
	"sort"
)

type ruleBreakpoint struct {
	stop, index   int
	offset, ready int64
}
type ruleBreakpointQueue []ruleBreakpoint

func (h ruleBreakpointQueue) Len() int { return len(h) }
func (h ruleBreakpointQueue) Less(i, j int) bool {
	a, b := h[i], h[j]
	if a.ready != b.ready {
		return a.ready > b.ready
	}
	if a.stop != b.stop {
		return a.stop < b.stop
	}
	return a.index < b.index
}
func (h ruleBreakpointQueue) Swap(i, j int) { h[i], h[j] = h[j], h[i] }
func (h *ruleBreakpointQueue) Push(v any)   { *h = append(*h, v.(ruleBreakpoint)) }
func (h *ruleBreakpointQueue) Pop() any     { a := *h; v := a[len(a)-1]; *h = a[:len(a)-1]; return v }

func (x *FlashIndex) ruleWindow() (int64, int64) {
	if x.base != nil && x.base.p != nil && x.base.p.Data.Window != nil {
		return x.base.p.Data.Window.Start, x.base.p.Data.Window.End
	}
	return 0, model.MaxServiceSecond
}

// Index once by legal boarding stop. Discovery is O(C log C), not a full C
// connection scan for every source. Keep every distinct exact departure.
func (x *FlashIndex) ruleDepartureLists(w *flashWork) ([][]int64, error) {
	lists := make([][]int64, len(x.names))
	_, end := x.ruleWindow()
	for _, t := range x.cp.trips {
		for _, c := range t.cs {
			if e := w.tick(); e != nil {
				return nil, e
			}
			if c.Pickup && c.Arrival <= end {
				s := x.ids[c.From]
				lists[s] = append(lists[s], c.Departure)
			}
		}
	}
	for s := range lists {
		sort.Slice(lists[s], func(i, j int) bool { return lists[s][i] > lists[s][j] })
		out := lists[s][:0]
		for _, d := range lists[s] {
			if e := w.tick(); e != nil {
				return nil, e
			}
			if len(out) == 0 || out[len(out)-1] != d {
				out = append(out, d)
			}
		}
		lists[s] = out
	}
	return lists, nil
}

// K-way merge of reachable stop lists shifted by canonical access durations.
// Equal readiness times coalesce; memory is O(reachable boarding stops), with
// no sampled/coarsened times and no entire per-source breakpoint array.
func (x *FlashIndex) eachRuleBreakpoint(source int, lists [][]int64, w *flashWork, visit func(int64) error) error {
	q := &ruleBreakpointQueue{}
	lower, upper := x.ruleWindow()
	for _, p := range x.walks[source] {
		if e := w.tick(); e != nil {
			return e
		}
		s := x.ids[p.To]
		if len(lists[s]) > 0 && lists[s][0]-p.Duration >= lower {
			heap.Push(q, ruleBreakpoint{s, 0, p.Duration, lists[s][0] - p.Duration})
		}
	}
	seen := false
	var previous int64
	for q.Len() > 0 {
		if e := w.tick(); e != nil {
			return e
		}
		v := heap.Pop(q).(ruleBreakpoint)
		if v.ready <= upper && (!seen || v.ready != previous) {
			if e := visit(v.ready); e != nil {
				return e
			}
			seen = true
			previous = v.ready
		}
		v.index++
		if v.index < len(lists[v.stop]) {
			v.ready = lists[v.stop][v.index] - v.offset
			if v.ready >= lower {
				heap.Push(q, v)
			}
		}
	}
	return nil
}
func (x *FlashIndex) ruleProfiles(w *flashWork) error {
	if x.cfg.ProfilePolicy == "retained_exact_event_v1" {
		return x.retainedRuleProfiles(w)
	}
	lists, e := x.ruleDepartureLists(w)
	if e != nil {
		return e
	}
	for s := range x.names {
		source := s
		if e = x.eachRuleBreakpoint(source, lists, w, func(ready int64) error {
			if e := w.run(true); e != nil {
				return e
			}
			return x.ruleProfile(source, ready, w)
		}); e != nil {
			return e
		}
	}
	return nil
}
